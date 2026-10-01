// This file proves the opt-in owner stamp (`tenancy.owners`) against a REAL
// relabel implementation for metrics, and against the rendered OTTL for the
// OTLP signals, reading both back out of the rendered golden rather than
// from a copy written into the test. The "off" goldens must carry nothing.
package tests

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const ownersGolden = "golden/observability-emitters/tenancy-owners.yaml"

func ownerRelabelConfigs(t *testing.T, golden string) []*relabel.Config {
	t.Helper()
	agent := findDoc(t, renderedDocs(t, golden), "VMAgent", nil)
	raw, _ := dig(agent, "spec", "inlineRelabelConfig").([]any)
	var cfgs []*relabel.Config
	for i, item := range raw {
		b, err := yaml.Marshal(item)
		require.NoError(t, err)
		var cfg relabel.Config
		require.NoErrorf(t, yaml.Unmarshal(b, &cfg), "decoding inlineRelabelConfig[%d]", i)
		cfg.NameValidationScheme = model.LegacyValidation
		cfgs = append(cfgs, &cfg)
	}
	return cfgs
}

func ownerOf(cfgs []*relabel.Config, in map[string]string) (string, bool) {
	lb := labels.NewBuilder(labels.FromMap(in))
	relabel.ProcessBuilder(lb, cfgs...)
	out := lb.Labels().Map()
	v, ok := out["owner"]
	return v, ok
}

// A series whose k8s_namespace_name is mapped to an owner gets that owner;
// an unmatched one gets the default; one an application labelled itself is
// overwritten (the application does not choose); cluster-scoped series (no
// namespace) get the default too.
func TestOwnerStampMetrics(t *testing.T) {
	cfgs := ownerRelabelConfigs(t, ownersGolden)
	require.NotEmpty(t, cfgs)

	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"literal", map[string]string{"__name__": "up", "k8s_namespace_name": "checkout"}, "acme"},
		{"glob", map[string]string{"__name__": "up", "k8s_namespace_name": "payments-eu"}, "acme"},
		{"other owner", map[string]string{"__name__": "up", "k8s_namespace_name": "globex-web"}, "globex"},
		{"unmatched gets default", map[string]string{"__name__": "up", "k8s_namespace_name": "kube-system"}, "acme"},
		{"no namespace gets default", map[string]string{"__name__": "node_load1"}, "acme"},
		{"glob is anchored", map[string]string{"__name__": "up", "k8s_namespace_name": "xglobex-web"}, "acme"},
		{"self-claimed owner is replaced", map[string]string{"__name__": "up", "k8s_namespace_name": "globex-web", "owner": "acme"}, "globex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ownerOf(cfgs, c.in)
			require.True(t, ok, "no owner label")
			assert.Equal(t, c.want, got)
		})
	}
}

// With no default, an unmatched series carries no owner label at all.
func TestOwnerStampMetricsNoDefault(t *testing.T) {
	cfgs := ownerRelabelConfigs(t, ownersGolden)
	// Drop the default rule (the last one) to model `defaultOwner: ""`.
	cfgs = cfgs[:len(cfgs)-1]
	_, ok := ownerOf(cfgs, map[string]string{"__name__": "up", "k8s_namespace_name": "kube-system"})
	assert.False(t, ok)
	got, ok := ownerOf(cfgs, map[string]string{"__name__": "up", "k8s_namespace_name": "checkout"})
	assert.True(t, ok)
	assert.Equal(t, "acme", got)
}

var ottlOwner = regexp.MustCompile(
	`set\(attributes\["owner"\], "([^"]+)"\) where attributes\["owner"\] == nil` +
		`(?: and attributes\["k8s.namespace.name"\] != nil` +
		` and IsMatch\(attributes\["k8s.namespace.name"\], "([^"]+)"\))?`)

// The gateway's OTTL, for each of the three signals, resolves the same
// owners the metric rules do (evaluated here in Go, first match wins, then
// the default), and the disown step removes a self-claimed owner first.
func TestOwnerStampGateway(t *testing.T) {
	cm := findDoc(t, renderedDocs(t, ownersGolden), "ConfigMap", func(d map[string]any) bool {
		return dig(d, "data", "config.yaml") != nil
	})
	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(dig(cm, "data", "config.yaml").(string)), &cfg))

	for _, sig := range []string{"metric", "log", "trace"} {
		t.Run(sig, func(t *testing.T) {
			stmts, _ := dig(cfg, "processors", "transform/tenancy", sig+"_statements").([]any)
			require.NotEmpty(t, stmts)
			var rules [][2]string // owner, pattern ("" = default)
			for _, c := range stmts {
				for _, s := range dig(c, "statements").([]any) {
					if m := ottlOwner.FindStringSubmatch(s.(string)); m != nil {
						rules = append(rules, [2]string{m[1], m[2]})
					}
				}
			}
			require.Len(t, rules, 3, "two owners and the default")
			resolve := func(ns string) string {
				for _, r := range rules {
					if r[1] == "" || regexp.MustCompile(r[1]).MatchString(ns) {
						return r[0]
					}
				}
				return ""
			}
			assert.Equal(t, "acme", resolve("payments-eu"))
			assert.Equal(t, "globex", resolve("globex-web"))
			assert.Equal(t, "acme", resolve("kube-system"))

			dis, _ := dig(cfg, "processors", "transform/disown", sig+"_statements").([]any)
			assert.Contains(t, mustYAML(t, dis), `delete_key(attributes, "owner")`)
		})
	}
	assert.Contains(t, mustYAML(t, dig(cfg, "exporters")), "- owner")
}

func mustYAML(t *testing.T, v any) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// Off by default: nothing owner-shaped in any existing render.
func TestOwnerStampOffByDefault(t *testing.T) {
	for _, g := range []string{
		"golden/observability-emitters/minimal.yaml",
		"golden/observability-emitters/everything.yaml",
		"golden/platform-alerts/everything.yaml",
		"golden/observability-stack/everything.yaml",
	} {
		b, err := os.ReadFile(g)
		require.NoError(t, err)
		assert.NotContainsf(t, string(b), "inlineRelabelConfig", g)
		assert.Falsef(t, strings.Contains(string(b), `"owner"`) || strings.Contains(string(b), ", owner,"), g)
	}
}

// platform-alerts: with ownerLabel set every aggregating rule that is about
// a namespaced object keeps the label in its by (...).
func TestOwnerLabelPlatformAlerts(t *testing.T) {
	b, err := os.ReadFile("golden/platform-alerts/owner-label.yaml")
	require.NoError(t, err)
	s := string(b)
	for _, want := range []string{
		"max by (k8s_cluster_name, owner, namespace, cronjob)",
		"max by (k8s_cluster_name, owner, namespace, job_name)",
		"max by (k8s_cluster_name, owner, namespace, pod)",
		"max by (k8s_cluster_name, owner, nodeclaim, nodepool, type)",
	} {
		assert.Contains(t, s, want)
	}
}
