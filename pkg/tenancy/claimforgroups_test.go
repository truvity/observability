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
			{Group: "team-b", Grants: []Grant{
				{Cluster: "cluster-a", Namespaces: []string{"app-b", "shared"}},
				{Cluster: "cluster-b", Namespaces: []string{"app-b"}},
			}},
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
		`_stream:{"k8s.cluster.name"="cluster-a","kubernetes.pod_namespace"=~"^(app-a|app-b|shared)$"`+
			` or "k8s.cluster.name"="cluster-b","kubernetes.pod_namespace"=~"^(app-b)$"}`,
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

func wholeStoreConfig() Config {
	return Config{
		ClaimName: "groups",
		Principals: []Principal{
			{Group: "ns-only", Grants: []Grant{
				{Cluster: "cluster-a", Namespaces: []string{"app-a"}},
				{Cluster: "cluster-b", Namespaces: []string{"app-a"}},
			}},
			{Group: "all-a", Grants: []Grant{{Cluster: "cluster-a", AllNamespaces: true}}},
			{Group: "all-b", Grants: []Grant{{Cluster: "cluster-b", AllNamespaces: true}}},
			{Name: "fleet", Groups: []string{"fleet-x"}, Grants: []Grant{
				{Cluster: "cluster-a", AllNamespaces: true},
				{Cluster: "cluster-b", AllNamespaces: true},
			}},
		},
	}
}

func TestWholeStoreReach(t *testing.T) {
	cfg := wholeStoreConfig()
	require.NoError(t, cfg.Validate())
	both := []string{"cluster-a", "cluster-b"}

	cases := []struct {
		name     string
		groups   []string
		clusters []string
		want     bool
	}{
		{"namespace-only grant", []string{"ns-only"}, both, false},
		{"whole cluster on one of two", []string{"all-a"}, both, false},
		{"whole cluster on all, one principal", []string{"fleet-x"}, both, true},
		{"whole cluster on all, two principals", []string{"all-a", "all-b"}, both, true},
		{"whole cluster plus a namespace group", []string{"ns-only", "all-a", "all-b"}, both, true},
		{"whole cluster plus a namespace group, one cluster short", []string{"ns-only", "all-a"}, both, false},
		{"whole cluster on the only cluster served", []string{"all-a"}, []string{"cluster-a"}, true},
		{"no group selects anything", []string{"stranger"}, both, false},
		{"no groups", nil, both, false},
		{"no clusters served is never whole-store", []string{"fleet-x"}, nil, false},
		{"a cluster nobody is granted", []string{"fleet-x"}, []string{"cluster-a", "cluster-c"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cfg.WholeStoreReach(tc.groups, tc.clusters))
		})
	}
}

func TestIsTracesPath(t *testing.T) {
	for _, p := range []string{"/select/jaeger/api/services", "/select/jaeger/api/traces/abc", "/select/tempo/api/search"} {
		assert.True(t, IsTracesPath(p), p)
	}
	for _, p := range []string{"/prometheus/api/v1/query", "/select/logsql/query", "/select/jaegerx", "/x/select/jaeger/api", ""} {
		assert.False(t, IsTracesPath(p), p)
	}
}
