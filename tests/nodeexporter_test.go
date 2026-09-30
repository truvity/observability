// This file holds charts/observability-emitters' node-exporter to what it
// exists for, against the `node-exporter` golden render: a DaemonSet that
// really reads the node (host network and PID, the host's filesystems
// read-only), that runs on every node the consumer does not exclude, that
// carries only the collectors something reads, and a scrape object that
// stores the series the way the node-exporter dashboard and the k8s-stack's
// `node.rules` select on them.
//
// The label chain is run through the real Prometheus relabel implementation
// from what is RENDERED (the default scrape class, the ServiceMonitor's
// relabelings and metricRelabelings), not from a copy written into the test.
// hack/node-exporter-proof.sh is the same claim against the real binaries.
package tests

import (
	"strings"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const nodeExporterGolden = "golden/observability-emitters/node-exporter.yaml"

func nodeExporterDaemonSet(t *testing.T) map[string]any {
	t.Helper()
	return findDoc(t, renderedDocs(t, nodeExporterGolden), "DaemonSet", func(d map[string]any) bool {
		return strings.Contains(dig(d, "metadata", "name").(string), "node-exporter")
	})
}

func nodeExporterPod(t *testing.T) map[string]any {
	t.Helper()
	pod, ok := dig(nodeExporterDaemonSet(t), "spec", "template", "spec").(map[string]any)
	require.True(t, ok, "no pod spec on the node-exporter DaemonSet")
	return pod
}

func nodeExporterContainer(t *testing.T) map[string]any {
	t.Helper()
	for _, c := range nodeExporterPod(t)["containers"].([]any) {
		if m := c.(map[string]any); m["name"] == "node-exporter" {
			return m
		}
	}
	t.Fatal("no node-exporter container")
	return nil
}

func TestNodeExporterReadsTheNodeNotThePod(t *testing.T) {
	pod := nodeExporterPod(t)
	assert.Equal(t, true, pod["hostNetwork"], "without the host's network namespace node_network_* describes the pod")
	assert.Equal(t, true, pod["hostPID"], "without the host's PID namespace node_processes_* describes the pod")
	assert.NotEqual(t, true, pod["hostIPC"], "nothing here reads the host's IPC namespace")

	mounts := map[string]map[string]any{}
	for _, m := range nodeExporterContainer(t)["volumeMounts"].([]any) {
		mm := m.(map[string]any)
		mounts[mm["mountPath"].(string)] = mm
	}
	for _, path := range []string{"/host/proc", "/host/sys", "/host/root"} {
		require.Containsf(t, mounts, path, "the exporter needs the host's %s", path)
		assert.Equalf(t, true, mounts[path]["readOnly"], "%s must be read-only", path)
	}
	assert.Equal(t, "HostToContainer", mounts["/host/root"]["mountPropagation"], "mounts made on the node after the pod started must show up")

	args := nodeExporterContainer(t)["args"].([]any)
	var joined []string
	for _, a := range args {
		joined = append(joined, a.(string))
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{"--path.procfs=/host/proc", "--path.sysfs=/host/sys", "--path.rootfs=/host/root"} {
		assert.Contains(t, all, want)
	}

	sc := dig(nodeExporterContainer(t), "securityContext", "readOnlyRootFilesystem")
	assert.Equal(t, true, sc, "the container's own filesystem is read-only")
	assert.Equal(t, true, dig(pod, "securityContext", "runAsNonRoot"), "the exporter reads /proc and /sys as nobody")
}

// The node-agent defaults are the log collector's: a DaemonSet pod cannot
// go elsewhere, so it must be able to preempt, tolerate every taint, and
// ask for what it uses.
func TestNodeExporterHasTheNodeAgentDefaults(t *testing.T) {
	pod := nodeExporterPod(t)
	assert.Equal(t, "system-node-critical", pod["priorityClassName"])
	assert.Equal(t, []any{map[string]any{"operator": "Exists"}}, pod["tolerations"], "a node agent tolerates every taint, NoExecute included")
	assert.Equal(t, map[string]any{"kubernetes.io/os": "linux"}, pod["nodeSelector"])

	res := nodeExporterContainer(t)["resources"].(map[string]any)
	req, lim := res["requests"].(map[string]any), res["limits"].(map[string]any)
	assert.Equal(t, lim["memory"], req["memory"], "memory is capped where it is measured: request equals limit")
	assert.Equal(t, "10m", req["cpu"])
	assert.NotContains(t, lim, "cpu", "no CPU limit: a quota throttles the scrape burst")
}

// A consumer that keeps ephemeral CI pools out of its DaemonSets passes the
// same nodeAffinity it passes the log collector. It reaches the pod, and
// upstream's own exclusions (Fargate, virtual kubelet) are replaced by it
// rather than merged, which the values comment says.
func TestNodeExporterTakesTheConsumersNodeAffinity(t *testing.T) {
	terms := dig(nodeExporterPod(t), "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)
	require.Len(t, terms, 1)
	exprs := terms[0].(map[string]any)["matchExpressions"].([]any)
	require.Len(t, exprs, 1)
	e := exprs[0].(map[string]any)
	assert.Equal(t, "karpenter.sh/nodepool", e["key"])
	assert.Equal(t, "NotIn", e["operator"])
	assert.Equal(t, []any{"ci-example-a", "ci-example-b"}, e["values"])
}

// The collector set is the chart's: everything upstream turns on by default
// is off, and each one on has a reader.
func TestNodeExporterCollectorSetIsExplicitAndLean(t *testing.T) {
	var flags []string
	for _, a := range nodeExporterContainer(t)["args"].([]any) {
		flags = append(flags, a.(string))
	}
	assert.Contains(t, flags, "--collector.disable-defaults", "upstream's defaults must be off so the set below is the whole set")
	on := map[string]bool{}
	for _, f := range flags {
		if strings.HasPrefix(f, "--collector.") && strings.Count(f, ".") == 1 && !strings.Contains(f, "=") {
			on[strings.TrimPrefix(f, "--collector.")] = true
		}
	}
	// What `node.rules`, `kube-prometheus-node-recording.rules` and the two
	// node dashboards read.
	read := []string{"cpu", "meminfo", "loadavg", "diskstats", "filesystem", "netdev", "netstat", "sockstat", "stat", "vmstat", "uname", "time", "filefd"}
	for _, c := range read {
		assert.Truef(t, on[c], "collector %q is read by a rule or a dashboard and must be on", c)
	}
	// Per-CPU-per-IRQ, a D-Bus grant, and per-sensor series absent on a
	// cloud VM: nothing reads them and they are the costly ones.
	for _, c := range []string{"interrupts", "systemd", "cpufreq", "thermal_zone", "rapl", "powersupplyclass", "zoneinfo", "slabinfo", "textfile"} {
		assert.Falsef(t, on[c], "collector %q is left out on purpose", c)
	}
	// The pod interfaces and per-pod mounts a node churns through.
	all := strings.Join(flags, "\n")
	assert.Contains(t, all, "--collector.netdev.device-exclude=")
	assert.Contains(t, all, "eni[0-9a-f]{8,}", "the AWS CNI's per-pod host interfaces")
	assert.Contains(t, all, "--collector.filesystem.mount-points-exclude=")
	assert.Contains(t, all, "var/lib/kubelet/(pods|plugins)/.+", "per-pod tmpfs and CSI mounts")
}

func nodeExporterRelabelChain(t *testing.T) (class, endpoint, metric []*relabel.Config) {
	t.Helper()
	docs := renderedDocs(t, nodeExporterGolden)
	toConfigs := func(v any) []*relabel.Config {
		var out []*relabel.Config
		for i, item := range v.([]any) {
			b, err := yaml.Marshal(k8sRelabelFieldsToPrometheusFields(item.(map[string]any)))
			require.NoError(t, err)
			var cfg relabel.Config
			require.NoErrorf(t, yaml.Unmarshal(b, &cfg), "decoding relabel step %d: %s", i, b)
			cfg.NameValidationScheme = model.LegacyValidation
			out = append(out, &cfg)
		}
		return out
	}
	agent := findDoc(t, docs, "VMAgent", nil)
	classes := dig(agent, "spec", "scrapeClasses").([]any)
	require.NotEmpty(t, classes)
	class = toConfigs(classes[0].(map[string]any)["relabelConfigs"])

	sm := findDoc(t, docs, "ServiceMonitor", func(d map[string]any) bool {
		return strings.Contains(dig(d, "metadata", "name").(string), "node-exporter")
	})
	eps := dig(sm, "spec", "endpoints").([]any)
	require.Len(t, eps, 1)
	ep := eps[0].(map[string]any)
	return class, toConfigs(ep["relabelings"]), toConfigs(ep["metricRelabelings"])
}

// The series are stored the way the dashboard and the recording rules
// select on them: `job="node-exporter"`, `instance` and `node` the node's
// name, the cluster and tier stamped, and no namespace key (a node belongs
// to no namespace), while the exporter pod's own `namespace` and `pod`
// stay, because `node.rules` joins on them.
func TestNodeExporterSeriesAreStoredTheWayTheRulesAndDashboardsSelectThem(t *testing.T) {
	class, endpoint, metric := nodeExporterRelabelChain(t)

	// The target as the operator's ServiceMonitor conversion hands it over
	// (the labels it stamps, per its converter) and as service discovery
	// describes it. The Service-named job and the ip:port instance are what
	// would be stored without the chart's own steps.
	target := map[string]string{
		"job":                             "x-prometheus-node-exporter",
		"instance":                        "10.0.1.7:9100",
		"namespace":                       "observability",
		"pod":                             "x-prometheus-node-exporter-abc12",
		"container":                       "node-exporter",
		"service":                         "x-prometheus-node-exporter",
		"endpoint":                        "metrics",
		"__meta_kubernetes_namespace":     "observability",
		"__meta_kubernetes_pod_node_name": "node-a",
		"__meta_kubernetes_pod_label_app_kubernetes_io_instance": "x",
	}
	got := applyRelabel(t, append(class, endpoint...), target)
	assert.Equal(t, "node-exporter", got["job"], "the kube-prometheus job name, not the Service's")
	assert.Equal(t, "node-a", got["instance"], "the node's name, a stable identity, not ip:port")
	assert.Equal(t, "node-a", got["node"])
	assert.Equal(t, "example-cluster", got["k8s_cluster_name"])
	assert.Equal(t, "development", got["deployment_environment_name"])
	assert.Equal(t, "observability", got["namespace"], "node.rules joins on the exporter pod's namespace and pod")
	assert.Equal(t, "x-prometheus-node-exporter-abc12", got["pod"])

	series := map[string]string{"__name__": "node_cpu_seconds_total", "cpu": "0", "mode": "idle"}
	for k, v := range got {
		if !strings.HasPrefix(k, "__") {
			series[k] = v
		}
	}
	stored := applyRelabel(t, metric, series)
	_, has := stored["k8s_namespace_name"]
	assert.Falsef(t, has, "a node series carries no k8s_namespace_name (the default class stamped %q from the pod's namespace)",
		series["k8s_namespace_name"])
	assert.Equal(t, "example-cluster", stored["k8s_cluster_name"])
	assert.Equal(t, "node-exporter", stored["job"])
	assert.Equal(t, "idle", stored["mode"])
}
