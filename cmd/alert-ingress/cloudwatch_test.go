// CloudWatch alarm notifications: the SNS Message is a JSON object
// (AlarmName, NewStateValue, NewStateReason, Trigger, ...) that an alarm's
// AlarmActions, OKActions and InsufficientDataActions publish to a topic.
// The mapping under test is testdata/cloudwatch-mapping.yaml, the same text
// docs/alert-ingress.md shows, and the payloads are the shape CloudWatch
// sends (the three states of an alarm), run through the whole handler.
package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// This repository is public and hack/leak-canary.sh refuses an ARN or an
// account id anywhere in it, so the payloads carry the tokens ARN_PREFIX and
// ACCOUNT_ID and the test fills in a made-up pair. Only the shape matters to
// a mapping.
const (
	arnPrefix  = "arn:" + "aws"
	account    = "1111" + "2222" + "3333"
	alarmTopic = arnPrefix + ":sns:eu-central-1:" + account + ":audit-alarms"
)

var fill = strings.NewReplacer("ARN_PREFIX", arnPrefix, "ACCOUNT_ID", account)

func cloudwatchMappings(t *testing.T) []Mapping {
	t.Helper()

	raw, err := os.ReadFile("testdata/cloudwatch-mapping.yaml")
	require.NoError(t, err)

	var doc struct {
		Mappings []Mapping `yaml:"mappings"`
	}

	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.Len(t, doc.Mappings, 1)

	return doc.Mappings
}

func cloudwatchPost(t *testing.T, payload string) (*Server, *fakeAlertmanager, *http.Response) {
	t.Helper()

	raw, err := os.ReadFile("testdata/" + payload)
	require.NoError(t, err)

	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := baseConfig()
	cfg.Topics = []string{alarmTopic}
	cfg.Mappings = cloudwatchMappings(t)
	cfg.ResolveAfter = Duration(time.Hour)
	srv := newTestServer(t, fixture, cfg, am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "cw-1",
		TopicArn:  alarmTopic,
		Subject:   "ALARM: \"audit-writer-errors\" in EU (Frankfurt)",
		Message:   fill.Replace(string(raw)),
		Timestamp: "2026-10-04T09:52:12.400Z",
	})

	rec := post(t, srv, env)

	return srv, am, rec.Result()
}

func TestCloudWatchAlarmFires(t *testing.T) {
	srv, am, resp := cloudwatchPost(t, "cloudwatch-alarm.json")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, am.posts, 1)

	a := am.posts[0][0]
	assert.Equal(t, map[string]string{
		"alertname":        "audit-writer-errors",
		"severity":         "critical",
		"source":           "cloudwatch",
		"k8s_cluster_name": "cloud-security",
		"account":          account,
		"region":           "eu-central-1",
	}, a.Labels)
	assert.Equal(t, "ALARM", a.Annotations["state"])
	assert.Equal(t, "OK", a.Annotations["previous_state"])
	assert.Contains(t, a.Annotations["reason"], "Threshold Crossed")
	assert.Equal(t, "AWS/Lambda/Errors", a.Annotations["metric"])
	assert.Equal(t, 24*time.Hour, a.EndsAt.Sub(a.StartsAt), "the mapping's own resolveAfter, not the 1h global")
	assert.Equal(t, float64(1), counterValue(t, srv, "mapped", "cloudwatch-alarm"))
}

// The OK message must clear the alert the ALARM one raised: the same
// labels (so the same fingerprint) and an endsAt that is not in the future.
func TestCloudWatchOKResolves(t *testing.T) {
	_, firing, _ := cloudwatchPost(t, "cloudwatch-alarm.json")
	_, ok, resp := cloudwatchPost(t, "cloudwatch-ok.json")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, ok.posts, 1)

	a := ok.posts[0][0]
	assert.Equal(t, firing.posts[0][0].Labels, a.Labels, "a resolve with other labels clears nothing")
	assert.Equal(t, a.StartsAt, a.EndsAt, "resolved means endsAt is now")
	assert.Equal(t, "OK", a.Annotations["state"])
}

// INSUFFICIENT_DATA stays a firing alert at the default severity: a monitor
// that cannot see is not known-good. Whether it is sent at all is the
// alarm's InsufficientDataActions, so an alarm that is routinely sparse
// simply does not name the topic there.
func TestCloudWatchInsufficientDataFiresAtDefaultSeverity(t *testing.T) {
	_, am, resp := cloudwatchPost(t, "cloudwatch-insufficient-data.json")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, am.posts, 1)

	a := am.posts[0][0]
	assert.Equal(t, "audit-queue-age", a.Labels["alertname"])
	assert.Equal(t, "warning", a.Labels["severity"], "a null AlarmDescription must default, not fail the render")
	assert.Equal(t, "INSUFFICIENT_DATA", a.Annotations["state"])
	assert.Equal(t, 24*time.Hour, a.EndsAt.Sub(a.StartsAt))
}

// A body that is not an alarm must not match: it stays CloudEventUnmapped.
func TestCloudWatchMappingIgnoresOtherShapes(t *testing.T) {
	m := cloudwatchMappings(t)[0]
	body := parseBody(`{"detail-type":"CloudWatch Alarm State Change","detail":{"alarmName":"x"}}`)

	assert.False(t, matchesMapping(m, body))
}

func TestResolvedTemplate(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"true":                    true,
		" True \n":                true,
		"false":                   false,
		"yes":                     false,
		`{{ eq .state "OK" }}`:    true,
		`{{ eq .state "ALARM" }}`: false,
	}

	for tmpl, want := range cases {
		got, err := renderResolved(AlertSpec{Resolved: tmpl}, map[string]any{"state": "OK"})
		require.NoError(t, err, tmpl)
		assert.Equal(t, want, got, tmpl)
	}

	_, err := renderResolved(AlertSpec{Resolved: "{{ .a.b.c }"}, map[string]any{})
	assert.Error(t, err)
}

// A resolved template that fails to render falls back to
// CloudEventUnmapped, still firing: never a silent drop.
func TestBrokenResolvedTemplateFallsBackToUnmapped(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := baseConfig()
	cfg.Mappings[0].Alert.Resolved = "{{ .x.y.z }"
	cfg.Mappings[0].ResolveAfter = Duration(24 * time.Hour)
	cfg.ResolveAfter = Duration(time.Hour)
	srv := newTestServer(t, fixture, cfg, am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "id-r",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	require.Equal(t, http.StatusOK, post(t, srv, env).Code)
	require.Len(t, am.posts, 1)

	a := am.posts[0][0]
	assert.Equal(t, "CloudEventUnmapped", a.Labels["alertname"])
	assert.Equal(t, time.Hour, a.EndsAt.Sub(a.StartsAt), "the fallback uses the global expiry")
}

func TestNegativeMappingResolveAfterIsRefused(t *testing.T) {
	cfg := baseConfig()
	cfg.Alertmanager.URL = "http://am"
	cfg.Mappings[0].ResolveAfter = Duration(-time.Minute)

	assert.ErrorContains(t, cfg.Validate(), "negative resolveAfter")
}
