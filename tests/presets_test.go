// Chart presets: values files shipped inside a chart (charts/<chart>/presets/
// <name>.yaml) that a consumer lists BEFORE its own values. A subchart's values
// cannot be computed from the parent's, so a value that is the same on every
// estate of a given shape (a node pool's selector, a store's metric names) is
// shipped as a file instead of being written out again in every consumer.
//
// Two guarantees are held here. A preset renders exactly what the values it
// stands for render: each `presets` case (its `presets` file lists the presets,
// its values.yaml is what no preset can know) must equal its `-explicit` twin,
// the same values written out in full. And a preset is not a default: nothing
// in the chart reads it unless it is listed, so the cases that do not list one
// (every other golden, held by hack/default-change-guard.sh) do not move.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresetsRenderWhatTheirValuesRender(t *testing.T) {
	for _, tc := range []struct {
		chart, name string
		// markers: strings the preset render must carry, so the equality
		// cannot hold because both renders lost the preset.
		markers []string
	}{
		{"observability-stack", "presets", []string{
			"karpenter.sh/nodepool: durable", "vm_rows_ignored_total",
			"alertgroup", "KubeCPUOvercommit",
		}},
		{"observability-stack", "presets-operator-only", []string{"kubernetes.io/arch: arm64"}},
		{"observability-alerting", "presets", []string{
			"vm_rows_ignored_total", "alertgroup",
		}},
		{"observability-emitters", "presets", []string{
			"vmauth-observability-stack.observability.svc:8427", "--remoteWrite.bearerTokenFile=/etc/observability-write-token/token",
			"karpenter.sh/nodepool: durable",
		}},
		{"observability-emitters", "presets-remote", []string{"/etc/observability-write-ca"}},
		{"observability-grafana", "presets", []string{"kubernetes.io/arch: arm64"}},
		{"observability-rum", "presets", []string{"kubernetes.io/arch: arm64"}},
		{"platform-alerts", "presets", []string{"vm_rows_inserted_total", "vl_rows_ingested_total"}},
	} {
		t.Run(tc.chart+"/"+tc.name, func(t *testing.T) {
			preset := readGolden(t, tc.chart, tc.name+"-preset")
			explicit := readGolden(t, tc.chart, tc.name+"-explicit")
			assert.Equal(t, explicit, preset,
				"the presets render something other than the values they stand for")
			minimal := readGolden(t, tc.chart, "minimal")
			assert.NotEqual(t, minimal, preset, "the preset case renders the same as the minimal one")
			for _, m := range tc.markers {
				assert.Contains(t, preset, m)
			}
		})
	}
}

func TestEveryPresetIsListedByACase(t *testing.T) {
	listed := map[string]bool{}
	cases, err := filepath.Glob("cases/*/*/presets")
	require.NoError(t, err)
	for _, f := range cases {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		chart := filepath.Base(filepath.Dir(filepath.Dir(f)))
		for _, name := range strings.Fields(string(b)) {
			listed[chart+"/"+name] = true
		}
	}
	files, err := filepath.Glob("../charts/*/presets/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		chart := filepath.Base(filepath.Dir(filepath.Dir(f)))
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		assert.True(t, listed[chart+"/"+name], "preset %s/%s is listed by no case, so nothing renders it", chart, name)
	}
}

func readGolden(t *testing.T, chart, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("golden", chart, name+".yaml"))
	require.NoError(t, err)
	return string(b)
}
