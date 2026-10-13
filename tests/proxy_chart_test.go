package tests

// The Door plane lives in charts/observability-proxy and nowhere else
// (docs/adoption.md, "Upgrading to the release that removes the Door plane
// from observability-stack").
//
// Until that release the stack rendered the Door itself (`door.source: stack`,
// the default) and the proxy chart could take it over (0.81.0). The release
// removed the stack's copies and the switch, so the statement these tests
// hold is the end state of that hand-over:
//
//   - the stack renders no Door object, in any of its cases;
//   - the proxy chart renders all of them, and every object the proxy renders
//     is of a kind the Door is made of;
//   - the keys the two charts both declare mean the same in both, so a
//     values section moved from one to the other is a copy and paste, and the
//     keys that moved (`vmauth`, `tenancy` apart from the cluster label,
//     `door`, `networkPolicy.proxyFrom`) are gone from the stack's schema.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doorKinds is what the Door plane consists of; an object of any other kind
// in the proxy chart's render is a surprise somebody should read.
var doorKinds = map[string]bool{
	"VMAuth": true, "VMUser": true, "ServiceAccount": true, "ConfigMap": true,
	"Service": true, "Deployment": true, "PodDisruptionBudget": true,
	"ServiceMonitor": true, "SecurityPolicy": true, "NetworkPolicy": true,
	"SSHKey": true, "ExternalSecret": true,
}

// stackDoorKinds are the kinds only the Door renders: the stack must have
// none of them. (The stack renders ServiceAccounts, Services and so on for
// its stores and alerters, so those are told apart by name below.)
var stackDoorKinds = map[string]bool{"VMAuth": true, "VMUser": true, "SecurityPolicy": true}

// The stack renders no Door object. Held over every golden the stack has, and
// over a real render of every case (presets merged), so a case added later
// is covered without anyone remembering to regenerate a golden first.
func TestTheStackRendersNoDoorObject(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(goldens), 40, "the stack goldens moved; this proof would pass on too few")
	for _, g := range goldens {
		b, err := os.ReadFile(g)
		require.NoError(t, err)
		for key := range objectsByKey(t, string(b)) {
			assertNotDoor(t, filepath.Base(g), key)
		}
	}

	cases, err := filepath.Glob("cases/observability-stack/*/values.yaml")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(cases), 40, "the stack cases moved; this proof would pass on too few")
	for _, valuesPath := range cases {
		caseDir := filepath.Dir(valuesPath)
		name := filepath.Base(caseDir)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			namespace := "default"
			if b, err := os.ReadFile(filepath.Join(caseDir, "namespace")); err == nil {
				namespace = strings.TrimSpace(string(b))
			}
			var files []string
			if b, err := os.ReadFile(filepath.Join(caseDir, "presets")); err == nil {
				for _, p := range strings.Fields(string(b)) {
					files = append(files, "-f", filepath.Join("..", "charts", "observability-stack", "presets", p+".yaml"))
				}
			}
			files = append(files, "-f", valuesPath)
			out, errOut, err := helmTemplate(t, stackRelease, "observability-stack", namespace, files...)
			require.NoError(t, err, errOut)
			for key := range objectsByKey(t, out) {
				assertNotDoor(t, name, key)
			}
		})
	}
}

// assertNotDoor fails on a Door object. Keys are Kind/namespace/name.
func assertNotDoor(t *testing.T, where, key string) {
	t.Helper()
	parts := strings.SplitN(key, "/", 3)
	kind, name := parts[0], parts[2]
	assert.Falsef(t, stackDoorKinds[kind], "%s: the stack renders %s: the Door plane is observability-proxy's", where, key)
	assert.Falsef(t, kind == "NetworkPolicy" && strings.HasSuffix(name, "-proxy"), "%s: the stack renders the proxy's NetworkPolicy %s", where, key)
	assert.Falsef(t, strings.HasSuffix(name, "-tenancy-authz") || strings.HasSuffix(name, "-tenancy-authz-signing"),
		"%s: the stack renders tenancy-authz's %s", where, key)
}

// The proxy chart renders every kind of object the Door is made of, and
// nothing else, across its cases. Not vacuous: every kind is seen at least
// once.
func TestTheProxyChartRendersTheDoor(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-proxy/*.yaml")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(goldens), 15, "the proxy goldens moved; this proof would pass on too few")
	seen := map[string]bool{}
	for _, g := range goldens {
		b, err := os.ReadFile(g)
		require.NoError(t, err, "regenerate the golden renders with `just golden`")
		keys := objectsByKey(t, string(b))
		require.NotEmpty(t, keys, g)
		for key := range keys {
			kind, _, _ := strings.Cut(key, "/")
			assert.Truef(t, doorKinds[kind], "%s: %s is a kind the Door plane is not expected to contain", g, key)
			seen[kind] = true
		}
	}
	var missing []string
	for kind := range doorKinds {
		if !seen[kind] {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "no proxy case renders these kinds; the proof does not cover them")
}

// A value the proxy chart declares must be one the stack declares, with the
// same schema, except the Door's own keys, which only the proxy declares now:
// "copy and paste" migration is the contract for moving a section between
// the two values files. The mirrors are reduced on purpose and are compared
// on the keys they keep.
func TestProxyChartValuesAreTheStacksValues(t *testing.T) {
	read := func(chart string) map[string]any {
		b, err := os.ReadFile(filepath.Join("..", "charts", chart, "values.schema.json"))
		require.NoError(t, err)
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(b, &m))
		return m["properties"].(map[string]any)
	}
	stack, proxy := read("observability-stack"), read("observability-proxy")
	// What only the proxy declares: its own key, and the Door's sections.
	own := map[string]bool{"stackReleaseName": true, "vmauth": true, "tenancy": true}
	reduced := map[string]bool{"networkPolicy": true, "resources": true, "vmalert": true, "alertmanager": true,
		"victoria-metrics-k8s-stack": true, "victoria-logs-single": true, "victoria-traces-single": true}
	for k, v := range proxy {
		if own[k] {
			continue
		}
		require.Contains(t, stack, k, "%s is declared by the proxy chart and not by the stack", k)
		if reduced[k] {
			continue
		}
		assert.Equal(t, stack[k], v, "%s has a different schema in the two charts: a copy-paste migration would change meaning", k)
	}

	// The Door's keys are gone from the stack. What stays of `tenancy` is the
	// cluster label (the self-alerts and the k8s-stack mirror read it) and of
	// `vmauth` the switch that says whether a proxy fronts the stores; both
	// are declared by the proxy chart too, with the same meaning.
	assert.NotContains(t, stack, "door", "the switch is gone: the stack never renders the Door")
	keysOf := func(section map[string]any) []string {
		var out []string
		for k := range section["properties"].(map[string]any) {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	stackTenancy, proxyTenancy := stack["tenancy"].(map[string]any), proxy["tenancy"].(map[string]any)
	assert.Equal(t, []string{"clusterLabel"}, keysOf(stackTenancy), "the stack keeps only the cluster label of `tenancy`")
	assert.Equal(t, stackTenancy["properties"].(map[string]any)["clusterLabel"],
		proxyTenancy["properties"].(map[string]any)["clusterLabel"], "tenancy.clusterLabel differs between the charts")
	assert.Equal(t, []string{"enabled"}, keysOf(stack["vmauth"].(map[string]any)), "the stack keeps only the proxy switch of `vmauth`")
	assert.NotContains(t, stack["networkPolicy"].(map[string]any)["properties"], "proxyFrom", "the proxy's policy is observability-proxy's")
}
