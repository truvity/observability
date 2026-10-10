// Per-cluster datasources (restructure R2.2, first slice;
// docs/architecture/cluster-datasources.md).
//
// Two halves that have to agree. The stack renders one bearer-token VMUser
// per cluster with the cluster FORCED on its metrics and logs routes; the
// observability-cluster chart renders the GrafanaDatasources that read
// through them. Both are opt-in: the byte-identical default render is held by
// the goldens, and TestClusterDatasourcesOffByDefault by name.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clusterDatasourcesGolden = "golden/observability-stack/tenancy-cluster-datasources.yaml"

func TestClusterDatasourceUserForcesItsClusterAndNothingElse(t *testing.T) {
	var ds []vmUser
	for _, u := range readVMUsers(t, clusterDatasourcesGolden) {
		if strings.HasPrefix(u.Spec.Name, "datasource-") {
			ds = append(ds, u)
		}
	}
	require.Len(t, ds, 2, "one datasource user per cluster listed")

	want := map[string][2]string{
		"datasource-example-a": {`{k8s_cluster_name="example-a"}`, `_stream:{"k8s.cluster.name"="example-a"}`},
		"datasource-example-b": {`{k8s_cluster_name="example-b"}`, `_stream:{"k8s.cluster.name"="example-b"}`},
	}
	for _, u := range ds {
		filters, ok := want[u.Spec.Name]
		require.True(t, ok, "unexpected datasource user %q", u.Spec.Name)

		assert.Empty(t, u.Spec.JWT.MatchClaims, "%s: a bearer token, not an OIDC identity", u.Spec.Name)
		require.Len(t, u.Spec.TargetRefs, 2, "%s: the metrics store and the log store; no traces, no write, no vmalert route", u.Spec.Name)

		metrics, logs := u.Spec.TargetRefs[0], u.Spec.TargetRefs[1]
		require.Len(t, metrics.QueryArgs, 1)
		assert.Equal(t, "extra_filters", metrics.QueryArgs[0].Name)
		assert.Equal(t, []string{filters[0]}, metrics.QueryArgs[0].Values,
			"%s: exactly one cluster, literal, never a claim placeholder", u.Spec.Name)
		require.Len(t, logs.QueryArgs, 1)
		assert.Equal(t, "extra_stream_filters", logs.QueryArgs[0].Name)
		assert.Equal(t, []string{filters[1]}, logs.QueryArgs[0].Values)

		// The dashboard read set: variables need series, labels and values.
		assert.Contains(t, metrics.Paths, "/prometheus/api/v1/series")
		assert.Contains(t, metrics.Paths, "/prometheus/api/v1/labels")
		for _, p := range append(append([]string{}, metrics.Paths...), logs.Paths...) {
			for _, bad := range []string{"write", "import", "delete", "admin", "jaeger", "tempo", "insert"} {
				assert.NotContains(t, p, bad, "%s: %s", u.Spec.Name, p)
			}
		}
	}
}

func TestClusterDatasourcesOffByDefault(t *testing.T) {
	// Every other golden is a default-shaped render of the stack: none may
	// carry the role, and `hack/golden.sh` (CI) proves none moved.
	files, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "tenancy-cluster-datasources.yaml") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "cluster-datasource", "%s: no datasource user unless tenancy.clusterDatasources is set", f)
	}
}

func TestClusterChartRendersTwoDatasourcesPerCluster(t *testing.T) {
	objs := clusterChartObjects(t, "datasources")
	assert.Equal(t, map[string]int{"Grafana": 1, "GrafanaDatasource": 4}, kindsOf(objs),
		"a metrics and a logs datasource for each of two clusters; no folder (the platform pre-creates them)")

	for _, c := range []string{"example-a", "example-b"} {
		m := objs["GrafanaDatasource/default/"+c+"-metrics"]
		l := objs["GrafanaDatasource/default/"+c+"-logs"]
		require.NotEmpty(t, m)
		require.NotEmpty(t, l)
		// uid == name: resolved by name or by uid, the same object.
		assert.Contains(t, m, `uid: "`+c+`-metrics"`)
		assert.Contains(t, m, `name: "`+c+`-metrics"`)
		assert.Contains(t, m, `url: "https://telemetry.example.org/prometheus"`, "a trailing slash on proxyUrl is trimmed")
		assert.Contains(t, l, `uid: "`+c+`-logs"`)
		assert.Contains(t, l, `url: "https://telemetry.example.org"`)
		for _, d := range []string{m, l} {
			// A reference to the token, never the token, and no filter of the
			// datasource's own: the proxy forces the cluster.
			assert.Contains(t, d, `Bearer ${token}`)
			assert.Contains(t, d, `name: "`+c+`-datasource-token"`)
			assert.NotContains(t, d, "k8s_cluster_name")
			assert.NotContains(t, d, "extra_filters")
			assert.Contains(t, d, "observability.truvity.io/grafana: shared")
		}
	}
}
