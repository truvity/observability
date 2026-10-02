// The `nodeClaims` kube-state-metrics preset and the platform-alerts group
// that reads it: what the preset renders (the config, and a grant of
// get/list/watch on nodeclaims.karpenter.sh and nothing else), and the
// literal MetricsQL of the two alerts, read back from their goldens.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNodeClaimsPresetRendersConfigAndNarrowGrant(t *testing.T) {
	var cfg string
	var rules []struct {
		APIGroups []string `yaml:"apiGroups"`
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	}
	for _, doc := range splitDocs(t, "golden/observability-emitters/nodeclaims-preset.yaml") {
		var o struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data  map[string]string `yaml:"data"`
			Rules []struct {
				APIGroups []string `yaml:"apiGroups"`
				Resources []string `yaml:"resources"`
				Verbs     []string `yaml:"verbs"`
			} `yaml:"rules"`
		}
		if yaml.Unmarshal(doc, &o) != nil {
			continue
		}
		if o.Kind == "ConfigMap" && strings.HasSuffix(o.Metadata.Name, "-customresourcestate-config") {
			cfg = o.Data["config.yaml"]
		}
		if o.Kind == "ClusterRole" && strings.HasSuffix(o.Metadata.Name, "-nodeclaims") {
			rules = o.Rules
		}
	}
	require.NotEmpty(t, cfg, "no customresourcestate ConfigMap in the golden")

	var c struct {
		Spec struct {
			Resources []struct {
				GVK struct {
					Group, Version, Kind string
				} `yaml:"groupVersionKind"`
				MetricNamePrefix *string             `yaml:"metricNamePrefix"`
				Labels           map[string][]string `yaml:"labelsFromPath"`
				Metrics          []struct {
					Name string `yaml:"name"`
					Each struct {
						Type  string `yaml:"type"`
						Gauge struct {
							Path      []string `yaml:"path"`
							ValueFrom []string `yaml:"valueFrom"`
						} `yaml:"gauge"`
					} `yaml:"each"`
				} `yaml:"metrics"`
			} `yaml:"resources"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(cfg), &c))
	require.Len(t, c.Spec.Resources, 1, "only the nodeClaims preset is on")
	r := c.Spec.Resources[0]
	assert.Equal(t, "karpenter.sh", r.GVK.Group)
	assert.Equal(t, "v1", r.GVK.Version)
	assert.Equal(t, "NodeClaim", r.GVK.Kind)
	require.NotNil(t, r.MetricNamePrefix)
	assert.Equal(t, "", *r.MetricNamePrefix)
	assert.Equal(t, []string{"metadata", "name"}, r.Labels["nodeclaim"])
	assert.Equal(t, []string{"metadata", "labels", "karpenter.sh/nodepool"}, r.Labels["nodepool"])
	require.Len(t, r.Metrics, 1)
	assert.Equal(t, "nodeclaim_status_condition", r.Metrics[0].Name)
	assert.Equal(t, "Gauge", r.Metrics[0].Each.Type)
	assert.Equal(t, []string{"status", "conditions"}, r.Metrics[0].Each.Gauge.Path)
	assert.Equal(t, []string{"status"}, r.Metrics[0].Each.Gauge.ValueFrom)

	require.Len(t, rules, 1, "the grant is one rule")
	assert.Equal(t, []string{"karpenter.sh"}, rules[0].APIGroups)
	assert.Equal(t, []string{"nodeclaims"}, rules[0].Resources)
	assert.Equal(t, []string{"get", "list", "watch"}, rules[0].Verbs)
}

func TestNodeClaimAlertsRenderTheirExpressions(t *testing.T) {
	const notReady = `max by (k8s_cluster_name, nodeclaim, nodepool, type) (` +
		` nodeclaim_status_condition{type=~"Launched|Registered|Initialized"} == 0 )`
	want := map[string]struct{ expr, hold string }{
		"NodeClaimNotReady":      {notReady, "10m"},
		"NodeClaimMetricsAbsent": {absentGuard(`nodeclaim_status_condition{type="Launched"}`, ""), "30m"},
	}
	got := map[string]bool{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/node-claims.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				w, ok := want[r.Alert]
				if !ok {
					continue
				}
				got[r.Alert] = true
				assert.Equal(t, w.expr, strings.Join(strings.Fields(r.Expr), " "), r.Alert)
				assert.Equal(t, w.hold, r.For, r.Alert)
			}
		}
	}
	assert.Len(t, got, len(want))
}
