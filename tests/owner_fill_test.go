// This file proves `notifications.ownerFill`: the owner label filled at the
// vmalert -> Alertmanager edge, by the notifier configuration file the chart
// renders. The relabel rules are read back out of the rendered golden and
// their `if` selectors are parsed with MetricsQL (the parser VictoriaMetrics
// itself uses), then evaluated with the selector semantics the vmalert
// documentation gives: a label an alert lacks is empty, `=~` is anchored.
package tests

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type fillRule struct {
	If          string `yaml:"if"`
	Action      string `yaml:"action"`
	TargetLabel string `yaml:"target_label"`
	Replacement string `yaml:"replacement"`
}

type notifierFile struct {
	Scheme        string `yaml:"scheme"`
	PathPrefix    string `yaml:"path_prefix"`
	StaticConfigs []struct {
		Targets []string `yaml:"targets"`
	} `yaml:"static_configs"`
	AlertRelabelConfigs []fillRule `yaml:"alert_relabel_configs"`
}

func ownerFillFile(t *testing.T, docs []map[string]any) notifierFile {
	t.Helper()
	sec := findDoc(t, docs, "Secret", func(d map[string]any) bool {
		return strings.HasSuffix(fmt.Sprint(dig(d, "metadata", "name")), "-vmalert-notifier")
	})
	raw, _ := dig(sec, "stringData", "notifier.yaml").(string)
	require.NotEmpty(t, raw)
	var f notifierFile
	require.NoError(t, yaml.Unmarshal([]byte(raw), &f))
	return f
}

// matches evaluates one `if` selector against an alert's labels.
func fillMatches(t *testing.T, sel string, lbls map[string]string) bool {
	t.Helper()
	e, err := metricsql.Parse(sel)
	require.NoError(t, err, sel)
	me, ok := e.(*metricsql.MetricExpr)
	require.True(t, ok, sel)
	require.Len(t, me.LabelFilterss, 1, sel)
	for _, lf := range me.LabelFilterss[0] {
		v := lbls[lf.Label]
		var m bool
		if lf.IsRegexp {
			m = regexp.MustCompile("^(?:" + lf.Value + ")$").MatchString(v)
		} else {
			m = v == lf.Value
		}
		if m == lf.IsNegative {
			return false
		}
	}
	return true
}

// fill applies the rules in order the way vmalert's relabeling does.
func fill(t *testing.T, rules []fillRule, in map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	for _, r := range rules {
		require.Equal(t, "replace", r.Action)
		if fillMatches(t, r.If, out) {
			out[r.TargetLabel] = r.Replacement
		}
	}
	return out
}

func TestOwnerFillRelabeling(t *testing.T) {
	for _, chart := range []string{"observability-stack", "observability-alerting"} {
		t.Run(chart, func(t *testing.T) {
			f := ownerFillFile(t, renderedDocs(t, "golden/"+chart+"/notifications-owner-fill.yaml"))
			cases := []struct {
				name string
				in   map[string]string
				want string
			}{
				{"literal namespace", map[string]string{"k8s_namespace_name": "checkout"}, "acme"},
				{"glob namespace", map[string]string{"k8s_namespace_name": "payments-eu"}, "acme"},
				{"other owner", map[string]string{"k8s_namespace_name": "globex-web"}, "globex"},
				{"glob is anchored", map[string]string{"k8s_namespace_name": "xglobex-web"}, "platform"},
				{"literal is anchored", map[string]string{"k8s_namespace_name": "checkout-2"}, "platform"},
				{"unclaimed namespace gets the default", map[string]string{"k8s_namespace_name": "kube-system"}, "platform"},
				{"no namespace (a node) gets the default", map[string]string{"alertname": "NodeMemoryPressureHigh"}, "platform"},
				{"second label used when the first is empty", map[string]string{"namespace": "globex-web"}, "globex"},
				{"first non-empty label decides", map[string]string{"k8s_namespace_name": "kube-system", "namespace": "checkout"}, "platform"},
				{"a present owner is never overwritten", map[string]string{"k8s_namespace_name": "checkout", "owner": "trustform"}, "trustform"},
				{"a present owner on a namespace-less alert is kept", map[string]string{"owner": "finttech"}, "finttech"},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					assert.Equal(t, c.want, fill(t, f.AlertRelabelConfigs, c.in)["owner"])
				})
			}
		})
	}
}

