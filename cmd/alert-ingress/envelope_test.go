package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	exampleTopic  = "<the security-alerts topic ARN>"
	exampleBudget = "<the budgets topic ARN>"
)

func rejectedValue(t *testing.T, srv *Server, reason string) float64 {
	t.Helper()

	var m dto.Metric
	require.NoError(t, srv.Metrics.Rejected.WithLabelValues(reason).Write(&m))

	return m.GetCounter().GetValue()
}

func envelopeConfig(mappings ...Mapping) Config {
	return Config{
		Topics:    []string{exampleTopic, exampleBudget},
		Mappings:  mappings,
		Heartbeat: Heartbeat{Match: map[string]string{"source": "alert-ingress-heartbeat"}},
	}
}

func notification(f *signingFixture, t *testing.T, topic, subject, message string) Envelope {
	t.Helper()

	return f.sign(t, Envelope{
		Type: "Notification", MessageID: "id", TopicArn: topic, Subject: subject,
		Message: message, Timestamp: "2026-01-01T00:00:00.000Z",
	})
}

// A plain-text publisher (a budget notification) has no JSON body at all:
// it must be matchable on the topic, the subject and the raw text.
func TestPlainTextMessageMatchesOnEnvelopeFields(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := envelopeConfig(Mapping{
		Name:       "budget",
		Match:      map[string]string{"_sns.TopicArn": exampleBudget},
		MatchRegex: map[string]string{"_sns.Message": `Budget Name: (\S+)`},
		Alert: AlertSpec{
			Alertname: "BudgetThresholdCrossed", Severity: "warning",
			Annotations: map[string]string{"summary": "{{ ._sns.Subject }}", "text": "{{ ._sns.Message }}"},
		},
	})
	srv := newTestServer(t, fixture, cfg, am)

	rec := post(t, srv, notification(fixture, t, exampleBudget,
		"AWS Budgets: example has exceeded your alert threshold",
		"AWS Budget Notification\nBudget Name: example\nThreshold: > 80%"))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, am.posts, 1)
	a := am.posts[0][0]
	assert.Equal(t, "BudgetThresholdCrossed", a.Labels["alertname"])
	assert.Equal(t, "AWS Budgets: example has exceeded your alert threshold", a.Annotations["summary"])
	assert.Contains(t, a.Annotations["text"], "Budget Name: example")
	assert.Equal(t, float64(1), counterValue(t, srv, "mapped", "budget"))
}

// A non-matching plain-text message on the same topic still surfaces as
// CloudEventUnmapped.
func TestPlainTextWithoutMatchIsStillUnmapped(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := envelopeConfig(Mapping{
		Name:       "budget",
		MatchRegex: map[string]string{"_sns.Message": `Budget Name:`},
		Alert:      AlertSpec{Alertname: "BudgetThresholdCrossed", Severity: "warning"},
	})
	srv := newTestServer(t, fixture, cfg, am)

	post(t, srv, notification(fixture, t, exampleTopic, "", "something else entirely"))

	require.Len(t, am.posts, 1)
	assert.Equal(t, "CloudEventUnmapped", am.posts[0][0].Labels["alertname"])
}

// A publisher cannot forge the envelope by putting its own `_sns` key in
// the JSON body: the signed fields overwrite it.
func TestBodyCannotForgeEnvelopeKey(t *testing.T) {
	in := buildInput(Envelope{TopicArn: exampleTopic, Message: `{"_sns":{"TopicArn":"forged"},"a":"b"}`})

	got, ok := lookup(in, "_sns.TopicArn")
	require.True(t, ok)
	assert.Equal(t, exampleTopic, got)

	got, _ = lookup(in, "a")
	assert.Equal(t, "b", got)
}

// Existing mappings, which read only the parsed body, keep working
// unchanged next to the envelope.
func TestJSONBodyMappingsAreUnaffectedByEnvelope(t *testing.T) {
	m, body, ok := firstMatch(t, exampleMappings(),
		`{"detail-type":"GuardDuty Finding","detail":{"severity":8.5,"title":"t"}}`)
	require.True(t, ok)
	assert.Equal(t, "guardduty", m.Name)

	body[envelopeKey] = map[string]any{"TopicArn": exampleTopic}
	assert.True(t, matchesMapping(m, body))
}

func TestInvalidMatchRegexIsRefusedAtLoad(t *testing.T) {
	cfg := baseConfig()
	cfg.Alertmanager.URL = "http://am"
	cfg.Mappings = []Mapping{{
		Name: "x", MatchRegex: map[string]string{"_sns.Message": "("},
		Alert: AlertSpec{Alertname: "X", Severity: "warning"},
	}}
	assert.ErrorContains(t, cfg.Validate(), "matchRegex")
}

