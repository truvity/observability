// The `cnpg` kube-state-metrics preset and the platform-alerts group that
// reads it: what the preset renders (the config, and a grant of
// get/list/watch on clusters.postgresql.cnpg.io and nothing else), the
// literal MetricsQL of the two alerts read back from their goldens, and
// the `for` behaviour of CNPGClusterNotReady against a Cluster that sits at
// Ready=False for hours, as one did after an automatic failover while every
// instance was up.
package tests

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestCNPGPresetRendersConfigAndNarrowGrant(t *testing.T) {
	var cfg string
	var rules []struct {
		APIGroups []string `yaml:"apiGroups"`
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	}
	for _, doc := range splitDocs(t, "golden/observability-emitters/cnpg-preset.yaml") {
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
		if o.Kind == "ClusterRole" && strings.HasSuffix(o.Metadata.Name, "-cnpg") {
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
							Path      []string            `yaml:"path"`
							Labels    map[string][]string `yaml:"labelsFromPath"`
							ValueFrom []string            `yaml:"valueFrom"`
						} `yaml:"gauge"`
					} `yaml:"each"`
				} `yaml:"metrics"`
			} `yaml:"resources"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(cfg), &c))
	require.Len(t, c.Spec.Resources, 1, "only the cnpg preset is on")
	r := c.Spec.Resources[0]
	assert.Equal(t, "postgresql.cnpg.io", r.GVK.Group)
	assert.Equal(t, "v1", r.GVK.Version)
	assert.Equal(t, "Cluster", r.GVK.Kind)
	require.NotNil(t, r.MetricNamePrefix)
	assert.Equal(t, "", *r.MetricNamePrefix)
	assert.Equal(t, []string{"metadata", "namespace"}, r.Labels["namespace"])
	assert.Equal(t, []string{"metadata", "name"}, r.Labels["cluster"])
	assert.Equal(t, []string{"status", "phase"}, r.Labels["phase"])
	require.Len(t, r.Metrics, 1)
	assert.Equal(t, "cnpg_cluster_status_condition", r.Metrics[0].Name)
	assert.Equal(t, "Gauge", r.Metrics[0].Each.Type)
	assert.Equal(t, []string{"status", "conditions"}, r.Metrics[0].Each.Gauge.Path)
	assert.Equal(t, []string{"type"}, r.Metrics[0].Each.Gauge.Labels["type"])
	assert.Equal(t, []string{"status"}, r.Metrics[0].Each.Gauge.ValueFrom)

	require.Len(t, rules, 1, "the grant is one rule")
	assert.Equal(t, []string{"postgresql.cnpg.io"}, rules[0].APIGroups)
	assert.Equal(t, []string{"clusters"}, rules[0].Resources)
	assert.Equal(t, []string{"get", "list", "watch"}, rules[0].Verbs)
}

func cnpgRules(t *testing.T, golden string) map[string]struct{ expr, hold string } {
	t.Helper()
	got := map[string]struct{ expr, hold string }{}
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				got[r.Alert] = struct{ expr, hold string }{strings.Join(strings.Fields(r.Expr), " "), r.For}
			}
		}
	}
	return got
}

func TestCNPGAlertsRenderTheirExpressions(t *testing.T) {
	const notReady = `max by (k8s_cluster_name, namespace, cluster, phase) (` +
		` cnpg_cluster_status_condition{type="Ready"} == 0 )`
	got := cnpgRules(t, "golden/platform-alerts/cnpg-shared-store.yaml")
	require.Contains(t, got, "CNPGClusterNotReady")
	require.Contains(t, got, "CNPGMetricsAbsent")
	assert.Equal(t, notReady, got["CNPGClusterNotReady"].expr)
	assert.Equal(t, "15m", got["CNPGClusterNotReady"].hold)
	assert.Equal(t, absentGuard(`cnpg_cluster_status_condition{type="Ready"}`, ""), got["CNPGMetricsAbsent"].expr)
	assert.Equal(t, "30m", got["CNPGMetricsAbsent"].hold)

	// At the group's own defaults the rule is the same, severity warning.
	d := cnpgRules(t, "golden/platform-alerts/cnpg.yaml")
	assert.Equal(t, "15m", d["CNPGClusterNotReady"].hold)
}

// A Cluster Ready=False in one phase for ten hours (every instance up, so
// no per-instance metric moves) fires once, 15 minutes in and stays firing;
// a failover that settles inside the hold never fires.
func TestCNPGClusterNotReadyHold(t *testing.T) {
	hold, err := time.ParseDuration(cnpgRules(t, "golden/platform-alerts/cnpg.yaml")["CNPGClusterNotReady"].hold)
	require.NoError(t, err)
	const step = time.Minute

	stuck := vmalertHoldWindow(allTrue(600), hold, step)
	requireFiresAfter(t, stuck, int(hold/step))

	brief := vmalertHoldWindow(untilThen(60, 5), hold, step)
	requireNeverFires(t, brief, "a failover that settled within five minutes must not alert")

	justShort := vmalertHoldWindow(untilThen(60, 14), hold, step)
	requireNeverFires(t, justShort, "Ready back before the hold elapsed must not alert")
}
