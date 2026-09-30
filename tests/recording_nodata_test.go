// `RecordingRulesNoData` is upstream's alert on a recording rule that
// produces no data. `count:up0` (`count without(...) (up == 0)`) is empty
// whenever every target is up, so on a healthy estate the alert fired for
// nothing. values.yaml overrides the alert's expression through the sync
// job's per-rule `spec`; this pins that the override reaches the sync job's
// rendered config on every golden that has one, that it still watches every
// recording except `count:up0`, and that it keeps upstream's threshold.
// hack/recording-nodata-proof.sh proves the behaviour against the real sync
// job and a real store.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestRecordingRulesNoDataIgnoresRecordingsEmptyWhenHealthy(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	checked := 0
	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var cm struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Data struct {
					ConfigYAML string `yaml:"config.yaml"`
				} `yaml:"data"`
			}
			if yaml.Unmarshal(doc, &cm) != nil || cm.Kind != "ConfigMap" || !strings.HasSuffix(cm.Metadata.Name, "-sync-job-config") {
				continue
			}
			var cfg struct {
				Rules struct {
					Sources []any `yaml:"sources"`
					Rules   map[string]struct {
						Spec struct {
							Expr string `yaml:"expr"`
						} `yaml:"spec"`
					} `yaml:"rules"`
				} `yaml:"rules"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg), g)
			if len(cfg.Rules.Sources) == 0 {
				continue // defaultRules is off: no upstream rule is fetched, nothing to override
			}
			expr := cfg.Rules.Rules["RecordingRulesNoData"].Spec.Expr
			assert.Containsf(t, expr, `recording!~"count:up0"`, "%s: RecordingRulesNoData must ignore count:up0", g)
			assert.Truef(t, strings.HasPrefix(expr, "sum(vmalert_recording_rules_last_evaluation_samples{"), "%s: %s", g, expr)
			assert.Truef(t, strings.HasSuffix(expr, "without(id) < 1"), "%s: threshold drifted from upstream: %s", g, expr)
			// Only count:up0 is excluded: a second entry would silence a
			// recording that is broken when empty.
			assert.Equalf(t, 1, strings.Count(expr, "recording!~"), "%s: %s", g, expr)
			checked++
		}
	}
	require.NotZero(t, checked, "no golden carries a sync-job config")
}
