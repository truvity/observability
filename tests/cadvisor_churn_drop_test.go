// This file proves `metrics.scrape.cadvisorDrop` — the default churn
// reduction for the cadvisor scrape — against a REAL relabel
// implementation, the same doctrine tests/kubestatemetrics_relabel_test.go
// already applies to the kube-state-metrics namespace stamp: reasoning
// about the rendered YAML is not the same as running it.
//
// The three never-queried metric names, the bucket-histogram drop (with
// its one measured exception, go_sched_latencies_seconds_bucket) and the
// conditional `id` clear are all read straight off the cadvisor job's
// `metric_relabel_configs` in the rendered `minimal` golden — not a copy
// written into the test — so a values.yaml change that never makes it
// into the golden cannot make this test pass on a stale belief about
// what ships. The "extended" and "off" cases are each read off their own
// golden the same way, proving the override shape and the switch, not
// merely the default.
//
// TestCadvisorChurnDropIDScopedToContainerLevel is the regression proof
// for a review finding against this chart's own first draft: an
// unconditional `labeldrop id` merges cadvisor's node-level cgroups (the
// root, the pod-manager slice, a systemd unit — none of them a pod or a
// container, so `id` is their ONLY distinguishing label) into one
// identical label set per node, which vmagent/vmsingle deduplication
// then silently collapses to an arbitrary sample. The fix clears `id`
// only where `container` is already non-empty; that test replays all
// five representative shapes through the SAME relabel chain and asserts
// every one stays a distinct series afterward, not merely that `id` was
// handled correctly on each in isolation.
package tests

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// cadvisorMetricRelabelConfigs reads the cadvisor job's own
// metric_relabel_configs out of the named golden's VMAgent
// inlineScrapeConfig — already native Prometheus scrape-config YAML
// (snake_case throughout), unlike the kube-state-metrics ServiceMonitor's
// camelCase CRD fields, so no field rename is needed before decoding.
func cadvisorMetricRelabelConfigs(t *testing.T, goldenPath string) []*relabel.Config {
	t.Helper()
	docs := renderedDocs(t, goldenPath)
	agent := findDoc(t, docs, "VMAgent", nil)
	spec, ok := agent["spec"].(map[string]any)
	require.True(t, ok, "VMAgent has no spec")
	inline, ok := spec["inlineScrapeConfig"].(string)
	require.True(t, ok && inline != "", "VMAgent has no inlineScrapeConfig")

	var jobs []map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(inline), &jobs), "decoding inlineScrapeConfig")

	var cadvisor map[string]any
	for _, j := range jobs {
		if j["job_name"] == "cadvisor" {
			cadvisor = j
			break
		}
	}
	require.NotNil(t, cadvisor, "no cadvisor job in inlineScrapeConfig")

	raw, ok := cadvisor["metric_relabel_configs"].([]any)
	require.True(t, ok, "the cadvisor job has no metric_relabel_configs")
	require.NotEmpty(t, raw)

	cfgs := make([]*relabel.Config, 0, len(raw))
	for i, item := range raw {
		b, err := yaml.Marshal(item)
		require.NoErrorf(t, err, "re-marshalling metric_relabel_configs[%d]", i)
		var cfg relabel.Config
		require.NoErrorf(t, yaml.Unmarshal(b, &cfg), "decoding metric_relabel_configs[%d]: %s", i, string(b))
		// See kubestatemetrics_relabel_test.go's own comment on this line:
		// relabel.relabel() panics on an unset validation scheme rather
		// than defaulting one.
		cfg.NameValidationScheme = model.LegacyValidation
		cfgs = append(cfgs, &cfg)
	}
	return cfgs
}

// applyCadvisorRelabel runs the real relabel chain and reports whether
// the series survived, alongside its labels afterwards — dropping a
// series is exactly the outcome half of these cases are checking for, so
// unlike applyRelabel in kubestatemetrics_relabel_test.go this does not
// require survival.
func applyCadvisorRelabel(cfgs []*relabel.Config, in map[string]string) (map[string]string, bool) {
	lb := labels.NewBuilder(labels.FromMap(in))
	keep := relabel.ProcessBuilder(lb, cfgs...)
	return lb.Labels().Map(), keep
}