// Distinct findings must not collapse into one Alertmanager alert: the
// labels carry account, region, finding type and id.
func TestDistinctFindingsGetDistinctLabelSets(t *testing.T) {
	spec := AlertSpec{
		Alertname: "CloudSecurityFinding",
		Severity:  `{{ if atLeast .detail.severity 7 }}critical{{ else }}warning{{ end }}`,
		Labels: map[string]string{
			"account":      "{{ .account }}",
			"region":       "{{ .region }}",
			"finding_type": "{{ .detail.type }}",
			"finding_id":   "{{ .detail.id }}",
		},
	}
	render := func(msg string) map[string]string {
		l, _, err := renderAlert(spec, buildInput(Envelope{Message: msg}))
		require.NoError(t, err)

		return l
	}

	a := render(`{"account":"example-account","region":"eu-west-1","detail":{"severity":8,"type":"Recon:EC2/Portscan","id":"f1"}}`)
	b := render(`{"account":"example-account","region":"eu-west-1","detail":{"severity":8,"type":"Recon:EC2/Portscan","id":"f2"}}`)

	assert.NotEqual(t, a, b)
	assert.Equal(t, "f1", a["finding_id"])
	assert.Equal(t, "eu-west-1", a["region"])
	assert.Equal(t, "critical", a["severity"])
}

func TestAtLeastSeverityThreshold(t *testing.T) {
	cases := []struct {
		name, message, want string
	}{
		{"float above", `{"detail":{"severity":8.9}}`, "critical"},
		{"int exactly at", `{"detail":{"severity":7}}`, "critical"},
		{"float just below", `{"detail":{"severity":6.9}}`, "warning"},
		{"numeric string", `{"detail":{"severity":"8"}}`, "critical"},
		{"absent", `{"detail":{}}`, "warning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := renderString(`{{ if atLeast .detail.severity 7 }}critical{{ else }}warning{{ end }}`, parseBody(c.message))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}

	_, err := renderString(`{{ if atLeast .detail.severity 7 }}x{{ end }}`, parseBody(`{"detail":{"severity":"high"}}`))
	assert.Error(t, err, "a non-numeric severity must fail the render, which falls back to CloudEventUnmapped")
}

// Each rejection path increments its own bounded reason.
func TestRejectedReasons(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, envelopeConfig(), am)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	srv.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, float64(1), rejectedValue(t, srv, ReasonMalformed))

	unsigned := notification(fixture, t, exampleTopic, "", `{}`)
	unsigned.Signature = "AAAA"
	post(t, srv, unsigned)
	assert.Equal(t, float64(1), rejectedValue(t, srv, ReasonSignature))

	post(t, srv, notification(fixture, t, "<some other topic nobody allow-listed>", "", `{}`))
	assert.Equal(t, float64(1), rejectedValue(t, srv, ReasonTopic))

	unsub := fixture.sign(t, Envelope{Type: "UnsubscribeConfirmation", TopicArn: exampleTopic, Message: "m", Timestamp: "t", SubscribeURL: "u", Token: "k"})
	post(t, srv, unsub)
	assert.Equal(t, float64(1), rejectedValue(t, srv, ReasonType))

	badConfirm := fixture.sign(t, Envelope{
		Type: "SubscriptionConfirmation", TopicArn: exampleTopic, Message: "m", Timestamp: "t",
		SubscribeURL: "https://attacker.example/x", Token: "k",
	})
	post(t, srv, badConfirm)
	assert.Equal(t, float64(1), rejectedValue(t, srv, ReasonConfirmation))

	assert.Equal(t, float64(5), counterValue(t, srv, "rejected", ""), "the unlabelled counter still counts every rejection")
	assert.Empty(t, am.posts)
}

