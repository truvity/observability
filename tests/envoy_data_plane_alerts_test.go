// The two Envoy data-plane fault alerts of platform-alerts' envoyRoutes
// group: EnvoyUpstreamConnectFailures (one proxy pod cannot connect to one
// backend pod) and EnvoyOutlierEjections (the proxy keeps ejecting an
// endpoint).
//
// The fault they exist for: after a node was reclaimed, one proxy pod could
// not connect to one backend pod. Its counter of failed connects rose and
// nothing else did: the route's 5xx ratio was a fraction of a fraction and
// held under the ratio rules' traffic floor, and the Envoy cluster still had
// healthy members on the other replicas. These rules have no traffic floor
// and read the counter of the failed connects itself.
//
// Each rule's exact MetricsQL, hold and severity are read back from the
// goldens. What the expression computes is modelled the way
// tests/kargo_alerts_test.go models vmalert's `for:`: `increase()` over a
// per-minute counter, compared with the threshold, then driven through
// vmalertHoldWindow. The model proves the thresholds and holds against the
// shape of the fault; it does not evaluate MetricsQL.
package tests

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type envoyRule struct {
	expr, hold string
	labels     map[string]string
}

// envoyRules returns every rule of the envoy-routes group named alert, in
// render order (the connect-failure rule is rendered once per severity when
// `criticalClusters` is set).
func envoyRules(t *testing.T, golden, alert string) []envoyRule {
	t.Helper()
	var out []envoyRule
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			if g.Name != "platform-alerts.envoy-routes" {
				continue
			}
			for _, r := range g.Rules {
				if r.Alert != alert {
					continue
				}
				assert.NotEmpty(t, r.Annotations["summary"], alert)
				assert.NotEmpty(t, r.Annotations["description"], alert)
				assert.Equal(t, "https://runbooks.example.com/"+alert, r.Annotations["runbook_url"], alert)
				out = append(out, envoyRule{strings.Join(strings.Fields(r.Expr), " "), r.For, r.Labels})
			}
		}
	}
	return out
}

func TestEnvoyUpstreamConnectFailuresRenders(t *testing.T) {
	const c = "k8s_cluster_name"
	expr := func(matcher string) string {
		return `sum by (` + c + `, namespace, pod, envoy_cluster_name) (increase(envoy_cluster_upstream_cx_connect_fail{` +
			`job=~"envoy-gateway-system/envoy-proxy", envoy_cluster_name=~"(http|tls|grpc|tcp)route/.*"` + matcher + `}[5m])) > 3`
	}

	// Defaults: one rule, warning everywhere, no traffic floor in it.
	def := envoyRules(t, "golden/platform-alerts/generic-gaps.yaml", "EnvoyUpstreamConnectFailures")
	require.Len(t, def, 1)
	assert.Equal(t, expr(""), def[0].expr)
	assert.Equal(t, "5m", def[0].hold)
	assert.Equal(t, "warning", def[0].labels["severity"])
	assert.NotContains(t, def[0].expr, "envoy_cluster_upstream_rq", "no traffic floor")

	// The shared-store render keeps the cluster label in the result, so
	// the alert says which cluster the proxy pod is in.
	shared := envoyRules(t, "golden/platform-alerts/generic-gaps-shared-store.yaml", "EnvoyUpstreamConnectFailures")
	require.Len(t, shared, 1)
	assert.NotContains(t, shared[0].labels, c)

	// criticalClusters: one rule per side of the split, critical on the
	// clusters it names, warning on every other.
	split := envoyRules(t, "golden/platform-alerts/envoy-connect-failures.yaml", "EnvoyUpstreamConnectFailures")
	require.Len(t, split, 2)
	assert.Equal(t, expr(`, `+c+`=~"^(production)$"`), split[0].expr)
	assert.Equal(t, "critical", split[0].labels["severity"])
	assert.Equal(t, expr(`, `+c+`!~"^(production)$"`), split[1].expr)
	assert.Equal(t, "warning", split[1].labels["severity"])
	for _, r := range split {
		assert.Equal(t, "5m", r.hold)
	}
}

func TestEnvoyOutlierEjectionsRenders(t *testing.T) {
	const c = "k8s_cluster_name"
	want := `sum by (` + c + `, namespace, pod, envoy_cluster_name) (increase(envoy_cluster_outlier_detection_ejections_enforced_total{` +
		`job=~"envoy-gateway-system/envoy-proxy", envoy_cluster_name=~"(http|tls|grpc|tcp)route/.*"}[10m])) > 0`
	got := envoyRules(t, "golden/platform-alerts/generic-gaps.yaml", "EnvoyOutlierEjections")
	require.Len(t, got, 1)
	assert.Equal(t, want, got[0].expr)
	assert.Equal(t, "10m", got[0].hold)
	assert.Equal(t, "warning", got[0].labels["severity"])
}

func TestEnvoyDataPlaneAlertsCanBeSwitchedOff(t *testing.T) {
	const g = "golden/platform-alerts/envoy-connect-failures-off.yaml"
	assert.Empty(t, envoyRules(t, g, "EnvoyUpstreamConnectFailures"))
	assert.Empty(t, envoyRules(t, g, "EnvoyOutlierEjections"))
	// The rest of the group is untouched.
	assert.NotEmpty(t, envoyRules(t, g, "EnvoyRoute5xxRatioHigh"))
}