// TestCadvisorChurnDropDefault is the default's own proof: the three
// named metrics and the id-labeldrop rule
// charts/observability-emitters/values.yaml documents apply to the
// "minimal" golden's cadvisor job. All three series and the bucket
// carry an `id` label the way cadvisor stamps every one of its
// container-scoped series with the cgroup path — proving the drop and
// the labeldrop both actually run, not merely that a rule NAMING them
// is present in the rendered text.
func TestCadvisorChurnDropDefault(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")

	base := func(name string) map[string]string {
		return map[string]string{
			"__name__":  name,
			"namespace": "team-a",
			"pod":       "web-0",
			"container": "app",
			"id":        "/kubepods/burstable/pod1234/5678",
			"image":     "example.com/app:v1",
			"uid":       "1234-5678",
		}
	}

	for _, dropped := range []string{
		"container_tasks_state",
		"container_memory_failures_total",
		"container_blkio_device_usage_total",
		// Never named directly: any other `_bucket` series is dropped
		// by the blanket rule, not the exact-name list.
		"container_fs_io_time_seconds_bucket",
	} {
		t.Run("dropped/"+dropped, func(t *testing.T) {
			_, keep := applyCadvisorRelabel(cfgs, base(dropped))
			assert.Falsef(t, keep, "%s: measured as never queried on a real install, and should be dropped by the default", dropped)
		})
	}

	for _, kept := range []string{
		// A series nothing here names to drop.
		"container_cpu_usage_seconds_total",
		// The one `_bucket` series the evidence found heavily queried —
		// the exception the blanket bucket drop must not catch.
		"go_sched_latencies_seconds_bucket",
	} {
		t.Run("kept/"+kept, func(t *testing.T) {
			got, keep := applyCadvisorRelabel(cfgs, base(kept))
			require.Truef(t, keep, "%s: should survive the default drop", kept)
			_, hasID := got["id"]
			assert.Falsef(t, hasID, "%s: the cgroup-path `id` label should be cleared on a container-level series (container is set) — no shipped dashboard or rule "+
				"filters on it", kept)
			for _, must := range []string{"namespace", "pod", "container", "uid"} {
				_, ok := got[must]
				assert.Truef(t, ok, "%s: label %q must survive — dashboards and rules use it, and this default never touches it", kept, must)
			}
		})
	}
}

// labelSetKey builds a canonical, sorted "k=v,k=v,..." string from a
// label map, so two maps built independently compare equal regardless
// of Go map iteration order — the dedup key
// TestCadvisorChurnDropIDScopedToContainerLevel uses to prove no two
// surviving series collide.
func labelSetKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// TestCadvisorChurnDropIDScopedToContainerLevel is the fix's own proof:
// `id` is cleared only where `container` already identifies the
// series, never unconditionally. Five representative label sets, all
// sharing the same scrape target (job/instance — one node, one scrape
// response), the way cadvisor actually presents them: three node-level
// cgroups that are neither a pod nor a container (the cgroup root, the
// pod-manager slice, a systemd unit), a pod-level rollup (`pod` set,
// `container` empty), and an ordinary container-level series. Only the
// last should lose `id` — and every one of the five must still be a
// DISTINCT series after the relabel chain runs on all of them, which is
// exactly what an unconditional `labeldrop id` got wrong: the three
// node-level cgroups share job/instance and, with `id` gone, an
// otherwise identical empty namespace/pod/container/uid/image — so
// vmagent/vmsingle deduplication would silently keep only one of them.
func TestCadvisorChurnDropIDScopedToContainerLevel(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")

	const metricName = "container_cpu_usage_seconds_total"
	withTarget := func(extra map[string]string) map[string]string {
		out := map[string]string{
			"__name__": metricName,
			"job":      "cadvisor",
			"instance": "node-1:10250",
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name       string
		in         map[string]string
		wantIDKept bool
	}{
		{
			// The cgroup root. No pod, no container, no namespace at
			// all — `id` is the only thing distinguishing this from
			// every other node-level cgroup below.
			name:       "node root cgroup (id=/)",
			in:         withTarget(map[string]string{"id": "/"}),
			wantIDKept: true,
		},
		{
			// The pod-manager slice — still no pod or container of its
			// own; it is the PARENT of every pod's cgroup, not one.
			name:       "pod-manager slice (id=/kubepods.slice)",
			in:         withTarget(map[string]string{"id": "/kubepods.slice"}),
			wantIDKept: true,
		},
		{
			// A systemd unit outside Kubernetes entirely.
			name:       "systemd unit (id=/system.slice/containerd.service)",
			in:         withTarget(map[string]string{"id": "/system.slice/containerd.service"}),
			wantIDKept: true,
		},
		{
			// A pod-level rollup: pod (and its namespace/uid) is known,
			// but `container` is empty — cadvisor's own per-pod network
			// counters are shaped exactly like this.
			name: "pod-level rollup (container empty)",
			in: withTarget(map[string]string{
				"id":        "/kubepods/burstable/podabc123",
				"namespace": "team-a",
				"pod":       "web-0",
				"uid":       "abc-123",
			}),
			wantIDKept: true,
		},
		{
			// An ordinary container-level series — the shape this
			// default is actually built to shrink.
			name: "container-level series",
			in: withTarget(map[string]string{
				"id":        "/kubepods/burstable/podabc123/def456",
				"namespace": "team-a",
				"pod":       "web-0",
				"container": "app",
				"uid":       "abc-123",
				"image":     "example.com/app:v1",
			}),
			wantIDKept: false,
		},
	}

	results := make([]map[string]string, len(cases))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := applyCadvisorRelabel(cfgs, tc.in)
			require.Truef(t, keep, "%s: this default must never drop the SERIES, only the id label", tc.name)
			_, hasID := got["id"]
			if tc.wantIDKept {
				assert.Truef(t, hasID, "%s: id should SURVIVE — with no container label, nothing else identifies this series, and clearing id here is exactly the "+
					"collision this fix exists to prevent", tc.name)
			} else {
				assert.Falsef(t, hasID, "%s: id should be CLEARED — container (with namespace and pod) already identifies this series", tc.name)
			}
			results[i] = got
		})
	}

	seen := make(map[string]string, len(results))
	for i, got := range results {
		key := labelSetKey(got)
		if prior, dup := seen[key]; dup {
			t.Fatalf("case %q collides with case %q — both produce the identical label set %q; vmagent/vmsingle deduplication would silently keep only one of these "+
				"two series", cases[i].name, prior, key)
		}
		seen[key] = cases[i].name
	}
}

