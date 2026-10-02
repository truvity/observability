// `metrics.scrape.kubeletDrop` — the default drop on the kubelet scrape of
// `kubernetes_feature_enabled` — run through a REAL relabel implementation,
// the same doctrine as cadvisor_churn_drop_test.go: the rule is read off the
// kubelet job's `metric_relabel_configs` in the rendered goldens, never copied
// into the test, so a values change that does not reach the golden cannot
// pass on a stale belief about what ships.
package tests

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// scrapeJobMetricRelabelConfigs reads one inline scrape job's
// metric_relabel_configs out of the named golden's VMAgent.
func scrapeJobMetricRelabelConfigs(t *testing.T, goldenPath, job string) []*relabel.Config {
	t.Helper()
	agent := findDoc(t, renderedDocs(t, goldenPath), "VMAgent", nil)
	spec, ok := agent["spec"].(map[string]any)
	require.True(t, ok, "VMAgent has no spec")
	inline, ok := spec["inlineScrapeConfig"].(string)
	require.True(t, ok && inline != "", "VMAgent has no inlineScrapeConfig")
	var jobs []map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(inline), &jobs))
	for _, j := range jobs {
		if j["job_name"] != job {
			continue
		}
		raw, ok := j["metric_relabel_configs"].([]any)
		require.Truef(t, ok, "the %s job has no metric_relabel_configs", job)
		var cfgs []*relabel.Config
		for i, item := range raw {
			b, err := yaml.Marshal(item)
			require.NoErrorf(t, err, "re-marshalling metric_relabel_configs[%d]", i)
			var cfg relabel.Config
			require.NoErrorf(t, yaml.Unmarshal(b, &cfg), "decoding metric_relabel_configs[%d]: %s", i, string(b))
			cfg.NameValidationScheme = model.LegacyValidation
			cfgs = append(cfgs, &cfg)
		}
		return cfgs
	}
	t.Fatalf("no %s job in inlineScrapeConfig", job)
	return nil
}

func survives(cfgs []*relabel.Config, name string) bool {
	lb := labels.NewBuilder(labels.FromMap(map[string]string{"__name__": name, "node": "node-a", "name": "SomeGate", "stage": "BETA"}))
	return relabel.ProcessBuilder(lb, cfgs...)
}

func TestKubeletDropDefault(t *testing.T) {
	cfgs := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml", "kubelet")
	assert.False(t, survives(cfgs, "kubernetes_feature_enabled"), "the feature-gate gauge is dropped by default")
	for _, kept := range []string{"kubelet_running_pods", "kubelet_volume_stats_used_bytes", "kubernetes_build_info", "up"} {
		assert.Truef(t, survives(cfgs, kept), "%s is not named by the drop and must survive", kept)
	}
	// Kubelet only: the cadvisor job has its own drop and is untouched by this one.
	cad := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml", "cadvisor")
	assert.True(t, survives(cad, "kubernetes_feature_enabled"), "the kubelet drop must not reach the cadvisor job")
}

func TestKubeletDropOff(t *testing.T) {
	cfgs := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/kubelet-drop-off.yaml", "kubelet")
	assert.True(t, survives(cfgs, "kubernetes_feature_enabled"), "kubeletDrop.enabled: false stores every kubelet series as emitted")
}

func TestKubeletDropExtraNameIsAddedNotReplacing(t *testing.T) {
	cfgs := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/kubelet-drop-extra.yaml", "kubelet")
	assert.False(t, survives(cfgs, "kubernetes_feature_enabled"), "the default name stays dropped")
	assert.False(t, survives(cfgs, "kubelet_example_metric_total"), "the consumer's own name is dropped beside it")
	assert.True(t, survives(cfgs, "kubelet_running_pods"))
}
