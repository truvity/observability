// The platform-alerts groups that close the generic gaps for product
// namespaces: envoyRoutes, certificates, restarts and workloadAbsent, plus
// the backup rules' own namespace selector. Every alert with the exact
// MetricsQL, hold time and severity, read back from the goldens.
//
// The metric names and labels were read off a live store (Envoy Gateway's
// `envoy_cluster_upstream_rq_xx` / `_rq_time_bucket` per `httproute/...`
// cluster, cert-manager's `certmanager_certificate_*` with the Certificate's
// namespace in `exported_namespace`, kube-state-metrics' pod and Deployment
// series), and each expression was evaluated against it before it was written.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenericGapsRenderTheirExpressions(t *testing.T) {
	const c = "k8s_cluster_name"
	const g = "golden/platform-alerts/generic-gaps.yaml"
	const sel = `job=~"envoy-gateway-system/envoy-proxy", envoy_cluster_name=~"httproute/.*"`
	traffic := `sum by (` + c + `, envoy_cluster_name) (rate(envoy_cluster_upstream_rq_xx{` + sel + `}[5m]))`
	ratio := func(th string) string {
		return `sum by (` + c + `, envoy_cluster_name) (rate(envoy_cluster_upstream_rq_xx{` + sel + `, envoy_response_code_class="5"}[5m])) / ` +
			traffic + ` > ` + th + ` and on (` + c + `, envoy_cluster_name) ` + traffic + ` >= 0.1`
	}
	p99 := func(th string) string {
		return `histogram_quantile(0.99, sum by (` + c + `, envoy_cluster_name, le) (rate(envoy_cluster_upstream_rq_time_bucket{` + sel + `}[5m]))) > ` + th +
			` and on (` + c + `, envoy_cluster_name) ` + traffic + ` >= 0.1`
	}
	exp := func(days string) string {
		return `max by (` + c + `, exported_namespace, name) (certmanager_certificate_expiration_timestamp_seconds{exported_namespace=~"^(platform)$"} > 0) - time() < ` + days + ` * 86400`
	}

	cases := map[string]map[string]alertRow{
		"platform-alerts.envoy-routes": {
			"EnvoyRoute5xxRatioHigh":       {ratio("0.05"), "10m", "warning"},
			"EnvoyRoute5xxRatioCritical":   {ratio("0.25"), "10m", "critical"},
			"EnvoyRouteP99LatencyHigh":     {p99("2000"), "10m", "warning"},
			"EnvoyRouteP99LatencyCritical": {p99("10000"), "10m", "critical"},
		},
		"platform-alerts.certificates": {
			"CertificateExpiringSoon":   {exp("14"), "", "warning"},
			"CertificateExpiryCritical": {exp("3"), "", "critical"},
			"CertificateNotReady":       {`max by (` + c + `, exported_namespace, name) (certmanager_certificate_ready_status{exported_namespace=~"^(platform)$", condition="True"}) == 0`, "15m", "warning"},
		},
		"platform-alerts.restarts": {
			"ContainerOOMKilled": {`max by (` + c + `, namespace, pod, container) (kube_pod_container_status_last_terminated_reason{namespace=~"^(product-a)$", reason="OOMKilled"} == 1)` +
				` and on (` + c + `, namespace, pod, container) max by (` + c + `, namespace, pod, container) (increase(kube_pod_container_status_restarts_total{namespace=~"^(product-a)$"}[15m])) > 0`, "", "warning"},
			"ContainerRestartingOften": {`max by (` + c + `, namespace, pod, container) (increase(kube_pod_container_status_restarts_total{namespace=~"^(product-a)$"}[1h])) > 5`, "", "warning"},
		},
		"platform-alerts.workload-absent": {
			"DeploymentNoAvailableReplicas": {`max by (` + c + `, namespace, deployment) (kube_deployment_status_replicas_available{namespace=~"^(product-a|product-b)$"}) == 0` +
				` and on (` + c + `, namespace, deployment) max by (` + c + `, namespace, deployment) (kube_deployment_spec_replicas{namespace=~"^(product-a|product-b)$"}) > 0`, "10m", "critical"},
		},
	}
	for group, want := range cases {
		got := groupAlerts(t, g, group)
		assert.Equal(t, want, got, group)
	}

	// The backup rules read their own selector, not the top-level one.
	for name, a := range groupAlerts(t, g, "platform-alerts.backups") {
		assert.Contains(t, a.expr, `namespace=~"^(platform|product-a)$"`, name)
		assert.NotContains(t, a.expr, `namespace=~"^(platform)$"`, name)
	}
}

func TestGenericGapsKeepTheSeriesClusterLabel(t *testing.T) {
	const g = "golden/platform-alerts/generic-gaps-shared-store.yaml"
	for group, alert := range map[string]string{
		"platform-alerts.envoy-routes":    "EnvoyRoute5xxRatioHigh",
		"platform-alerts.certificates":    "CertificateNotReady",
		"platform-alerts.restarts":        "ContainerOOMKilled",
		"platform-alerts.workload-absent": "DeploymentNoAvailableReplicas",
	} {
		labels := labelsOf(t, g, group, alert)
		require.NotNil(t, labels)
		assert.NotContains(t, labels, "k8s_cluster_name", alert)
		assert.Equal(t, "kernel", labelsOf(t, "golden/platform-alerts/generic-gaps-shared-store.yaml", "platform-alerts.backups", "CronJobNotSucceeding")["k8s_cluster_name"])
	}
}
