// docs/alert-ingress.md asks for a unit fixture per source shape: a
// finding, a sign-in, a budget notification, an alarm state change, and
// the heartbeat. The first three have a mapping rule of their own in the
// design page's own example values; the fourth has none, which is
// deliberate — it is the fixture that proves the unmapped path, because
// an alarm state change is a real, well-formed shape this service was
// simply never told about. The heartbeat is covered in handler_test.go,
// where it is a request-level behaviour (recognised, counted, never
// posted) rather than a template to render.
package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exampleMappings mirrors the three mapping rules in docs/alert-ingress.md
// exactly, so a change to that page's own example is the thing that
// breaks these tests rather than a copy drifting from it unnoticed.
func exampleMappings() []Mapping {
	return []Mapping{
		{
			Name:  "guardduty",
			Match: map[string]string{"detail-type": "GuardDuty Finding"},
			Alert: AlertSpec{
				Alertname: "CloudSecurityFinding",
				Severity:  `{{ if ge .detail.severity 7.0 }}critical{{ else }}warning{{ end }}`,
				Labels: map[string]string{
					"source":           "guardduty",
					"k8s_cluster_name": "cloud",
				},
				Annotations: map[string]string{
					"summary": "{{ .detail.title }}",
					"runbook": "cloud-security-finding",
				},
			},
		},
		{
			Name:  "root-login",
			Match: map[string]string{"detail-type": "AWS Console Sign In via CloudTrail", "detail.userIdentity.type": "Root"},
			Alert: AlertSpec{
				Alertname: "RootConsoleLogin",
				Severity:  "critical",
				Labels:    map[string]string{"source": "cloudtrail", "k8s_cluster_name": "cloud"},
			},
		},
		{
			Name:  "budget",
			Match: map[string]string{"Message.budgetName": "*"},
			Alert: AlertSpec{
				Alertname: "BudgetThresholdCrossed",
				Severity:  "warning",
				Labels:    map[string]string{"source": "budgets", "k8s_cluster_name": "cloud"},
			},
		},
	}
}

// firstMatch is the same loop handler.go runs in step 3, isolated here so
// a fixture test can name the mapping it expects without standing up a
// whole Server.
func firstMatch(t *testing.T, mappings []Mapping, message string) (Mapping, map[string]any, bool) {
	t.Helper()

	body := parseBody(message)
	for _, m := range mappings {
		if matches(m.Match, body) {
			return m, body, true
		}
	}

	return Mapping{}, body, false
}

// TestGuardDutyFindingRendersSeverityFromScore is the finding fixture. A
// severity below 7.0 must render `warning`, and one at or above it
// `critical` — the whole reason the design page's severity field is a
// template rather than a fixed string.
func TestGuardDutyFindingRendersSeverityFromScore(t *testing.T) {
	cases := []struct {
		name     string
		message  string
		severity string
	}{
		{"a high-severity finding", `{"detail-type":"GuardDuty Finding","detail":{"severity":8.5,"title":"example finding"}}`, "critical"},
		{"a low-severity finding", `{"detail-type":"GuardDuty Finding","detail":{"severity":2.0,"title":"example finding"}}`, "warning"},
		{"exactly the threshold", `{"detail-type":"GuardDuty Finding","detail":{"severity":7.0,"title":"example finding"}}`, "critical"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, body, ok := firstMatch(t, exampleMappings(), c.message)
			require.Truef(t, ok, "a GuardDuty finding must match the guardduty mapping")
			require.Equal(t, "guardduty", m.Name)

			labels, annotations, err := renderAlert(m.Alert, body)
			require.NoError(t, err)

			assert.Equal(t, "CloudSecurityFinding", labels["alertname"])
			assert.Equal(t, c.severity, labels["severity"])
			assert.Equal(t, "example finding", annotations["summary"])
		})
	}
}

