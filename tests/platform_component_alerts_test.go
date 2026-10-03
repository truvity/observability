// The platform-alerts groups for Argo CD, External Secrets Operator and AWS
// Controllers for Kubernetes, and the Kargo controller rules: every alert
// with the exact MetricsQL, hold time, severity and the annotations an
// on-call reads, read back from the goldens.
//
// The metric names come from each component's own metrics endpoint, and the
// expressions were run against a real single-node VictoriaMetrics with
// hand-built healthy and broken series (the healthy ones, a failure older than
// the window, an isolated error, another namespace's series and a stage that
// is Progressing must stay silent; the broken ones fire, and an `absent` rule
// fires only once its series is gone).
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type alertRow struct{ expr, hold, severity string }

// groupAlerts reads one group's alerts out of a golden, asserting the
// annotations every alert must carry.
func groupAlerts(t *testing.T, golden, group string) map[string]alertRow {
	t.Helper()
	out := map[string]alertRow{}
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			if g.Name != group {
				continue
			}
			for _, r := range g.Rules {
				out[r.Alert] = alertRow{strings.Join(strings.Fields(r.Expr), " "), r.For, r.Labels["severity"]}
				assert.NotEmpty(t, r.Annotations["summary"], r.Alert)
				assert.NotEmpty(t, r.Annotations["description"], r.Alert)
				if !strings.HasSuffix(golden, "kargo-defaults.yaml") { // that case sets no runbookBaseUrl
					assert.Equal(t, "https://runbooks.example.com/"+r.Alert, r.Annotations["runbook_url"], r.Alert)
				}
			}
		}
	}
	return out
}

func labelsOf(t *testing.T, golden, group, alert string) map[string]string {
	t.Helper()
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				if g.Name == group && r.Alert == alert {
					return r.Labels
				}
			}
		}
	}
	t.Fatalf("no %s in %s", alert, group)
	return nil
}

// absentGuard is the expression `platform-alerts.absentGuard` renders for a
// non-empty clusterLabel and the default `absentLookback` of 1d: per cluster
// (and per `by` label) a series that was there within the lookback and is
// not now, or the whole-store absent() when it never existed in the window.
func absentGuard(sel, cur string, by ...string) string {
	l := strings.Join(append([]string{"k8s_cluster_name"}, by...), ", ")
	lsel := strings.TrimSuffix(sel, "}") + `, k8s_cluster_name!=""}`
	return `(group by (` + l + `) (max_over_time(` + lsel + `[1d])) unless group by (` + l + `) (` + lsel + cur + `)) or ` +
		`(absent(` + sel + cur + `) unless on() group(max_over_time(` + sel + `[1d])))`
}

func TestArgoCDAlertsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	want := map[string]alertRow{
		"ArgoCDAppSyncFailed": {`sum by (` + c + `, name, project, dest_server, phase) (` +
			` increase_pure(argocd_app_sync_total{job=~"argocd-.*-metrics", phase=~"Failed|Error"}[10m]) ) > 0`, "1m", "warning"},
		"ArgoCDAppUnhealthy": {`max by (` + c + `, name, project, dest_server, health_status) (` +
			` argocd_app_info{job=~"argocd-.*-metrics", health_status=~"Degraded|Missing|Unknown"} )`, "15m", "warning"},
		"ArgoCDAppOutOfSync": {`max by (` + c + `, name, project, dest_server) (` +
			` argocd_app_info{job=~"argocd-.*-metrics", sync_status="OutOfSync"} )`, "30m", "warning"},
		"ArgoCDClusterConnectionLost": {`min by (` + c + `, server) (argocd_cluster_connection_status{job=~"argocd-.*-metrics"}) < 1`, "5m", "critical"},
		"ArgoCDGitFetchFailing": {`sum by (` + c + `, repo) (` +
			` increase_pure(argocd_git_fetch_fail_total{job=~"argocd-.*-metrics"}[10m]) ) >= 3`, "5m", "warning"},
		"ArgoCDMetricsAbsent": {absentGuard(`argocd_app_info{job=~"argocd-.*-metrics"}`, ""), "15m", "warning"},
	}
	assert.Equal(t, want, groupAlerts(t, "golden/platform-alerts/argocd.yaml", "platform-alerts.argocd"))
}

func TestESOAlertsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	want := map[string]alertRow{
		"ESOWebhookDown":   {`max by (` + c + `, pod) (up{namespace="external-secrets", job=~"external-secrets-webhook.*"}) == 0`, "5m", "critical"},
		"ESOWebhookAbsent": {absentGuard(`up{namespace="external-secrets", job=~"external-secrets-webhook.*"}`, ""), "5m", "critical"},
		"ESOExternalSecretNotReady": {`max by (` + c + `, exported_namespace, name) (` +
			` externalsecret_status_condition{condition="Ready", status="False"} ) == 1`, "15m", "warning"},
		"ESOSecretStoreNotReady": {`max by (` + c + `, exported_namespace, name) (` +
			` secretstore_status_condition{condition="Ready", status="False"} ) == 1`, "10m", "warning"},
		"ESOClusterSecretStoreNotReady": {`max by (` + c + `, name) (` +
			` clustersecretstore_status_condition{condition="Ready", status="False"} ) == 1`, "10m", "warning"},
		"ESOReconcileErrors": {`sum by (` + c + `, controller) (` +
			` increase(controller_runtime_reconcile_errors_total{namespace="external-secrets"}[15m]) ) > 0`, "15m", "warning"},
		"ESOProviderAPIErrors": {`sum by (` + c + `, provider) (` +
			` increase(externalsecret_provider_api_calls_count{status="error"}[10m]) ) > 0`, "10m", "warning"},
		"ESOWorkqueueStuck": {`max by (` + c + `, name) (workqueue_depth{namespace="external-secrets"}) > 0`, "30m", "warning"},
		"ESOMetricsAbsent":  {absentGuard(`up{namespace="external-secrets"}`, ""), "15m", "warning"},
	}
	assert.Equal(t, want, groupAlerts(t, "golden/platform-alerts/eso.yaml", "platform-alerts.eso"))
}

func TestACKAlertsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	want := map[string]alertRow{
		"ACKControllerDown":   {`max by (` + c + `, namespace, pod) (up{namespace=~"ack-.*"}) == 0`, "5m", "critical"},
		"ACKControllerAbsent": {absentGuard(`up{namespace=~"ack-.*"}`, "", "namespace"), "10m", "critical"},
		"ACKReconcileErrors": {`sum by (` + c + `, namespace, controller) (` +
			` increase(controller_runtime_reconcile_errors_total{namespace=~"ack-.*"}[15m]) ) > 0`, "15m", "warning"},
		"ACKTerminalReconcileErrors": {`sum by (` + c + `, namespace, controller) (` +
			` increase(controller_runtime_terminal_reconcile_errors_total{namespace=~"ack-.*"}[1h]) ) > 0`, "1m", "warning"},
		"ACKReconcilePanics": {`sum by (` + c + `, namespace, controller) (` +
			` increase(controller_runtime_reconcile_panics_total{namespace=~"ack-.*"}[15m]) ) > 0`, "1m", "warning"},
	}
	assert.Equal(t, want, groupAlerts(t, "golden/platform-alerts/ack.yaml", "platform-alerts.ack"))

	// Naming the controllers' namespaces turns the deadman into one `absent`
	// each, so a missing controller among several is seen; the down and error
	// rules still read the selector.
	named := groupAlerts(t, "golden/platform-alerts/ack-expected-namespaces.yaml", "platform-alerts.ack")
	const union = `up{namespace=~"example-ack-one|example-ack-two"}`
	clause := func(ns string) string {
		return `(label_replace(group by (k8s_cluster_name) (max_over_time(` + strings.TrimSuffix(union, "}") + `, k8s_cluster_name!=""}[1d])), "namespace", "` + ns + `", "", "") ` +
			`unless group by (k8s_cluster_name, namespace) (up{namespace="` + ns + `", k8s_cluster_name!=""})) or ` +
			`(absent(up{namespace="` + ns + `"}) unless on() group(max_over_time(` + union + `[1d])))`
	}
	assert.Equal(t, clause("example-ack-one")+` or `+clause("example-ack-two"), strings.Join(strings.Fields(named["ACKControllerAbsent"].expr), " "))
	assert.Contains(t, named["ACKControllerDown"].expr, `namespace=~"example-ack-.*"`)
}

