// The platform-alerts pulumiDrift group: three alerts over two gauges that a
// scheduled job pushes once per run. The expressions read the latest sample
// over `lookback` (an instant selector would see nothing between pushes), and
// every aggregation keeps the cluster label.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			`time() - max by (` + c + `, scope, stack) (tlast_over_time(pulumi_stack_drift_changes[7d])) > 36h`, "", "warning"},
	}
	assert.Equal(t, want, groupAlerts(t, g, "platform-alerts.pulumi-drift"))
}
