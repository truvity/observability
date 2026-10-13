package tests

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// `tenancy.identities` is the one list of "groups -> reach" that renders a
// person's, a workload's and a machine's reader (`tenancy.principals`, which
// it replaced, was removed together with the stack's Door plane). What has to
// hold:
//
//   - the person entries are in the service's configuration with whole-store
//     reach as `allNamespaces` on every cluster of `storeClusters`, which is
//     what lets them read traces, and the namespace-only entry is not given
//     that reach (TestIdentitiesReachInTheServicesConfiguration);
//   - every shape a principal could take is still held by a case, so a field
//     an identity gained or lost fails here (TestEveryIdentityShapeHasACase);
//   - the refusals (a name or a group twice, a reach that is both or neither,
//     an empty groups, a whole-store reach with no storeClusters, the
//     workload knobs on a person under tenancy-authz) are the fixtures
//     tests/invalid/observability-proxy/tenancy-identities-*.yaml, and
//     `tenancy.principals` itself is refused by the schema
//     (tests/invalid/*/tenancy-principals-removed.yaml).
//
// The cases were once also held byte for byte against the principals they
// replaced: the `<case>-as-identities` cases became the cases of the same
// name, and their goldens did not change by one byte.

func TestIdentitiesReachInTheServicesConfiguration(t *testing.T) {
	cfg := authzConfigOf(t, "golden/observability-proxy/tenancy-authz-identities.yaml")
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

// Every shape a principal could take is held by some case as an identity:
// the workload knobs, whole-cluster reach on some clusters only, whole-store
// reach, a name left out (the single-group entry) and several groups.
func TestEveryIdentityShapeHasACase(t *testing.T) {
	cases, err := filepath.Glob("cases/observability-proxy/*/values.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, cases)

	covered := map[string]bool{}
	for _, values := range cases {
		b, err := os.ReadFile(values)
		require.NoError(t, err)
		var doc map[string]any
		require.NoError(t, yaml.Unmarshal(b, &doc))
		tenancy, _ := doc["tenancy"].(map[string]any)
		assert.NotContains(t, tenancy, "principals", "%s: tenancy.principals was removed", values)
		list, _ := tenancy["identities"].([]any)
		for _, e := range list {
			id, _ := e.(map[string]any)
			if _, ok := id["name"]; !ok {
				covered["no name (single group)"] = true
			}
			if g, _ := id["groups"].([]any); len(g) > 1 {
				covered["several groups"] = true
			}
			if _, ok := id["audience"]; ok {
				covered["audience"] = true
			}
			w, _ := id["workload"].(map[string]any)
			for k := range w {
				covered["workload."+k] = true
			}
			reach, _ := id["reach"].(map[string]any)
			if reach["wholeStore"] == true {
				covered["reach.wholeStore"] = true
			}
			clusters, _ := reach["clusters"].([]any)
			for _, c := range clusters {
				cm, _ := c.(map[string]any)
				if cm["allNamespaces"] == true {
					covered["reach.clusters[].allNamespaces"] = true
				}
				if _, ok := cm["namespaces"]; ok {
					covered["reach.clusters[].namespaces"] = true
				}
			}
		}
	}

	want := []string{"no name (single group)", "several groups", "audience", "workload.routes",
		"workload.metricsQueryOnly", "workload.vmalertAPI", "reach.wholeStore",
		"reach.clusters[].allNamespaces", "reach.clusters[].namespaces"}
	var missing []string
	for _, k := range want {
		if !covered[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "identity shapes no proxy case holds; add a case")
}
