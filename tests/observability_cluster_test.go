// charts/observability-cluster, first slice (restructure R2.1;
// docs/architecture/observability-cluster.md).
//
// The chart is installed on every cluster and is off by default, so what
// has to hold is small and has to hold always: an empty values file renders
// nothing (installing the chart changes no cluster), the operator renders
// only when asked, no CustomResourceDefinition comes out of a render (the
// CRDs are another release's, docs/adoption.md), and the Grafana resource
// carries a reference to its credential and never the credential.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clusterChartObjects(t *testing.T, valuesCase string) map[string]string {
	t.Helper()
	args := []string{}
	if valuesCase != "" {
		args = append(args, "-f", filepath.Join("cases", "observability-cluster", valuesCase, "values.yaml"))
	}
	out, stderr, err := helmTemplate(t, "observability-cluster", "observability-cluster", "default", args...)
	require.NoError(t, err, stderr)
	return objectsByKey(t, out)
}

func kindsOf(objs map[string]string) map[string]int {
	kinds := map[string]int{}
	for k := range objs {
		kinds[strings.SplitN(k, "/", 2)[0]]++
	}
	return kinds
}

func TestClusterChartDefaultsRenderNothing(t *testing.T) {
	assert.Empty(t, clusterChartObjects(t, ""),
		"installing observability-cluster with no values must change nothing on the cluster")
}

func TestClusterChartRendersWhatItWasAskedFor(t *testing.T) {
	grafana := clusterChartObjects(t, "minimal")
	assert.Equal(t, map[string]int{"Grafana": 1, "GrafanaFolder": 1}, kindsOf(grafana),
		"the external Grafana and the folder, and no operator")

	operator := clusterChartObjects(t, "operator")
	assert.NotContains(t, kindsOf(operator), "Grafana", "the operator alone names no Grafana")
	assert.Equal(t, 1, kindsOf(operator)["Deployment"])

	bare := clusterChartObjects(t, "folder-less")
	assert.Equal(t, map[string]int{"Grafana": 1}, kindsOf(bare))

	cases := map[string]map[string]string{
		"minimal": grafana, "operator": operator, "folder-less": bare,
		"everything": clusterChartObjects(t, "everything"),
	}
	for name, objs := range cases {
		assert.NotContains(t, kindsOf(objs), "CustomResourceDefinition",
			"%s: a CRD in the render would make this release a second owner of the Grafana CRDs", name)
	}
}

func TestClusterChartGrafanaHoldsAReferenceNotACredential(t *testing.T) {
	objs := clusterChartObjects(t, "everything")
	var g, f string
	for k, v := range objs {
		switch {
		case strings.HasPrefix(k, "Grafana/"):
			g = v
		case strings.HasPrefix(k, "GrafanaFolder/"):
			f = v
		}
	}
	require.NotEmpty(t, g)
	require.NotEmpty(t, f)

	assert.Contains(t, g, "apiKey:\n      name: \"grafana-credentials\"\n      key: \"api-token\"")
	for _, forbidden := range []string{"adminUser", "adminPassword", "insecureSkipVerify"} {
		assert.NotContains(t, g, forbidden)
	}
	// The label a component's GrafanaDashboard selects on is on the Grafana
	// resource, and the folder selects the same instance.
	assert.Contains(t, g, "observability.truvity.io/grafana: shared")
	assert.Contains(t, f, "observability.truvity.io/grafana: shared")
	assert.Contains(t, f, `uid: "example_cluster"`)
	// A Kubernetes name cannot hold an underscore; the folder uid can.
	assert.Contains(t, f, "name: example-cluster")
	assert.Contains(t, g, "namespace: \"cert-store\"")
}
