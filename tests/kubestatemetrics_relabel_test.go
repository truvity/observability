// This file proves the one relabel mechanism charts/observability-emitters'
// kube-state-metrics support depends on, against a REAL relabel
// implementation rather than by reading the YAML and reasoning about it.
//
// Reasoning about it is exactly what went wrong the first time: the
// original `metricRelabelings` read `namespace` as the source for
// `k8s_namespace_name`, on the assumption that `namespace` still held
// kube-state-metrics' own per-series field (the object's namespace) by
// the time metric relabeling runs. It does not. The VictoriaMetrics
// operator's ServiceMonitor-to-VMServiceScrape conversion
// (vmscrapes/servicescrape.go in github.com/VictoriaMetrics/operator)
// stamps `namespace`, `pod`, `container` and `service` as TARGET labels —
// the scrape target's own identity, i.e. the kube-state-metrics pod's —
// unconditionally, for every endpoints-role ServiceMonitor it converts.
// This chart forces `overrideHonorLabels: true` (`honor_labels: false` on
// every scrape), and Prometheus/vmagent's own scrape semantics under
// `honor_labels: false` are: when a scraped sample's own label collides
// with a label the target already carries, the TARGET's value wins and
// the sample's own value is kept only as `exported_<name>`. So by the
// time metric_relabel_configs run, a kube-state-metrics series such as
// `kube_pod_info{namespace="team-a",pod="web-0",...}` arrives as
// `kube_pod_info{namespace="<ksm's own namespace>",pod="<ksm's own
// pod>",exported_namespace="team-a",exported_pod="web-0",...}` — reading
// `namespace` reads the WRONG one.
package tests

import (
	"strings"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv2 "gopkg.in/yaml.v2"
	"gopkg.in/yaml.v3"
)