// counter builds a per-minute counter from the number of events in each
// minute, starting at zero.
func counter(perMinute []int) []int {
	c := make([]int, len(perMinute))
	total := 0
	for i, n := range perMinute {
		total += n
		c[i] = total
	}
	return c
}

// increaseAbove models `increase(counter[window]) > threshold` evaluated
// every minute: the counter now minus the counter `window` minutes ago (a
// series that began at zero before the first sample).
func increaseAbove(c []int, window, threshold int) []bool {
	out := make([]bool, len(c))
	for i := range c {
		prev := 0
		if i-window >= 0 {
			prev = c[i-window]
		}
		out[i] = c[i]-prev > threshold
	}
	return out
}

func spread(n, total int) []int {
	// total events spread evenly over n minutes
	out := make([]int, n)
	for i := 0; i < total; i++ {
		out[i*n/total]++
	}
	return out
}

func holdOf(t *testing.T, rules []envoyRule) time.Duration {
	t.Helper()
	require.NotEmpty(t, rules)
	d, err := time.ParseDuration(rules[0].hold)
	require.NoError(t, err)
	return d
}

func TestEnvoyUpstreamConnectFailuresFiresOnTheFault(t *testing.T) {
	hold := holdOf(t, envoyRules(t, "golden/platform-alerts/generic-gaps.yaml", "EnvoyUpstreamConnectFailures"))
	const step = time.Minute

	// The fault: 121 failed connects and no success, over 85 minutes, then
	// the pod goes on failing. It fires once the hold has passed since the
	// counter first moved past the threshold and does not stop: there is no
	// traffic floor to reset it.
	fault := append(spread(85, 121), make([]int, 35)...)
	for i := 85; i < len(fault); i++ {
		fault[i] = 1
	}
	firing := vmalertHoldWindow(increaseAbove(counter(fault), 5, 3), hold, step)
	var first = -1
	for i, f := range firing {
		if f {
			first = i
			break
		}
	}
	require.NotEqual(t, -1, first, "the fault never fired")
	assert.Less(t, first, 15, "the fault must page within minutes, not an hour")
	for i := first; i < len(firing); i++ {
		assert.Truef(t, firing[i], "minute %d: the alert dropped while the pod kept failing", i)
	}

	// A steady 1 per minute (5 per window) fires.
	steady := vmalertHoldWindow(increaseAbove(counter(spread(30, 30)), 5, 3), hold, step)
	assert.True(t, steady[len(steady)-1])
}

func TestEnvoyUpstreamConnectFailuresStaysQuietOnNoise(t *testing.T) {
	hold := holdOf(t, envoyRules(t, "golden/platform-alerts/generic-gaps.yaml", "EnvoyUpstreamConnectFailures"))
	const step = time.Minute

	one := func(at, n int) []int {
		m := make([]int, 60)
		m[at] = n
		return m
	}
	// Exactly the threshold (3) once is not above it.
	requireNeverFires(t, vmalertHoldWindow(increaseAbove(counter(one(10, 3)), 5, 3), hold, step),
		"three failed connects, once, must not alert")
	// A burst that crosses the threshold, once: above it for one window
	// (5 minutes), which is shorter than the hold.
	requireNeverFires(t, vmalertHoldWindow(increaseAbove(counter(one(10, 40)), 5, 3), hold, step),
		"a single burst of failed connects must not alert")
	// A trickle of one failure every three minutes: under 3 per window.
	trickle := make([]int, 120)
	for i := 0; i < len(trickle); i += 3 {
		trickle[i] = 1
	}
	requireNeverFires(t, vmalertHoldWindow(increaseAbove(counter(trickle), 5, 3), hold, step),
		"a failure every three minutes must not alert")
	// A healthy pod: the counter never moves.
	requireNeverFires(t, vmalertHoldWindow(increaseAbove(counter(make([]int, 120)), 5, 3), hold, step),
		"a flat counter must not alert")
}

func TestEnvoyOutlierEjectionsHold(t *testing.T) {
	hold := holdOf(t, envoyRules(t, "golden/platform-alerts/generic-gaps.yaml", "EnvoyOutlierEjections"))
	const step = time.Minute

	// One ejection, once, is the proxy doing its job: the window it sits in
	// (10 minutes) is shorter than the hold.
	m := make([]int, 90)
	m[20] = 1
	requireNeverFires(t, vmalertHoldWindow(increaseAbove(counter(m), 10, 0), hold, step),
		"a single ejection must not alert")

	// An endpoint that is ejected again every five minutes for an hour keeps
	// the counter moving in every window: the alert fires after the hold.
	rep := make([]int, 90)
	for i := 20; i < 80; i += 5 {
		rep[i] = 1
	}
	firing := vmalertHoldWindow(increaseAbove(counter(rep), 10, 0), hold, step)
	fired := false
	for _, f := range firing {
		fired = fired || f
	}
	assert.True(t, fired, "repeated ejections must alert")
	assert.False(t, firing[len(firing)-1], "the alert clears once the ejections stop")
}
