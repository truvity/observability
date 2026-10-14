// `metrics.scrape.collapse` — the opt-in merge of the identity of short-lived
// pods and nodes by vmagent stream aggregation — and the default drops that
// ride with it (cAdvisor families, kube_replicaset_*, the log agent's `flag`).
//
// The stream aggregation rules are read off the rendered VMAgent in the
// `collapse` golden, never copied into the test. What is proved here:
//
//   - every series is matched by AT MOST ONE rule (a series matched by two
//     rules is aggregated twice and stored twice);
//   - the series that must be collapsed are matched, and exactly one rule
//     picks their output (counter -> total, timestamp -> max, gauge ->
//     sum_samples);
//   - the series that must not be touched match no rule (other namespaces,
//     other metric names, `up` and `scrape_*`, pod-level series on a pool
//     node, a node of another pool);
//   - applying `without` makes two pods of one namespace one label set and
//     never merges two tenants (the scoping labels are not in `without`).
//
// tests/ and hack/collapse-stream-aggregation-proof.sh agree: the script runs
// the same rules through a real vmagent.
package tests

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type aggrRule struct {
	match   *metricsql.MetricExpr
	raw     string
	outputs []string
	without []string
}

func streamAggrRules(t *testing.T, goldenPath string) (rules []aggrRule, found bool) {
	t.Helper()
	agent := findDoc(t, renderedDocs(t, goldenPath), "VMAgent", nil)
	spec, ok := agent["spec"].(map[string]any)
	require.True(t, ok, "VMAgent has no spec")
	sa, ok := spec["streamAggrConfig"].(map[string]any)
	if !ok {
		return nil, false
	}
	assert.Equal(t, false, sa["keepInput"], "keepInput is false: matched input is replaced by the output")
	assert.NotContains(t, sa, "dropInput", "dropInput would drop the series no rule matches too")
	for i, item := range sa["rules"].([]any) {
		m := item.(map[string]any)
		raw := m["match"].(string)
		expr, err := metricsql.Parse(raw)
		require.NoErrorf(t, err, "rules[%d].match %q", i, raw)
		me, ok := expr.(*metricsql.MetricExpr)
		require.Truef(t, ok, "rules[%d].match %q is not a series selector", i, raw)
		r := aggrRule{match: me, raw: raw}
		for _, o := range m["outputs"].([]any) {
			r.outputs = append(r.outputs, o.(string))
		}
		for _, w := range m["without"].([]any) {
			r.without = append(r.without, w.(string))
		}
		assert.Equal(t, true, m["keep_metric_names"], "rules[%d]: names are kept so dashboards read the same metric", i)
		assert.Equal(t, m["interval"], m["dedup_interval"], "rules[%d]: one sample per input series per window", i)
		rules = append(rules, r)
	}
	return rules, true
}