// kubeStateMetricsMetricRelabelings reads the RENDERED metricRelabelings
// off the kube-state-metrics ServiceMonitor in the `everything` golden —
// not a copy of them written into the test — so a values.yaml change that
// is never regenerated into the golden cannot make this test pass on a
// stale belief about what ships.
//
// Each entry is re-marshalled to YAML and decoded with gopkg.in/yaml.v2,
// not v3: relabel.Config implements the v2-shaped `UnmarshalYAML(func(any)
// error) error` interface, which is where Prometheus's OWN defaults live
// (separator `;`, regex `(.*)`, replacement `$1`, action `replace`). v3
// does not recognise that interface and would silently leave those
// zero-valued — a nil `Regexp` that panics the moment it is matched
// against, for the very entries that omit `action` because "replace" is
// the default. Going through the same unmarshaller Prometheus itself uses
// is the only way this test is exercising the same defaulting real
// tooling would.
func kubeStateMetricsMetricRelabelings(t *testing.T) []*relabel.Config {
	t.Helper()
	docs := renderedDocs(t, "golden/observability-emitters/everything.yaml")
	sm := findDoc(t, docs, "ServiceMonitor", func(d map[string]any) bool {
		meta, _ := d["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		return strings.Contains(name, "kube-state-metrics")
	})
	spec, ok := sm["spec"].(map[string]any)
	require.True(t, ok, "kube-state-metrics ServiceMonitor has no spec")
	endpoints, ok := spec["endpoints"].([]any)
	require.True(t, ok && len(endpoints) > 0, "kube-state-metrics ServiceMonitor spec has no endpoints")
	ep, ok := endpoints[0].(map[string]any)
	require.True(t, ok)
	raw, ok := ep["metricRelabelings"].([]any)
	require.True(t, ok, "the endpoint has no metricRelabelings at all")
	require.NotEmpty(t, raw, "metricRelabelings is empty")

	cfgs := make([]*relabel.Config, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		require.Truef(t, ok, "metricRelabelings[%d] is not a mapping", i)
		b, err := yaml.Marshal(k8sRelabelFieldsToPrometheusFields(m))
		require.NoErrorf(t, err, "re-marshalling metricRelabelings[%d]", i)
		var cfg relabel.Config
		require.NoErrorf(t, yamlv2.Unmarshal(b, &cfg), "decoding metricRelabelings[%d]: %s", i, string(b))
		// relabel.relabel() panics on an unset validation scheme rather
		// than defaulting one; ordinarily Config.Validate sets this from
		// the global scrape config, which nothing here renders, so it is
		// set directly. Legacy, not UTF8: every label this chain reads or
		// writes (namespace, pod, container, service, k8s_namespace_name)
		// is a plain Prometheus-legacy name, and this is what vmagent
		// itself validates against unless told otherwise.
		cfg.NameValidationScheme = model.LegacyValidation
		cfgs = append(cfgs, &cfg)
	}
	return cfgs
}

// k8sRelabelFieldsToPrometheusFields renames the two fields the Prometheus
// Operator CRD (and this chart's values, which mirror it) spell in
// camelCase to the snake_case Prometheus's own relabel.Config decodes —
// the exact rename `generateRelabelConfig` performs in the VictoriaMetrics
// operator (vmscrapes/servicescrape.go) when it converts a ServiceMonitor's
// `RelabelConfigs` into the scrape config vmagent actually runs. Getting
// this wrong is not cosmetic: yaml.v2 silently ignores an unrecognised
// key rather than erroring, so a `sourceLabels`/`targetLabel` pair fed to
// relabel.Config decodes as an EMPTY source-label list and an empty
// target — which turns a `replace` rule into a silent no-op, not a
// decode error, and made this test misreport its own first draft.
func k8sRelabelFieldsToPrometheusFields(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for k, v := range item {
		switch k {
		case "sourceLabels":
			out["source_labels"] = v
		case "targetLabel":
			out["target_label"] = v
		default:
			out[k] = v
		}
	}
	return out
}

// applyRelabel runs the real relabel.ProcessBuilder over a hand-built
// input label set and returns the result as a plain map for assertions.
func applyRelabel(t *testing.T, cfgs []*relabel.Config, in map[string]string) map[string]string {
	t.Helper()
	lb := labels.NewBuilder(labels.FromMap(in))
	keep := relabel.ProcessBuilder(lb, cfgs...)
	require.True(t, keep, "the relabel chain dropped the series entirely, which none of these rules should ever do")
	return lb.Labels().Map()
}

// TestKubeStateMetricsNamespaceStampReadsTheObjectNotTheTarget is the
// fail-before/pass-after proof. Three label sets, each built by hand as
// vmagent would actually present it to metric_relabel_configs — after the
// operator's own target-level namespace/pod/container/service stamp AND
// after the honor_labels:false collision rename described above — for the
// three shapes that matter: a series carrying namespace, pod AND
// container of its own; a series carrying only namespace; and a
// genuinely cluster-scoped series carrying none of the four.
func TestKubeStateMetricsNamespaceStampReadsTheObjectNotTheTarget(t *testing.T) {
	cfgs := kubeStateMetricsMetricRelabelings(t)

	const (
		ksmNamespace = "observability"           // the kube-state-metrics POD's own namespace
		ksmPod       = "kube-state-metrics-abc12" // the kube-state-metrics pod's own name
		ksmContainer = "kube-state-metrics"
		ksmService   = "kube-state-metrics"
		cluster      = "example-cluster"
		tier         = "development"
	)

	cases := []struct {
		name string
		in   map[string]string
		want map[string]string
		// Labels that must be ABSENT from the result: an artifact of the
		// scrape target that must not survive, not merely a value to check.
		absent []string
	}{
		{
			// kube_pod_container_status_restarts_total{namespace,pod,container,...}
			// -- the fleet's own crashloop rule reads exactly this metric.
			name: "namespace+pod+container series (kube_pod_container_status_restarts_total)",
			in: map[string]string{
				"__name__":                    "kube_pod_container_status_restarts_total",
				"namespace":                   ksmNamespace,
				"pod":                         ksmPod,
				"container":                   ksmContainer,
				"service":                     ksmService,
				"exported_namespace":          "team-a",
				"exported_pod":                "web-0",
				"exported_container":          "app",
				"k8s_namespace_name":          ksmNamespace,
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
				"job":                         "kube-state-metrics",
			},
			want: map[string]string{
				"__name__":                    "kube_pod_container_status_restarts_total",
				"namespace":                   "team-a",
				"pod":                         "web-0",
				"container":                   "app",
				"k8s_namespace_name":          "team-a",
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
				"job":                         "kube-state-metrics",
			},
			absent: []string{"service", "exported_namespace", "exported_pod", "exported_container", "exported_service"},
		},
		{
			// kube_deployment_status_replicas{namespace,deployment,...} --
			// carries namespace but no pod/container/service of its own.
			name: "namespace-only series (kube_deployment_status_replicas)",
			in: map[string]string{
				"__name__":                    "kube_deployment_status_replicas",
				"namespace":                   ksmNamespace,
				"pod":                         ksmPod,
				"container":                   ksmContainer,
				"service":                     ksmService,
				"exported_namespace":          "team-b",
				"deployment":                  "api",
				"k8s_namespace_name":          ksmNamespace,
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
			},
			want: map[string]string{
				"__name__":                    "kube_deployment_status_replicas",
				"namespace":                   "team-b",
				"deployment":                  "api",
				"k8s_namespace_name":          "team-b",
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
			},
			absent: []string{"pod", "container", "service", "exported_namespace"},
		},
		{
			// kube_node_status_condition{node,condition,status,...} -- a
			// genuinely cluster-scoped kind: no namespace, pod, container
			// or service of its own anywhere in its exposition.
			name: "cluster-scoped series (kube_node_status_condition)",
			in: map[string]string{
				"__name__":                    "kube_node_status_condition",
				"namespace":                   ksmNamespace,
				"pod":                         ksmPod,
				"container":                   ksmContainer,
				"service":                     ksmService,
				"node":                        "node-1",
				"condition":                   "Ready",
				"status":                      "true",
				"k8s_namespace_name":          ksmNamespace,
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
			},
			want: map[string]string{
				"__name__":                    "kube_node_status_condition",
				"node":                        "node-1",
				"condition":                   "Ready",
				"status":                      "true",
				"k8s_cluster_name":            cluster,
				"deployment_environment_name": tier,
			},
			absent: []string{"namespace", "pod", "container", "service", "k8s_namespace_name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyRelabel(t, cfgs, tc.in)
			for k, v := range tc.want {
				assert.Equalf(t, v, got[k], "label %q", k)
			}
			for _, k := range tc.absent {
				v, ok := got[k]
				assert.Falsef(t, ok, "label %q should have been dropped, still reads %q", k, v)
			}
		})
	}
}
