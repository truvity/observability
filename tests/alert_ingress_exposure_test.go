package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func alertIngressDocs(t *testing.T, golden string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}

	for _, raw := range splitDocs(t, filepath.Join("golden", "alert-ingress", golden)) {
		var d map[string]any
		require.NoError(t, yaml.Unmarshal(raw, &d))

		if kind, _ := d["kind"].(string); kind != "" {
			out[kind] = d
		}
	}

	return out
}

func containerOf(t *testing.T, deploy map[string]any) map[string]any {
	t.Helper()

	spec := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

	return spec["containers"].([]any)[0].(map[string]any)
}

// By default /metrics and /healthz stay on the webhook port, so an
// existing install renders as before.
func TestAlertIngressDefaultKeepsOneListener(t *testing.T) {
	docs := alertIngressDocs(t, "minimal.yaml")
	c := containerOf(t, docs["Deployment"])

	assert.Len(t, c["ports"], 1)
	assert.NotContains(t, c["args"], "--metrics-addr=:9091")
}

// With service.metricsPort set, the metrics port is a container port only:
// the Service lists the webhook port alone, so no route to the Service can
// reach /metrics, and the probes use the metrics port.
func TestAlertIngressSeparateMetricsPortIsNotInTheService(t *testing.T) {
	docs := alertIngressDocs(t, "separate-metrics.yaml")

	svcPorts := docs["Service"]["spec"].(map[string]any)["ports"].([]any)
	require.Len(t, svcPorts, 1)
	assert.Equal(t, "http", svcPorts[0].(map[string]any)["name"])

	c := containerOf(t, docs["Deployment"])
	assert.Contains(t, c["args"], "--metrics-addr=:9091")
	assert.Len(t, c["ports"], 2)

	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		hg := c[probe].(map[string]any)["httpGet"].(map[string]any)
		assert.Equal(t, "metrics", hg["port"], probe)
	}

	pm := docs["PodMonitor"]["spec"].(map[string]any)["podMetricsEndpoints"].([]any)
	assert.Equal(t, "metrics", pm[0].(map[string]any)["port"])
}

// Each VMRule group can be switched off; the default renders both.
func TestAlertIngressRuleGroupsAreToggleable(t *testing.T) {
	groups := func(golden string) []string {
		var names []string

		for _, g := range alertIngressDocs(t, golden)["VMRule"]["spec"].(map[string]any)["groups"].([]any) {
			names = append(names, g.(map[string]any)["name"].(string))
		}

		return names
	}

	assert.Equal(t, []string{"alert-ingress.heartbeat", "alert-ingress.rejected"}, groups("minimal.yaml"))
	assert.Equal(t, []string{"alert-ingress.heartbeat"}, groups("separate-metrics.yaml"))
}
