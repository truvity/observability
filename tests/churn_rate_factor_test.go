// `TooHighChurnRate24h` (upstream's vmsingle group) compares 24h of new
// series to three times the hourly active series. A store that holds
// short-lived CI workloads needs a higher factor. The interface is the sync
// job's per-rule `spec.expr`, as for RecordingRulesNoData; this pins that an
// override reaches the sync job's config with only the factor changed, and
// that without one no goldens carry an entry for it (the default render is
// untouched, so upstream's own factor of 3 applies).
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func churnRuleExprs(t *testing.T, golden string) []string {
	t.Helper()
	var out []string
	for _, doc := range splitDocs(t, golden) {
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
				Rules map[string]struct {
					Spec struct {
						Expr string `yaml:"expr"`
					} `yaml:"spec"`
				} `yaml:"rules"`
			} `yaml:"rules"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg), golden)
		if r, ok := cfg.Rules.Rules["TooHighChurnRate24h"]; ok {
			out = append(out, r.Spec.Expr)
		}
	}
	return out
}

func TestChurnRate24hFactorOverride(t *testing.T) {
	exprs := churnRuleExprs(t, "golden/observability-stack/churn-rate-factor.yaml")
	require.Len(t, exprs, 1)
	assert.Equal(t, `sum(increase(vm_new_timeseries_created_total[24h])) by(instance) > (sum(vm_cache_entries{type="storage/hour_metric_ids"}) by(instance) * 6)`, exprs[0])
}

func TestChurnRate24hUntouchedByDefault(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	for _, g := range goldens {
		if strings.HasSuffix(g, "/churn-rate-factor.yaml") {
			continue
		}
		assert.Emptyf(t, churnRuleExprs(t, g), "%s: TooHighChurnRate24h must be upstream's unless overridden", g)
	}
}
