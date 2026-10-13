package tests

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
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
// The workload knobs (`audience`, `workload.{routes,metricsQueryOnly,vmalertAPI}`)
// and whole-cluster reach on some clusters only (`reach.clusters[].allNamespaces`)
// are proved by TestEveryPrincipalShapeRendersTheSameAsAnIdentity.
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

// Every principal the old key can express is expressible as an identity, and
// renders the same. The cases `<case>-as-identities` are the source case with
// each of its principals rewritten (docs/tenancy-authz.md has the table): the
// whole render, VMUsers and tenancy-authz ConfigMap included, must equal the
// source's byte for byte.
func TestEveryPrincipalShapeRendersTheSameAsAnIdentity(t *testing.T) {
	rewritten, err := filepath.Glob("cases/*/*-as-identities/values.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, rewritten, "the rewritten cases moved; this proof would pass on none")

	covered := map[string]bool{}
	for _, values := range rewritten {
		dir := filepath.Dir(values)
		chart := filepath.Base(filepath.Dir(dir))
		name := strings.TrimSuffix(filepath.Base(dir), "-as-identities")
		t.Run(chart+"/"+name, func(t *testing.T) {
			tenancy := tenancyOf(t, values)
			assert.Empty(t, tenancy["principals"], "a rewritten case holds no principal")
			assert.NotEmpty(t, tenancy["identities"])

			src := tenancyOf(t, filepath.Join("cases", chart, name, "values.yaml"))
			for _, p := range principalsOf(src) {
				covered[principalShape(p)] = true
			}

			a, err := os.ReadFile(filepath.Join("golden", chart, name+".yaml"))
			require.NoError(t, err, "regenerate the golden renders with `just golden`")
			b, err := os.ReadFile(filepath.Join("golden", chart, name+"-as-identities.yaml"))
			require.NoError(t, err, "regenerate the golden renders with `just golden`")
			require.NotEmpty(t, a)
			assert.Equal(t, string(a), string(b), "identities and the principals they replace render differently")
		})
	}

	// The pair of cases above (`tenancy-authz-identities-principals` and
	// `tenancy-authz-identities`) is proved by the test before this one.
	for _, p := range principalsOf(tenancyOf(t, "cases/observability-stack/tenancy-authz-identities-principals/values.yaml")) {
		covered[principalShape(p)] = true
	}

	// Not vacuous, and not stale: every principal shape any case holds has a
	// rewritten case, so a new field on a principal that no identity covers
	// fails here.
	all, err := filepath.Glob("cases/*/*/values.yaml")
	require.NoError(t, err)
	var missing []string
	for _, values := range all {
		for _, p := range principalsOf(tenancyOf(t, values)) {
			if shape := principalShape(p); !covered[shape] {
				missing = append(missing, shape+" (in "+values+")")
			}
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "principal shapes with no `-as-identities` case; add one")
}

func tenancyOf(t *testing.T, valuesPath string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(valuesPath)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(b, &doc))
	tenancy, _ := doc["tenancy"].(map[string]any)
	return tenancy
}

func principalsOf(tenancy map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := tenancy["principals"].([]any)
	for _, p := range list {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// principalShape is the set of fields a principal sets and the set of fields
// each of its grants sets, not their values.
func principalShape(p map[string]any) string {
	var fields, grants []string
	for k := range p {
		if k != "grants" {
			fields = append(fields, k)
		}
	}
	seen := map[string]bool{}
	list, _ := p["grants"].([]any)
	for _, g := range list {
		var gf []string
		for k := range g.(map[string]any) {
			gf = append(gf, k)
		}
		sort.Strings(gf)
		if key := strings.Join(gf, ","); !seen[key] {
			seen[key] = true
			grants = append(grants, key)
		}
	}
	sort.Strings(fields)
	sort.Strings(grants)
	return strings.Join(fields, ",") + " | " + strings.Join(grants, "; ")
}