// Any non-POST request on the webhook port, /metrics included, is a 405
// from the webhook handler itself.
func TestWebhookHandlerRefusesGET(t *testing.T) {
	fixture := newSigningFixture(t)
	srv := newTestServer(t, fixture, envelopeConfig(), newFakeAlertmanager(t))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

const (
	costAnomalyTopic = "<the cost anomaly topic ARN>"
	anomalyMessage   = `{"accountId":"example-account","anomalyId":"a1","dimensionalValue":"Amazon Elastic Block Store",` +
		`"monitorArn":"example-monitor-arn","impact":{"maxImpact":48.22,"totalImpact":48.22},` +
		`"rootCauses":[{"service":"Amazon Elastic Block Store","region":"us-east-1"}],` +
		`"anomalyDetailsLink":"https://example.invalid/anomaly/a1"}`
)

func awsMappings() []Mapping {
	return []Mapping{
		{
			Name:       "budget",
			Match:      map[string]string{"_sns.TopicArn": exampleBudget},
			MatchRegex: map[string]string{"_sns.Message": "Budget Name: "},
			Alert: AlertSpec{
				Alertname: "CloudBudgetThreshold", Severity: "warning",
				Labels: map[string]string{
					"topic":       "{{ ._sns.TopicArn }}",
					"subject":     "{{ ._sns.Subject }}",
					"budget_name": `{{ reFind "Budget Name: (\\S+)" ._sns.Message }}`,
				},
			},
		},
		{
			Name:  "cost-anomaly",
			Match: map[string]string{"_sns.TopicArn": costAnomalyTopic, "anomalyId": "*"},
			Alert: AlertSpec{
				Alertname: "CloudCostAnomaly", Severity: "warning",
				Labels: map[string]string{"monitor_name": "{{ .dimensionalValue }}", "account_id": "{{ .accountId }}"},
				Annotations: map[string]string{
					"impact": "{{ .impact.totalImpact }}", "details": "{{ .anomalyDetailsLink }}",
				},
			},
		},
	}
}

func TestBudgetPlainTextExtractsNameAndTopic(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := envelopeConfig(awsMappings()...)
	cfg.Topics = append(cfg.Topics, costAnomalyTopic)
	srv := newTestServer(t, fixture, cfg, am)

	post(t, srv, notification(fixture, t, exampleBudget, "AWS Budgets: example has exceeded your alert threshold",
		"AWS Budget Notification\nBudget Name: example\nBudget Type: COST\nAlert Threshold: > 80%"))

	require.Len(t, am.posts, 1)
	l := am.posts[0][0].Labels
	assert.Equal(t, "CloudBudgetThreshold", l["alertname"])
	assert.Equal(t, exampleBudget, l["topic"])
	assert.Equal(t, "AWS Budgets: example has exceeded your alert threshold", l["subject"])
	assert.Equal(t, "example", l["budget_name"])
}

func TestBudgetRewordedTextDegradesLabelNotAlert(t *testing.T) {
	in := buildInput(Envelope{TopicArn: exampleBudget, Message: "Budget Name: \nsomething else"})
	l, _, err := renderAlert(awsMappings()[0].Alert, in)
	require.NoError(t, err)
	assert.Empty(t, l["budget_name"])

	_, err = renderString(`{{ reFind "(" "x" }}`, nil)
	assert.Error(t, err)

	got, err := renderString(`{{ reFind "a+" "baab" }}`, nil)
	require.NoError(t, err)
	assert.Equal(t, "aa", got)
}

func TestCostAnomalyJSONMapsAndIsScopedByTopic(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := envelopeConfig(awsMappings()...)
	cfg.Topics = append(cfg.Topics, costAnomalyTopic)
	srv := newTestServer(t, fixture, cfg, am)

	post(t, srv, notification(fixture, t, costAnomalyTopic, "AWS Cost Management: Cost anomaly detected", anomalyMessage))
	// The same body on another allow-listed topic is not this mapping.
	post(t, srv, notification(fixture, t, exampleBudget, "", anomalyMessage))

	require.Len(t, am.posts, 2)
	a := am.posts[0][0]
	assert.Equal(t, "CloudCostAnomaly", a.Labels["alertname"])
	assert.Equal(t, "Amazon Elastic Block Store", a.Labels["monitor_name"])
	assert.Equal(t, "example-account", a.Labels["account_id"])
	assert.Equal(t, "48.22", a.Annotations["impact"])
	assert.Equal(t, "https://example.invalid/anomaly/a1", a.Annotations["details"])
	assert.Equal(t, "CloudEventUnmapped", am.posts[1][0].Labels["alertname"])
}

// Plain text yields a nil-equivalent body but never a dead end: the
// envelope is present and a JSON-only mapping simply does not match.
func TestPlainTextBodyIsEmptyButEnvelopeMatchable(t *testing.T) {
	in := buildInput(Envelope{TopicArn: exampleBudget, Subject: "s", Message: "not json"})
	assert.True(t, matches(map[string]string{"_sns.Subject": "s"}, in))
	assert.False(t, matches(map[string]string{"anomalyId": "*"}, in))
}
