package tests

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `tenancy.identities` is a generic list of "groups -> reach" that renders into
// the objects `tenancy.principals` does. What has to hold:
//
//   - the same people written as `principals` (the case
//     tenancy-authz-identities-principals) and as `identities` (the case
//     tenancy-authz-identities) render byte for byte the same objects: the
//     tenancy-authz ConfigMap, the VMUsers, and everything else;
//   - the person entries are in the service's configuration with whole-store
//     reach as `allNamespaces` on every cluster of `storeClusters`, which is
//     what lets them read traces, and the namespace-only entry is not given
//     that reach;
//   - the proxy chart renders what the stack hands over for it (the generic
//     proof in proxy_chart_test.go runs every stack case, this one included).
//
// The refusals (a name or group under both keys, a reach that is both or
// neither, an empty groups, a whole-store reach with no storeClusters) are the
// fixtures tests/invalid/*/tenancy-identities-*.yaml.
func TestIdentitiesRenderExactlyWhatThePrincipalsRender(t *testing.T) {
	const (
		oldKey = "golden/observability-stack/tenancy-authz-identities-principals.yaml"
		newKey = "golden/observability-stack/tenancy-authz-identities.yaml"
	)
	a, err := os.ReadFile(oldKey)
	require.NoError(t, err, "regenerate the golden renders with `just golden`")
	b, err := os.ReadFile(newKey)
	require.NoError(t, err, "regenerate the golden renders with `just golden`")
	require.NotEmpty(t, a)
	// The whole render, not just the ConfigMap: a VMUser or a checksum that
	// differs is a difference a consumer switching keys would see.
	assert.Equal(t, string(a), string(b), "identities and the principals they replace render differently")
}

func TestIdentitiesReachInTheServicesConfiguration(t *testing.T) {
	cfg := authzConfigOf(t, "golden/observability-stack/tenancy-authz-identities.yaml")
	require.Len(t, cfg.Principals, 2, "the workload's principal is not the service's; the two identities are")

	var viewer, deployer = cfg.Principals[0], cfg.Principals[1]
	assert.Equal(t, "k8s:viewer", viewer.Name)
	assert.ElementsMatch(t, []string{"cluster-a:k8s:viewer", "cluster-b:k8s:viewer"}, viewer.Groups)
	require.Len(t, viewer.Grants, 2)
	for _, g := range viewer.Grants {
		assert.True(t, g.AllNamespaces, "whole-store reach is allNamespaces on %s", g.Cluster)
	}
	assert.Equal(t, cfg.Authz.TraceClusters, []string{viewer.Grants[0].Cluster, viewer.Grants[1].Cluster},
		"whole-store reach covers every cluster the trace store serves, which is what reads traces")

	assert.Equal(t, "example:example-app:deployer", deployer.Name)
	require.Len(t, deployer.Grants, 1)
	assert.False(t, deployer.Grants[0].AllNamespaces, "namespace-only reach never covers a cluster, so never traces")
	assert.Equal(t, []string{"example-app", "other-app"}, deployer.Grants[0].Namespaces)
}
