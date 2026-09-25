// The chart refuses these same shapes at render time (templates/_validate.tpl,
// once written) — but this binary reads a ConfigMap, not a values file,
// and a validated chart three steps upstream says nothing about the
// bytes actually mounted into THIS pod: an old release rolled back, or a
// ConfigMap edited by hand, reaches this code with no chart in between.
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	return path
}

func TestLoadConfigValid(t *testing.T) {
	path := writeConfig(t, `
alertmanager:
  url: http://vmalertmanager-observability-stack.observability.svc:9093
topics:
  - "<the security-alerts topic ARN>"
mappings:
  - name: guardduty
    match: {"detail-type": "GuardDuty Finding"}
    alert:
      alertname: CloudSecurityFinding
      severity: warning
heartbeat:
  match: {"source": "alert-ingress-heartbeat"}
  interval: 15m
`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "http://vmalertmanager-observability-stack.observability.svc:9093", cfg.Alertmanager.URL)
	assert.Equal(t, 15*time.Minute, time.Duration(cfg.Heartbeat.Interval))
	assert.Equal(t, time.Hour, time.Duration(cfg.ResolveAfter),
		"resolveAfter must default to 1h when the file omits it, matching the chart's own schema default")
}

func TestLoadConfigResolveAfterOverride(t *testing.T) {
	path := writeConfig(t, `
alertmanager: {url: "http://am:9093"}
topics: ["<the security-alerts topic ARN>"]
mappings: []
heartbeat: {match: {"source": "alert-ingress-heartbeat"}, interval: 15m}
resolveAfter: 30m
`)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, time.Duration(cfg.ResolveAfter))
}

// TestLoadConfigRefusals is the same table the design page's refusals
// section describes, checked against the binary's own reading of the
// file rather than trusted from the chart that (should have) produced
// it.
func TestLoadConfigRefusals(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "topics empty is an open subscription endpoint",
			yaml: `
alertmanager: {url: "http://am:9093"}
topics: []
heartbeat: {match: {"source": "x"}, interval: 15m}
`,
			wantErr: "topics is empty",
		},
		{
			name: "heartbeat unset means the path can die unnoticed",
			yaml: `
alertmanager: {url: "http://am:9093"}
topics: ["<the security-alerts topic ARN>"]
heartbeat: {interval: 15m}
`,
			wantErr: "heartbeat.match is empty",
		},
		{
			name: "a mapping with no severity routes to the default tier by accident",
			yaml: `
alertmanager: {url: "http://am:9093"}
topics: ["<the security-alerts topic ARN>"]
heartbeat: {match: {"source": "x"}, interval: 15m}
mappings:
  - name: broken
    match: {"detail-type": "X"}
    alert: {alertname: Example}
`,
			wantErr: "no alert.severity",
		},
		{
			name: "alertmanager.url empty leaves nowhere to post an alert",
			yaml: `
topics: ["<the security-alerts topic ARN>"]
heartbeat: {match: {"source": "x"}, interval: 15m}
`,
			wantErr: "alertmanager.url is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, c.yaml))
			require.Errorf(t, err, "expected a refusal containing %q", c.wantErr)
			assert.Containsf(t, err.Error(), c.wantErr, "wrong refusal for %q", c.name)
		})
	}
}