func TestKargoControllerRulesAreOffUntilSwitchedOn(t *testing.T) {
	const c = "k8s_cluster_name"
	// The group alone, at its defaults, renders only the three it always did.
	def := groupAlerts(t, "golden/platform-alerts/kargo-defaults.yaml", "platform-alerts.kargo")
	assert.Len(t, def, 3)
	for _, a := range []string{"KargoControllerAbsent", "KargoControllerReconcileErrors", "KargoControllerWorkqueueStuck"} {
		assert.NotContains(t, def, a, "a controller rule is off by default")
	}

	got := groupAlerts(t, "golden/platform-alerts/kargo-controller.yaml", "platform-alerts.kargo")
	assert.Len(t, got, 6)
	assert.Equal(t, alertRow{absentGuard(`up{job="kargo-controller-metrics"}`, " == 1"), "5m", "critical"}, got["KargoControllerAbsent"])
	assert.Equal(t, alertRow{`sum by (` + c + `, controller) (` +
		` increase(controller_runtime_reconcile_errors_total{job="kargo-controller-metrics"}[15m]) ) > 0`, "15m", "warning"},
		got["KargoControllerReconcileErrors"])
	assert.Equal(t, alertRow{`max by (` + c + `, name) (workqueue_depth{job="kargo-controller-metrics"}) > 0`, "30m", "warning"},
		got["KargoControllerWorkqueueStuck"])
}

// With `keepClusterLabel` the series' own cluster label survives on every
// rule that aggregates by it, and the common label is left off. The `absent`
// guards are per cluster and keep their own cluster label too, whatever
// `keepClusterLabel` says: the common label would stamp every cluster with
// the store's. Every value in this
// case is off its default, so the golden also shows each one reaches the rule.
func TestPlatformComponentAlertsKeepTheSeriesClusterLabel(t *testing.T) {
	const g = "golden/platform-alerts/platform-components-shared-store.yaml"
	for _, tc := range []struct{ group, alert string }{
		{"platform-alerts.argocd", "ArgoCDAppUnhealthy"},
		{"platform-alerts.argocd", "ArgoCDClusterConnectionLost"},
		{"platform-alerts.eso", "ESOExternalSecretNotReady"},
		{"platform-alerts.eso", "ESOWebhookDown"},
		{"platform-alerts.ack", "ACKControllerDown"},
		{"platform-alerts.ack", "ACKReconcileErrors"},
		{"platform-alerts.kargo", "KargoControllerReconcileErrors"},
		{"platform-alerts.argocd", "ArgoCDMetricsAbsent"},
		{"platform-alerts.eso", "ESOWebhookAbsent"},
		{"platform-alerts.eso", "ESOMetricsAbsent"},
		{"platform-alerts.ack", "ACKControllerAbsent"},
		{"platform-alerts.kargo", "KargoControllerAbsent"},
	} {
		assert.NotContains(t, labelsOf(t, g, tc.group, tc.alert), "k8s_cluster_name", tc.alert)
	}

	ack := groupAlerts(t, g, "platform-alerts.ack")
	require.Contains(t, ack, "ACKTerminalReconcileErrors")
	assert.Equal(t, alertRow{`sum by (k8s_cluster_name, namespace, controller) (` +
		` increase(controller_runtime_terminal_reconcile_errors_total{namespace=~"example-ack-.*"}[2h]) ) > 0`, "3m", "info"},
		ack["ACKTerminalReconcileErrors"])
	argo := groupAlerts(t, g, "platform-alerts.argocd")
	assert.NotContains(t, argo, "ArgoCDAppOutOfSync", "outOfSync.enabled: false drops the rule")
	assert.Equal(t, `sum by (k8s_cluster_name, repo) ( increase_pure(argocd_git_fetch_fail_total{job=~"example-argocd-.*"}[20m]) ) >= 5`,
		argo["ArgoCDGitFetchFailing"].expr)
	assert.Equal(t, "warning", argo["ArgoCDClusterConnectionLost"].severity, "the group's `severity` moved")
}
