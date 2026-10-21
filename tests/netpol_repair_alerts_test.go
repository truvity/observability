// The platform-alerts `netpolRepair` group: every alert it renders, with the
// exact MetricsQL, hold time, severity and the annotations an on-call reads.
//
// The metric names come from the controller that exports them:
// truvity/k8s cmd/netpol-stuck-pod-repair (metrics.go), a TEMPORARY
// workaround for https://github.com/aws/aws-network-policy-agent/issues/676.
// values.yaml lists them. Every annotation must say it is the workaround and
// link the upstream issue and fix, so the alert carries its own removal
// condition.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNetpolRepairAlertsRenderTheirExpressions(t *testing.T) {
	const by = "k8s_cluster_name, exported_namespace, owner_kind, owner"
	want := map[string]struct{ expr, hold, severity string }{
		"NetpolStuckPodRepaired": {
			`sum by (` + by + `, reason) ( increase(netpol_repair_actions_total{mode="act"}[10m]) ) > 0` +
				` or sum by (` + by + `, reason) ( netpol_repair_actions_total{mode="act"}` +
				` unless netpol_repair_actions_total{mode="act"} offset 10m )`, "", "warning"},
		"NetpolStuckPodRepairHalted": {
			`max by (k8s_cluster_name, namespace, pod) (netpol_repair_halted) == 1`, "1m", "critical"},
		"NetpolStuckPodCandidate": {
			`max by (` + by + `) (netpol_repair_candidates) > 0`, "15m", "warning"},
	}

	got := map[string]bool{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/netpol-repair.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			require.Equal(t, "platform-alerts.netpol-repair", g.Name)
			for _, r := range g.Rules {
				w, ok := want[r.Alert]
				require.Truef(t, ok, "unexpected alert %s", r.Alert)
				got[r.Alert] = true
				assert.Equal(t, w.expr, strings.Join(strings.Fields(r.Expr), " "), r.Alert)
				assert.Equal(t, w.hold, r.For, r.Alert)
				assert.Equal(t, w.severity, r.Labels["severity"], r.Alert)
				assert.NotEmpty(t, r.Annotations["summary"], r.Alert)
				assert.Contains(t, r.Annotations["description"], "TEMPORARY", r.Alert)
				assert.Contains(t, r.Annotations["description"], "https://github.com/aws/aws-network-policy-agent/issues/676", r.Alert)
				assert.Contains(t, r.Annotations["description"], "https://github.com/aws/aws-network-policy-agent/pull/705", r.Alert)
				assert.Equal(t, "https://github.com/aws/aws-network-policy-agent/issues/676", r.Annotations["upstream_issue"], r.Alert)
				assert.Equal(t, "https://github.com/aws/aws-network-policy-agent/pull/705", r.Annotations["upstream_fix"], r.Alert)
				assert.Equal(t, "https://runbooks.example.com/"+r.Alert, r.Annotations["runbook_url"], r.Alert)
			}
		}
	}
	assert.Len(t, got, len(want))
}
