package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// Kargo's Warehouse watches the observability-cluster chart. The shared
// publish-charts action pushes the `charts` list in order, so when
// observability-cluster is last, Freight appears only after every chart of the
// release is published. Moving it up would let a promotion start against a
// release that is still half-pushed.
func TestReleaseWorkflowPushesObservabilityClusterLast(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yaml"))
	require.NoError(t, err)

	var wf struct {
		Jobs map[string]struct {
			With map[string]any `yaml:"with"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf))

	var found bool
	for name, job := range wf.Jobs {
		v, ok := job.With["charts"]
		if !ok {
			continue
		}
		found = true
		s, ok := v.(string)
		require.Truef(t, ok, "job %q: `charts` must be a JSON list string", name)
		var charts []string
		require.NoErrorf(t, json.Unmarshal([]byte(s), &charts), "job %q: `charts` is not a JSON list", name)
		require.NotEmptyf(t, charts, "job %q: `charts` is empty", name)
		require.Equalf(t, "observability-cluster", charts[len(charts)-1],
			"job %q: the last chart pushed is %q, but it must be observability-cluster: Kargo's Warehouse "+
				"watches that chart and publish-charts pushes in list order, so the Freight appears only "+
				"once every chart is published. Put observability-cluster last in `charts:` of "+
				".github/workflows/release.yaml.", name, charts[len(charts)-1])
	}
	require.True(t, found, "no job in release.yaml passes `charts:`; this test would pass vacuously")
}
