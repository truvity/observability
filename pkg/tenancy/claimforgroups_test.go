package tenancy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claimForGroupsConfig() Config {
	return Config{
		ClaimName: "groups",
		Principals: []Principal{
			{Group: "team-a", Grants: []Grant{{Cluster: "cluster-a", Namespaces: []string{"app-a", "shared"}}}},
			{Group: "team-b", Grants: []Grant{{Cluster: "cluster-a", Namespaces: []string{"app-b", "shared"}}, {Cluster: "cluster-b", Namespaces: []string{"app-b"}}}},
			{Name: "ops", Groups: []string{"ops-x", "ops-y"}, Grants: []Grant{{Cluster: "cluster-a", AllNamespaces: true}}},
		},
	}
}

func TestClaimForGroupsUnionsGrantsAndJoinsLogsIntoOneFilter(t *testing.T) {
	cfg := claimForGroupsConfig()
	require.NoError(t, cfg.Validate())

	claim, ok, err := cfg.ClaimForGroups([]string{"unrelated", "team-b", "team-a"})
	require.NoError(t, err)
	require.True(t, ok)

	// One selector per merged cluster grant; the namespaces of the two
	// principals on cluster-a are unioned, once each.
	assert.Equal(t, []string{
		`{k8s_cluster_name="cluster-a",k8s_namespace_name=~"^(app-a|app-b|shared)$"}`,
		`{k8s_cluster_name="cluster-b",k8s_namespace_name=~"^(app-b)$"}`,
	}, claim.MetricsExtraFilters)

	// ONE stream filter for the whole reach: the log store ANDs several.
	require.Len(t, claim.LogsExtraStreamFilters, 1)
	assert.Equal(t,
		`_stream:{"k8s.cluster.name"="cluster-a","kubernetes.pod_namespace"=~"^(app-a|app-b|shared)$" or "k8s.cluster.name"="cluster-b","kubernetes.pod_namespace"=~"^(app-b)$"}`,
		claim.LogsExtraStreamFilters[0])
}

func TestClaimForGroupsAllNamespacesSwallowsTheRestOfThatCluster(t *testing.T) {
	cfg := claimForGroupsConfig()
	claim, ok, err := cfg.ClaimForGroups([]string{"ops-y", "team-a"})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []string{`{k8s_cluster_name="cluster-a"}`}, claim.MetricsExtraFilters)
	assert.Equal(t, []string{`_stream:{"k8s.cluster.name"="cluster-a"}`}, claim.LogsExtraStreamFilters)
}

func TestClaimForGroupsSingleGroupEqualsThePrincipalsOwnClaim(t *testing.T) {
	cfg := claimForGroupsConfig()
	for _, p := range cfg.Principals {
		want, err := cfg.RenderClaim(p)
		require.NoError(t, err)
		got, ok, err := cfg.ClaimForGroups(p.groupList()[:1])
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, want, got, "principal %q", p.label())
	}
}

func TestClaimForGroupsNoMatchIsNotAGrant(t *testing.T) {
	cfg := claimForGroupsConfig()
	for _, groups := range [][]string{nil, {}, {"nobody"}, {"team-"}, {"TEAM-A"}, {"team-a "}, {".*"}} {
		claim, ok, err := cfg.ClaimForGroups(groups)
		require.NoError(t, err)
		assert.False(t, ok, "groups %q", groups)
		assert.Equal(t, Claim{}, claim)
	}
}

func TestClaimForGroupsIsDeterministic(t *testing.T) {
	cfg := claimForGroupsConfig()
	first, _, err := cfg.ClaimForGroups([]string{"team-a", "team-b", "ops-x"})
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		again, _, err := cfg.ClaimForGroups([]string{"ops-x", "team-b", "team-a"})
		require.NoError(t, err)
		require.Equal(t, first, again)
	}
}