func (r aggrRule) matches(series map[string]string) bool {
	for _, group := range r.match.LabelFilterss {
		ok := true
		for _, f := range group {
			v := series[f.Label]
			var hit bool
			if f.IsRegexp {
				hit = regexp.MustCompile("^(?:" + f.Value + ")$").MatchString(v)
			} else {
				hit = v == f.Value
			}
			if hit == f.IsNegative {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func matching(rules []aggrRule, series map[string]string) []aggrRule {
	var out []aggrRule
	for _, r := range rules {
		if r.matches(series) {
			out = append(out, r)
		}
	}
	return out
}

func without(series map[string]string, drop []string) string {
	m := map[string]string{}
	for k, v := range series {
		m[k] = v
	}
	for _, d := range drop {
		delete(m, d)
	}
	return labelSetKey(m)
}

func mk(name string, kv ...string) map[string]string {
	m := map[string]string{"__name__": name}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestCollapseRulesPickOneOutputPerSeries(t *testing.T) {
	rules, found := streamAggrRules(t, "golden/observability-emitters/collapse.yaml")
	require.True(t, found, "the collapse case renders a streamAggrConfig")
	require.Len(t, rules, 6, "three rules per scope (pods, nodes)")

	pod := func(name, ns string) map[string]string {
		return mk(name, "namespace", ns, "pod", "runner-x", "uid", "u", "container", "runner", "k8s_cluster_name", "c1", "k8s_namespace_name", ns)
	}
	for _, tc := range []struct {
		series map[string]string
		output string
	}{
		{pod("container_memory_working_set_bytes", "arc-runners-7"), "sum_samples"},
		{pod("container_cpu_usage_seconds_total", "arc-runners-7"), "total"},
		{pod("container_network_receive_bytes_total", "ci-build"), "total"},
		{pod("kube_pod_info", "arc-runners-x"), "sum_samples"},
		{pod("kube_pod_container_status_restarts_total", "ci-build"), "total"},
		{pod("kube_pod_start_time", "arc-runners-7"), "max"},
		{pod("kube_pod_created", "arc-runners-7"), "max"},
		{pod("kube_pod_container_status_last_terminated_exitcode", "arc-runners-7"), "max"},
		{mk("kubelet_running_pods", "nodepool", "ci-pool", "node", "n1", "instance", "n1:10250"), "sum_samples"},
		{mk("kubelet_runtime_operations_duration_seconds_bucket", "nodepool", "ci-pool", "node", "n1", "le", "1"), "total"},
		{mk("process_start_time_seconds", "nodepool", "ci-pool", "node", "n1"), "max"},
		{mk("container_memory_working_set_bytes", "nodepool", "ci-pool", "id", "/system.slice/kubelet.service", "node", "n1"), "sum_samples"},
	} {
		got := matching(rules, tc.series)
		require.Lenf(t, got, 1, "%v is matched by exactly one rule (two would store it twice)", tc.series)
		assert.Equalf(t, []string{tc.output}, got[0].outputs, "%v", tc.series)
	}

	for _, tc := range []struct {
		why    string
		series map[string]string
	}{
		{"another namespace", pod("container_memory_working_set_bytes", "team-a")},
		{"near-miss namespace (anchored)", pod("container_memory_working_set_bytes", "arc-runners")},
		{"near-miss namespace (anchored, suffix)", pod("container_memory_working_set_bytes", "xci-build")},
		{"an application's own metric in a collapsed namespace", pod("my_app_requests_total", "arc-runners-7")},
		{"a ksm family that is not pod-level", pod("kube_job_status_failed", "arc-runners-7")},
		{"up on a pool node", mk("up", "nodepool", "ci-pool", "node", "n1", "instance", "n1:10250")},
		{"scrape_samples_scraped on a pool node", mk("scrape_samples_scraped", "nodepool", "ci-pool", "node", "n1")},
		{"a node of another pool", mk("kubelet_running_pods", "nodepool", "durable", "node", "n9")},
		{"a node of no pool (no nodepool label)", mk("kubelet_running_pods", "node", "n9", "instance", "n9:10250")},
		{"a pod-level series of another namespace on a pool node", mk("container_memory_working_set_bytes", "nodepool", "ci-pool", "namespace", "kube-system", "pod", "proxy-x", "container", "proxy", "node", "n1")},
	} {
		assert.Emptyf(t, matching(rules, tc.series), "%s must not be aggregated: %v", tc.why, tc.series)
	}
}

func TestCollapseMergesPodsAndNeverTenants(t *testing.T) {
	rules, _ := streamAggrRules(t, "golden/observability-emitters/collapse.yaml")
	series := func(pod, uid, node, cluster, ns string) map[string]string {
		return mk("container_memory_working_set_bytes",
			"namespace", "arc-runners-7", "pod", pod, "uid", uid, "container_id", "cid-"+pod, "id", "/kubepods/"+pod,
			"image", "img", "name", "n-"+pod, "pod_ip", "10.0.0."+pod, "ip", "10.0.0."+pod,
			"container", "runner", "node", node, "instance", node+":10250",
			"k8s_cluster_name", cluster, "k8s_namespace_name", ns, "deployment_environment_name", "development", "job", "kubelet")
	}
	a := series("runner-a", "u1", "n1", "c1", "arc-runners-7")
	b := series("runner-b", "u2", "n2", "c1", "arc-runners-7")
	got := matching(rules, a)
	require.Len(t, got, 1)
	assert.Equal(t, without(a, got[0].without), without(b, got[0].without), "two pods of one namespace on two nodes become one label set")

	// Another cluster (a tenant) and another scoped namespace stay apart.
	other := series("runner-a", "u1", "n1", "c2", "arc-runners-7")
	assert.NotEqual(t, without(a, got[0].without), without(other, got[0].without), "the cluster label is never dropped")
	otherNS := series("runner-a", "u1", "n1", "c1", "arc-runners-8")
	otherNS["namespace"] = "arc-runners-8"
	assert.NotEqual(t, without(a, got[0].without), without(otherNS, got[0].without), "(namespace, container) stay the key")

	for _, keep := range []string{"namespace", "container", "k8s_cluster_name", "k8s_namespace_name", "deployment_environment_name", "job", "le"} {
		assert.NotContains(t, got[0].without, keep, "%s identifies a tenant or the series and is never dropped", keep)
	}
	assert.Contains(t, got[0].without, "pod")
	assert.Contains(t, got[0].without, "uid")
	assert.Contains(t, got[0].without, "container_id")
}

func TestCollapseNodeLevelOnePerPool(t *testing.T) {
	rules, _ := streamAggrRules(t, "golden/observability-emitters/collapse.yaml")
	n := func(node, id string) map[string]string {
		return mk("container_cpu_usage_seconds_total", "nodepool", "ci-pool", "node", node, "instance", node+":10250", "id", id, "job", "kubelet", "k8s_cluster_name", "c1")
	}
	got := matching(rules, n("n1", "/"))
	require.Len(t, got, 1)
	assert.Equal(t, without(n("n1", "/"), got[0].without), without(n("n2", "/"), got[0].without), "the same cgroup of two nodes of a pool is one series")
	assert.NotEqual(t, without(n("n1", "/"), got[0].without), without(n("n1", "/kubepods.slice"), got[0].without), "different node cgroups stay different")
	assert.NotContains(t, got[0].without, "id")
	assert.NotContains(t, got[0].without, "nodepool")
}

func TestCollapseOffByDefault(t *testing.T) {
	for _, g := range []string{"minimal", "everything", "churn-drops-empty"} {
		_, found := streamAggrRules(t, "golden/observability-emitters/"+g+".yaml")
		assert.Falsef(t, found, "%s: no collapse configured, no streamAggrConfig", g)
	}
	// And no node-pool stamp in the scrape config.
	agent := findDoc(t, renderedDocs(t, "golden/observability-emitters/minimal.yaml"), "VMAgent", nil)
	inline := agent["spec"].(map[string]any)["inlineScrapeConfig"].(string)
	assert.NotContains(t, inline, "target_label: nodepool")
}

func TestCollapseNamespacesOnlyStampsNothingOnNodes(t *testing.T) {
	// Only `namespaces` set: pod rules, no node rules, no stamp.
	// Covered by the invalid fixtures and the golden `collapse`; the pool half
	// is asserted here from the golden's scrape config.
	agent := findDoc(t, renderedDocs(t, "golden/observability-emitters/collapse.yaml"), "VMAgent", nil)
	inline := agent["spec"].(map[string]any)["inlineScrapeConfig"].(string)
	assert.Equal(t, 2, strings.Count(inline, "target_label: nodepool"), "kubelet and cadvisor targets of a pool node are stamped")
	assert.Contains(t, inline, "regex: (ci-pool)")
}

// The default drops added with the collapse: the cAdvisor families nothing
// reads, with the memory metrics the recordings and dashboards read kept.
func TestCadvisorUnreadFamiliesDroppedByDefault(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	series := func(name string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "team-a", "pod": "p", "container": "c", "id": "/x"}
	}
	dropped := []string{
		"container_memory_failcnt", "container_memory_mapped_file", "container_memory_max_usage_bytes", "container_memory_kernel_usage",
		"container_memory_total_active_file_bytes", "container_memory_numa_pages", "container_memory_failures_total",
		"container_spec_cpu_period", "container_spec_cpu_quota", "container_spec_memory_limit_bytes", "container_spec_cpu_shares",
		"container_pressure_cpu_waiting_seconds_total", "container_pressure_memory_stalled_seconds_total", "container_pressure_io_waiting_seconds_total",
		"container_threads", "container_threads_max",
		"container_processes", "container_sockets", "container_file_descriptors",
		"container_cpu_user_seconds_total", "container_cpu_system_seconds_total",
		"container_cpu_load_average_10s", "container_cpu_load_d_average_10s",
		"container_last_seen", "container_health_state", "container_start_time_seconds", "container_ulimits_soft",
		"container_fs_reads_total", "container_fs_reads_bytes_total", "container_fs_reads_merged_total",
		"container_fs_writes_total", "container_fs_writes_bytes_total", "container_fs_writes_merged_total",
	}
	for _, name := range dropped {
		_, keep := applyCadvisorRelabel(cfgs, series(name))
		assert.Falsef(t, keep, "%s: nothing reads it, dropped by default", name)
	}
	kept := []string{
		// read: usage dashboards, memory alerts, the k8s.rules recordings.
		"container_memory_working_set_bytes", "container_memory_rss", "container_memory_cache", "container_memory_swap", "container_memory_usage_bytes",
		"container_cpu_usage_seconds_total",
		// the network panels the owner chose to keep.
		"container_network_receive_packets_total", "container_network_transmit_packets_total",
		"container_network_receive_errors_total", "container_network_transmit_errors_total",
		"container_network_receive_packets_dropped_total", "container_network_transmit_packets_dropped_total",
		"container_network_receive_bytes_total", "container_network_transmit_bytes_total",
		"container_cpu_cfs_periods_total", "container_cpu_cfs_throttled_periods_total", "container_cpu_cfs_throttled_seconds_total",
		"container_oom_events_total",
		// near misses of the patterns.
		"container_fs_read_seconds_total", "container_fs_usage_bytes", "container_memory",
	}
	for _, name := range kept {
		got, keep := applyCadvisorRelabel(cfgs, series(name))
		require.Truef(t, keep, "%s is read, or not named, and must survive", name)
		assert.NotContains(t, got, "__cadvisor_keep_pattern__", "the scratch label must not leak")
	}
}

func TestCadvisorPatternsEmptyStoresThemAgain(t *testing.T) {
	cfgs := cadvisorMetricRelabelConfigs(t, "golden/observability-emitters/churn-drops-empty.yaml")
	_, keep := applyCadvisorRelabel(cfgs, map[string]string{"__name__": "container_spec_cpu_period", "namespace": "team-a", "container": "c", "id": "/x"})
	assert.True(t, keep, "metricPatterns: [] stores the families again")
	_, keep = applyCadvisorRelabel(cfgs, map[string]string{"__name__": "container_cpu_user_seconds_total", "namespace": "team-a", "container": "c", "id": "/x"})
	assert.False(t, keep, "the exact-name list is its own key and still applies")
}

// kube_replicaset_*: dropped but for kube_replicaset_owner, which the
// `k8s.rules` pod-owner recordings join through.
func TestKubeStateMetricsReplicasetDroppedExceptOwner(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	for _, name := range []string{
		"kube_replicaset_created", "kube_replicaset_labels", "kube_replicaset_metadata_generation", "kube_replicaset_spec_replicas",
		"kube_replicaset_status_replicas", "kube_replicaset_status_ready_replicas", "kube_replicaset_status_observed_generation",
		"kube_replicaset_status_fully_labeled_replicas", "kube_replicaset_annotations",
	} {
		for _, ns := range []string{"team-a", "ci-build-1"} {
			_, keep := ksmSeries(cfgs, name, ns)
			assert.Falsef(t, keep, "%s in %s: nothing reads it", name, ns)
		}
	}
	got, keep := ksmSeries(cfgs, "kube_replicaset_owner", "team-a")
	require.True(t, keep, "kube_replicaset_owner is read by the k8s.rules pod-owner recordings")
	assert.NotContains(t, got, "__ksm_keep_pattern__")
	for _, name := range []string{"kube_deployment_status_replicas", "kube_pod_info", "kube_replicasets_total", "kube_statefulset_replicas"} {
		_, keep := ksmSeries(cfgs, name, "team-a")
		assert.Truef(t, keep, "%s is not a replicaset series and survives", name)
	}
	empty := globalScrapeRelabelConfigs(t, "golden/observability-emitters/churn-drops-empty.yaml")
	_, keep = ksmSeries(empty, "kube_replicaset_created", "team-a")
	assert.True(t, keep, "metricPatterns: [] stores them again")
}

// ephemeralJobs.keepMetrics: [] stores no kube_job_* series of a matched Job.
func TestKubeStateMetricsEphemeralJobsKeepNone(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/ksm-ephemeral-jobs-keep-none.yaml")
	job := func(name, jobName string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "observability", "pod": "ksm-0", "exported_namespace": "team-a", "job_name": jobName}
	}
	for _, j := range []string{"queue-janitor", "queue-janitor-29001"} {
		for _, m := range []string{"kube_job_created", "kube_job_owner", "kube_job_status_failed", "kube_job_info"} {
			_, keep := ksmSeriesLabels(cfgs, job(m, j))
			assert.Falsef(t, keep, "%s of %s: no Job series is stored for the janitor", m, j)
		}
	}
	// A backup CronJob the pattern does not name keeps its three series.
	for _, m := range []string{"kube_job_created", "kube_job_owner", "kube_job_status_failed"} {
		_, keep := ksmSeriesLabels(cfgs, job(m, "nightly-backup-29000"))
		assert.Truef(t, keep, "%s of a Job outside the pattern is untouched", m)
	}
	for _, c := range cfgs {
		assert.NotContains(t, c.TargetLabel, "__ksm_keep_job__", "no keep rule is rendered for an empty keepMetrics")
	}
}

// The log agent's own scrape drops the flags left at their defaults; the
// "Non-default flags" panel reads only is_set="true".
func TestLogAgentFlagMetricDropsDefaultsOnly(t *testing.T) {
	docs := renderedDocs(t, "golden/observability-emitters/minimal.yaml")
	pm := findDoc(t, docs, "PodMonitor", func(d map[string]any) bool {
		return strings.Contains(d["metadata"].(map[string]any)["name"].(string), "victoria-logs-collector")
	})
	eps := dig(pm, "spec", "podMetricsEndpoints").([]any)
	raw := eps[0].(map[string]any)["metricRelabelings"].([]any)
	require.Len(t, raw, 1)
	b, err := yaml.Marshal(k8sRelabelFieldsToPrometheusFields(raw[0].(map[string]any)))
	require.NoError(t, err)
	var cfg relabel.Config
	require.NoError(t, yaml.Unmarshal(b, &cfg))
	cfg.NameValidationScheme = model.LegacyValidation
	assert.Equal(t, "drop", string(cfg.Action))
	assert.True(t, cfg.Regex.MatchString("flag;false"))
	assert.False(t, cfg.Regex.MatchString("flag;true"), "the flags the panel reads are kept")
	assert.False(t, cfg.Regex.MatchString("vl_rows_ingested_total;false"), "only the flag metric")
}

// kube-state-metrics node labels: the allow-list reaches the pod's args.
func TestKubeStateMetricsNodeLabelAllowlist(t *testing.T) {
	docs := renderedDocs(t, "golden/observability-emitters/ksm-node-labels.yaml")
	dep := findDoc(t, docs, "Deployment", func(d map[string]any) bool {
		return strings.Contains(d["metadata"].(map[string]any)["name"].(string), "kube-state-metrics")
	})
	args := dig(dep, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["args"].([]any)
	var got []string
	for _, a := range args {
		got = append(got, a.(string))
	}
	sort.Strings(got)
	assert.Contains(t, got, "--metric-labels-allowlist=nodes=[karpenter.sh/nodepool]")

	// Default off: the argument is absent.
	docs = renderedDocs(t, "golden/observability-emitters/cnpg-preset.yaml")
	dep = findDoc(t, docs, "Deployment", func(d map[string]any) bool {
		return strings.Contains(d["metadata"].(map[string]any)["name"].(string), "kube-state-metrics")
	})
	for _, a := range dig(dep, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["args"].([]any) {
		assert.NotContains(t, a.(string), "metric-labels-allowlist")
	}
}