// TestCadvisorChurnDropExtended proves the two `extra*` fields ADD to
// the shipped default rather than replacing it — the golden this reads,
// tests/cases/observability-emitters/cadvisor-churn-drop-extended, sets
// only `extraMetricNames`/`extraKeepBucketMetrics`.
func TestCadvisorChurnDropExtended(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/cadvisor-churn-drop-extended.yaml")

	base := func(name string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "team-a"}
	}

	for _, dropped := range []string{
		// The shipped default — still here, so `extra*` added rather
		// than replaced it.
		"container_tasks_state",
		// The consumer's own addition.
		"container_spec_memory_reservation_limit_bytes",
	} {
		_, keep := applyCadvisorRelabel(cfgs, base(dropped))
		assert.Falsef(t, keep, "%s: should be dropped", dropped)
	}

	for _, kept := range []string{
		// The shipped exception — still kept.
		"go_sched_latencies_seconds_bucket",
		// The consumer's own addition to the keep list — a `_bucket`
		// name that the blanket rule would otherwise catch.
		"container_fs_io_time_weighted_seconds_total_bucket",
	} {
		_, keep := applyCadvisorRelabel(cfgs, base(kept))
		assert.Truef(t, keep, "%s: should survive", kept)
	}
}

// TestCadvisorChurnDropReplaced proves `metricNames`/`keepBucketMetrics`
// are REPLACED wholesale when a consumer sets them directly — ordinary
// Helm list semantics — as opposed to Extended's additive fields above.
func TestCadvisorChurnDropReplaced(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/cadvisor-churn-drop-replaced.yaml")

	base := func(name string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "team-a"}
	}

	// None of the shipped defaults survive the replacement.
	for _, name := range []string{"container_tasks_state", "container_memory_failures_total", "container_blkio_device_usage_total"} {
		_, keep := applyCadvisorRelabel(cfgs, base(name))
		assert.Truef(t, keep, "%s: the default list was replaced, so this name is no longer dropped", name)
	}
	// The consumer's own replacement name is.
	_, keep := applyCadvisorRelabel(cfgs, base("container_spec_memory_reservation_limit_bytes"))
	assert.False(t, keep, "the consumer's own replacement metricNames entry should be dropped")

	// The shipped bucket exception no longer applies…
	_, keep = applyCadvisorRelabel(cfgs, base("go_sched_latencies_seconds_bucket"))
	assert.False(t, keep, "go_sched_latencies_seconds_bucket: the keep list was replaced, so the blanket bucket drop now catches it too")
	// …only the consumer's own replacement exception does.
	_, keep = applyCadvisorRelabel(cfgs, base("my_own_component_latency_seconds_bucket"))
	assert.True(t, keep, "my_own_component_latency_seconds_bucket: the consumer's own replacement keepBucketMetrics entry should survive")
}

// TestCadvisorChurnDropOff is the switch's own proof: with
// `cadvisorDrop.enabled: false`, every series this default would
// otherwise drop survives, unchanged, including its `id` label.
func TestCadvisorChurnDropOff(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/cadvisor-churn-drop-off.yaml")

	in := map[string]string{
		"__name__":  "container_tasks_state",
		"namespace": "team-a",
		"id":        "/kubepods/burstable/pod1234/5678",
	}
	got, keep := applyCadvisorRelabel(cfgs, in)
	require.True(t, keep, "cadvisorDrop.enabled: false should store every series exactly as cadvisor emits it")
	assert.Equal(t, "/kubepods/burstable/pod1234/5678", got["id"], "the id label should survive too — the switch turns off the whole default, not only the name "+
		"drop")
}
