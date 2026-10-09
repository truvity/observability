// The platform-alerts pulumiDrift group: three alerts over two gauges that a
// scheduled job pushes once per run. The expressions read the latest sample
// over `lookback` (an instant selector would see nothing between pushes), and
// every aggregation keeps the cluster label.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.yaml.in/yaml/v3"
)

func TestPulumiDriftRendersItsExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	const g = "golden/platform-alerts/pulumi-drift.yaml"
	want := map[string]alertRow{
		"PulumiDriftDetected": {
			`max by (` + c + `, scope, stack) (last_over_time(pulumi_stack_drift_changes[36h])) > 0`, "2d", "warning"},
		"PulumiDiffFailing": {
			`max by (` + c + `, scope, stack) (last_over_time(pulumi_stack_diff_error[36h])) > 0`, "2d", "warning"},
		"PulumiDriftSignalStale": {
			`time() - max by (` + c + `, scope, stack) (tlast_over_time(pulumi_stack_diff_error[7d])) > 36h`, "", "warning"},
	}
	assert.Equal(t, want, groupAlerts(t, g, "platform-alerts.pulumi-drift"))
}

func TestPulumiDriftRunbookUrlOverridesTheBaseUrl(t *testing.T) {
	seen := 0
	for _, doc := range splitDocs(t, "golden/platform-alerts/pulumi-drift-runbook.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				seen++
				assert.Equal(t, "https://docs.example.com/pulumi-drift", r.Annotations["runbook_url"], r.Alert)
			}
		}
	}
	assert.Equal(t, 3, seen)
	assert.Equal(t, "3d", groupAlerts2(t)["PulumiDriftDetected"].hold)
}

// groupAlerts asserts the base-URL runbook on every alert, which this case
// deliberately overrides, so the rows are read without that assertion.
func groupAlerts2(t *testing.T) map[string]alertRow {
	t.Helper()
	out := map[string]alertRow{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/pulumi-drift-runbook.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				out[r.Alert] = alertRow{strings.Join(strings.Fields(r.Expr), " "), r.For, r.Labels["severity"]}
			}
		}
	}
	return out
}
