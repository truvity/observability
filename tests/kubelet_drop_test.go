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

// The unused kubelet histogram buckets are dropped by default, with their
// _sum and _count kept, and the three buckets the kubelet dashboard takes a
// quantile of are kept.
func TestKubeletBucketDropDefault(t *testing.T) {
	cfgs := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml", "kubelet")
	for _, dropped := range []string{
		"rest_client_rate_limiter_duration_seconds_bucket",
		"rest_client_response_size_bytes_bucket",
		"rest_client_request_size_bytes_bucket",
		"volume_operation_total_seconds_bucket",
		"kubelet_http_requests_duration_seconds_bucket",
		"csi_operations_seconds_bucket",
		"workqueue_queue_duration_seconds_bucket",
		"workqueue_work_duration_seconds_bucket",
	} {
		assert.Falsef(t, survives(cfgs, dropped), "%s is read by nothing and is dropped by default", dropped)
	}
	for _, kept := range []string{
		"kubelet_runtime_operations_duration_seconds_bucket",
		"storage_operation_duration_seconds_bucket",
		"rest_client_request_duration_seconds_bucket",
		"rest_client_response_size_bytes_sum",
		"rest_client_response_size_bytes_count",
		"workqueue_depth",
	} {
		assert.Truef(t, survives(cfgs, kept), "%s is used or is not a dropped bucket and must survive", kept)
	}
}

// Emptying the new values restores the previous render byte for byte, which
// the golden diff proves; here the behaviour is checked too.
func TestChurnDropsEmptyRestoresOldRender(t *testing.T) {
	cfgs := scrapeJobMetricRelabelConfigs(t, "golden/observability-emitters/churn-drops-empty.yaml", "kubelet")
	assert.True(t, survives(cfgs, "csi_operations_seconds_bucket"))
	assert.False(t, survives(cfgs, "kubernetes_feature_enabled"), "the older default is untouched")

	cad := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/churn-drops-empty.yaml")
	_, keep := applyCadvisorRelabel(cad, map[string]string{"__name__": "container_fs_reads_total", "namespace": "ci-build-1", "container": "x", "id": "/a"})
	assert.True(t, keep, "ephemeralNamespaces: \"\" stores every cadvisor series in a CI namespace again")
}

func TestCadvisorEphemeralNamespaceAllowlist(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	series := func(name, ns string) map[string]string {
		return map[string]string{"__name__": name, "namespace": ns, "pod": "p", "container": "c", "id": "/kubepods/x"}
	}
	for _, ns := range []string{"arc-runners-org", "ci-build-1"} {
		for _, kept := range []string{
			"container_cpu_usage_seconds_total", "container_memory_working_set_bytes",
			"container_cpu_cfs_throttled_periods_total", "container_oom_events_total",
		} {
			got, keep := applyCadvisorRelabel(cfgs, series(kept, ns))
			require.Truef(t, keep, "%s in %s is on the allowlist", kept, ns)
			_, scratch := got["__cadvisor_keep_ephemeral__"]
			assert.False(t, scratch, "scratch label must not leak")
		}
		for _, dropped := range []string{"container_fs_reads_total", "container_spec_cpu_shares", "container_last_seen", "container_fs_usage_bytes"} {
			_, keep := applyCadvisorRelabel(cfgs, series(dropped, ns))
			assert.Falsef(t, keep, "%s in %s is off the allowlist", dropped, ns)
		}
	}
	// Other namespaces, including names that merely contain the pattern, are untouched.
	for _, ns := range []string{"team-a", "kube-system", "my-ci-tools", "arc-systems", "xci-1"} {
		_, keep := applyCadvisorRelabel(cfgs, series("container_fs_reads_total", ns))
		assert.Truef(t, keep, "%s is not an ephemeral namespace: nothing dropped", ns)
	}
	// Node-level series carry no namespace and are untouched.
	_, keep := applyCadvisorRelabel(cfgs, map[string]string{"__name__": "container_fs_reads_total", "id": "/"})
	assert.True(t, keep)
}
