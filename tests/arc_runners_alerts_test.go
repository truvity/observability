// The platform-alerts `arcRunners` group: every alert it renders, with the
// exact MetricsQL, hold time, severity and the annotations an on-call reads.
//
// The metric names come from the sources that export them, not from a guess:
// the runner-watchdog's /metrics (ci-plane, image/runner-watchdog/http.go)
// and the ARC listener (actions/actions-runner-controller,
// cmd/ghalistener/metrics/metrics.go at gha-runner-scale-set-0.14.2 and
// 0.15.0, which export the same names). values.yaml lists them.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestARCRunnersAlertsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	want := map[string]struct{ expr, hold, severity string }{
		"ARCRunnerStuck": {
			`max by (` + c + `, namespace, pod) (runner_watchdog_stuck) == 1`, "5m", "warning"},
		"ARCRunnerStuckDetectionsHigh": {
			`sum by (` + c + `, namespace) (increase(runner_watchdog_stuck_detections_total[1h])) > 10`, "5m", "info"},
		"ARCJobsWaiting": {
			`max by (` + c + `, pod, name) (gha_assigned_jobs) - max by (` + c + `, pod, name) (gha_running_jobs) > 0`, "5m", "warning"},
		"ARCJobStartupSlow": {
			`histogram_quantile(0.95, sum by (` + c + `, pod, le) (increase(gha_job_startup_duration_seconds_bucket[30m]))) > 300` +
				` and sum by (` + c + `, pod) (increase(gha_job_startup_duration_seconds_count[30m])) >= 3`, "10m", "warning"},
	}

	got := map[string]bool{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/arc-runners.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			require.Equal(t, "platform-alerts.arc-runners", g.Name)
			for _, r := range g.Rules {
				w, ok := want[r.Alert]
				require.Truef(t, ok, "unexpected alert %s", r.Alert)
				got[r.Alert] = true
				assert.Equal(t, w.expr, strings.Join(strings.Fields(r.Expr), " "), r.Alert)
				assert.Equal(t, w.hold, r.For, r.Alert)
				assert.Equal(t, w.severity, r.Labels["severity"], r.Alert)
				assert.NotEmpty(t, r.Annotations["summary"], r.Alert)
				assert.NotEmpty(t, r.Annotations["description"], r.Alert)
				assert.Equal(t, "https://runbooks.example.com/"+r.Alert, r.Annotations["runbook_url"], r.Alert)
			}
		}
	}
	assert.Len(t, got, len(want))
}