// Every vmalert the chart renders reads the file and none keeps `notifiers`
// (the operator refuses both), and the Alertmanager pair is the file's
// targets, one per replica.
func TestOwnerFillEveryVMAlertReadsTheFile(t *testing.T) {
	for _, chart := range []string{"observability-stack", "observability-alerting"} {
		t.Run(chart, func(t *testing.T) {
			docs := renderedDocs(t, "golden/"+chart+"/notifications-owner-fill.yaml")
			n := 0
			for _, d := range docs {
				if d["kind"] != "VMAlert" {
					continue
				}
				n++
				name := fmt.Sprint(dig(d, "metadata", "name"))
				ref, _ := dig(d, "spec", "notifierConfigRef").(map[string]any)
				require.NotNil(t, ref, name)
				assert.Equal(t, "notifier.yaml", ref["key"], name)
				assert.Contains(t, fmt.Sprint(ref["name"]), "-vmalert-notifier", name)
				assert.Nil(t, dig(d, "spec", "notifiers"), name)
				assert.NotEmpty(t, dig(d, "spec", "podMetadata", "annotations", "checksum/notifier-config"), name)
			}
			assert.Equal(t, 3, n, "metrics, logs and the remote evaluator")
			f := ownerFillFile(t, docs)
			require.Len(t, f.StaticConfigs, 1)
			assert.Len(t, f.StaticConfigs[0].Targets, 2)
			assert.Equal(t, "http", f.Scheme)
		})
	}
}

// Off by default: no Secret, `notifiers` as before.
func TestOwnerFillOffByDefault(t *testing.T) {
	for _, g := range []string{"golden/observability-stack/alertmanager-pair.yaml", "golden/observability-alerting/alertmanager-pair.yaml"} {
		raw, err := os.ReadFile(g)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "notifierConfigRef", g)
		assert.NotContains(t, string(raw), "-vmalert-notifier", g)
		assert.Contains(t, string(raw), "notifiers:", g)
	}
}

// A notifierUrl is split into the file's scheme, host and path_prefix, and
// under `ha` the peer's pod keeps its spread label beside the checksum.
func TestOwnerFillNotifierUrlAndHA(t *testing.T) {
	out, stderr, err := helmTemplate(t, "x", "observability-alerting", "default",
		"--set", "stackReleaseName=observability-stack",
		"--set", "alertmanager.enabled=false",
		"--set", "alertmanager.notifierUrl=https://am.example:9093/prefix/",
		"--set", "notifications.ownerLabel=owner",
		"--set", "notifications.ownerFill.enabled=true",
		"--set", "notifications.ownerFill.default=platform",
		"--set", "notifications.slack.workspaces[0].name=acme",
		"--set", "notifications.slack.workspaces[0].appTokenSecret.name=s",
		"--set", "notifications.slack.workspaces[0].appTokenSecret.key=token",
		"--set", "notifications.severities.critical.receiver=slack",
		"--set", "notifications.severities.critical.channel=#a",
		"--set", "notifications.severities.warning.receiver=slack",
		"--set", "notifications.severities.warning.channel=#a",
		"--set", "vmalert.externalUrl=https://grafana.example",
	)
	require.NoError(t, err, stderr)
	var f notifierFile
	for _, doc := range strings.Split(out, "\n---\n") {
		if strings.Contains(doc, "kind: Secret") && strings.Contains(doc, "vmalert-notifier") {
			i := strings.Index(doc, "notifier.yaml: |")
			require.NoError(t, yaml.Unmarshal([]byte(doc[i+len("notifier.yaml: |"):]), &f))
		}
	}
	assert.Equal(t, "https", f.Scheme)
	assert.Equal(t, "/prefix", f.PathPrefix)
	require.Len(t, f.StaticConfigs, 1)
	assert.Equal(t, []string{"am.example:9093"}, f.StaticConfigs[0].Targets)
	require.Len(t, f.AlertRelabelConfigs, 1, "no owners: only the default rule")
	assert.Equal(t, "platform", f.AlertRelabelConfigs[0].Replacement)
}