// TestRootSignInRendersFixedCriticalAlert is the sign-in fixture. Unlike
// the finding, every field here is a plain string with no `{{` in it —
// proving that a mapping which uses no template at all still renders,
// because renderString treats a plain string as its own template.
func TestRootSignInRendersFixedCriticalAlert(t *testing.T) {
	message := `{"detail-type":"AWS Console Sign In via CloudTrail","detail":{"userIdentity":{"type":"Root"}}}`

	m, body, ok := firstMatch(t, exampleMappings(), message)
	require.Truef(t, ok, "a root console sign-in must match the root-login mapping")
	require.Equal(t, "root-login", m.Name)

	labels, _, err := renderAlert(m.Alert, body)
	require.NoError(t, err)

	assert.Equal(t, "RootConsoleLogin", labels["alertname"])
	assert.Equal(t, "critical", labels["severity"])
	assert.Equal(t, "cloud", labels["k8s_cluster_name"],
		"k8s_cluster_name is the routing key the tree groups and routes on, not a real cluster — see docs/alert-ingress.md")
}

// TestBudgetNotificationMatchesOnPresenceAlone is the budget fixture: the
// mapping's `match` is a wildcard, so a notification is caught whichever
// budget crossed its line, not only one the mapping author anticipated by
// name.
func TestBudgetNotificationMatchesOnPresenceAlone(t *testing.T) {
	message := `{"Message":{"budgetName":"example-monthly-spend","threshold":80}}`

	m, body, ok := firstMatch(t, exampleMappings(), message)
	require.Truef(t, ok, "a budget notification must match the budget mapping")
	require.Equal(t, "budget", m.Name)

	labels, _, err := renderAlert(m.Alert, body)
	require.NoError(t, err)
	assert.Equal(t, "BudgetThresholdCrossed", labels["alertname"])
	assert.Equal(t, "warning", labels["severity"])
}

// TestAlarmStateChangeIsUnmapped is the fourth fixture and the unmapped
// path both at once: a CloudWatch alarm state change is a real,
// well-formed event — it is simply a shape none of the three example
// mappings names. It must NOT be dropped, and it must NOT crash the
// service; it becomes CloudEventUnmapped with its own body attached.
func TestAlarmStateChangeIsUnmapped(t *testing.T) {
	message := `{"detail-type":"CloudWatch Alarm State Change","detail":{"alarmName":"example-alarm","state":{"value":"ALARM"}}}`

	_, _, ok := firstMatch(t, exampleMappings(), message)
	assert.Falsef(t, ok, "an alarm state change must match none of the three example mappings — "+
		"if it does, this test and the design page's own examples have drifted apart")

	labels, annotations := unmappedAlert(message)
	assert.Equal(t, "CloudEventUnmapped", labels["alertname"])
	assert.Equal(t, "warning", labels["severity"])
	assert.Contains(t, annotations["body"], "CloudWatch Alarm State Change",
		"the unmapped alert must carry the body it could not place, or an on-call person has nothing to act on")
}

// TestUnmappedAlertNeverTemplatesTheBody is the defect the unmapped path
// exists to be safe against ONE MORE TIME: the body reaching it is, by
// definition, content this service does not understand, and running
// attacker-reachable text through text/template — even only as data a
// template executes against — would let a message containing `{{` break
// the one path that is supposed to be unbreakable. unmappedAlert must
// never call renderString at all.
func TestUnmappedAlertNeverTemplatesTheBody(t *testing.T) {
	hostile := `this is not json and it contains {{ .anything }} and {{ define "x" }}`

	labels, annotations := unmappedAlert(hostile)
	assert.Equal(t, "CloudEventUnmapped", labels["alertname"])
	assert.True(t, strings.Contains(annotations["body"], "{{ .anything }}"),
		"the template-looking text must survive UNCHANGED, proving it was never executed")
}

func TestSeverityLabelCannotBeShadowedByAMappingsOwnLabels(t *testing.T) {
	spec := AlertSpec{
		Alertname: "Example",
		Severity:  "critical",
		Labels:    map[string]string{"severity": "info", "alertname": "wrong"},
	}

	labels, _, err := renderAlert(spec, map[string]any{})
	require.NoError(t, err)

	assert.Equal(t, "critical", labels["severity"],
		"AlertSpec.Severity must win over a same-named entry under `labels`, the way "+
			"platform-alerts.labels protects `severity` from commonLabels")
	assert.Equal(t, "Example", labels["alertname"])
}
