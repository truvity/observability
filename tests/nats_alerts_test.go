// The platform-alerts `nats` group: every alert it renders, with the exact
// MetricsQL, hold time, severity and the annotations an on-call reads.
//
// The metric names in these expressions were read off a live store that
// scrapes prometheus-nats-exporter 0.20.x (`-varz -connz -routez -jsz`),
// not guessed; values.yaml lists them. The expressions were also run
// against a real single-node VictoriaMetrics with hand-built healthy and
// broken series (see the pull request that added the group).
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNATSAlertsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	want := map[string]struct{ expr, hold, severity string }{
		"NATSBrokerDown": {
			`max by (` + c + `, pod) (up{job="nats/nats"}) == 0`, "5m", "critical"},
		"NATSClusterRoutesMissing": {
			`max by (` + c + `, pod) (nats_varz_routes{job="nats/nats"}) < (` +
				` max by (` + c + `, pod) (nats_varz_jetstream_meta_cluster_size{job="nats/nats"} - 1) *` +
				` max by (` + c + `, pod) (nats_varz_cluster_pool_size{job="nats/nats"}) )`, "10m", "warning"},
		"NATSJetStreamNoMetaLeader": {
			`max by (` + c + `, pod) (nats_server_total_streams{job="nats/nats", meta_leader=""}) or` +
				` max by (` + c + `, pod) (nats_varz_jetstream_meta_leader{job="nats/nats", value=""})`, "5m", "critical"},
		"NATSStreamNoLeader": {
			`max by (` + c + `, account, stream_name) (nats_stream_total_messages{job="nats/nats", stream_leader=""})`, "5m", "critical"},
		"NATSConsumerBacklogGrowing": {
			`max by (` + c + `, account, stream_name, consumer_name) (` +
				` (delta(nats_consumer_num_pending{job="nats/nats"}[15m]) > 0) and` +
				` (min_over_time(nats_consumer_num_pending{job="nats/nats"}[15m]) > 100) )`, "5m", "warning"},
		"NATSSlowConsumers": {
			`max by (` + c + `, pod) (increase(nats_varz_slow_consumers{job="nats/nats"}[10m])) > 0`, "5m", "warning"},
		"NATSJetStreamStorageHigh": {
			`max by (` + c + `, pod) (nats_varz_jetstream_stats_storage{job="nats/nats"}) /` +
				` (max by (` + c + `, pod) (nats_varz_jetstream_config_max_storage{job="nats/nats"}) > 0) > 0.8`, "15m", "warning"},
		"NATSMemoryNearLimit": {
			`max by (` + c + `, pod) (container_memory_working_set_bytes{namespace="nats", container="nats"}) /` +
				` max by (` + c + `, pod) (kube_pod_container_resource_limits{namespace="nats", container="nats", resource="memory"}) > 0.9`, "10m", "warning"},
		"NATSMetricsAbsent": {
			`absent(up{job="nats/nats"})`, "30m", "warning"},
	}

	got := map[string]bool{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/nats.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			require.Equal(t, "platform-alerts.nats", g.Name)
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
