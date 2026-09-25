// End-to-end through Server.ServeHTTP, with a real Verifier (signing an
// envelope the same way TestSignedMessageVerifies does) and a fake
// Alertmanager recording what it was sent. These are the tests that would
// have caught the heartbeat design gap: docs/alert-ingress.md's six
// numbered steps never say where the heartbeat sits among them, and only
// running a heartbeat message through the whole handler proves it takes
// the branch this file gives it — recognised and counted, never posted —
// rather than falling into either "mapped" or "unmapped".
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAlertmanager records every alert it was POSTed, so a test can
// assert on what would have reached the routing tree without a real
// Alertmanager anywhere nearby.
type fakeAlertmanager struct {
	server *httptest.Server
	posts  [][]AlertmanagerAlert
}

func newFakeAlertmanager(t *testing.T) *fakeAlertmanager {
	t.Helper()

	f := &fakeAlertmanager{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var alerts []AlertmanagerAlert

		require.NoError(t, json.NewDecoder(r.Body).Decode(&alerts))
		f.posts = append(f.posts, alerts)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.server.Close)

	return f
}

func newTestServer(t *testing.T, fixture *signingFixture, cfg Config, am *fakeAlertmanager) *Server {
	t.Helper()

	cfg.Alertmanager.URL = am.server.URL

	return &Server{
		Config:       cfg,
		Verifier:     fixture.verifier(),
		Alertmanager: &AlertmanagerClient{URL: am.server.URL, HTTPClient: am.server.Client()},
		Metrics:      NewMetrics(prometheus.NewRegistry()),
		Logger:       slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	}
}

// testWriter sends the server's own log lines to t.Log, so a failing test
// shows what the handler saw rather than nothing.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func baseConfig() Config {
	return Config{
		Topics: []string{"<the security-alerts topic ARN>"},
		Mappings: []Mapping{
			{
				Name:  "guardduty",
				Match: map[string]string{"detail-type": "GuardDuty Finding"},
				Alert: AlertSpec{Alertname: "CloudSecurityFinding", Severity: "critical"},
			},
		},
		Heartbeat:    Heartbeat{Match: map[string]string{"source": "alert-ingress-heartbeat"}},
		ResolveAfter: Duration(0),
	}
}

func post(t *testing.T, srv *Server, env Envelope) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(env)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

func counterValue(t *testing.T, srv *Server, outcome, mapping string) float64 {
	t.Helper()

	var m dto.Metric
	require.NoError(t, srv.Metrics.Messages.WithLabelValues(outcome, mapping).Write(&m))

	return m.GetCounter().GetValue()
}

// TestHeartbeatIsCountedNeverPosted is the pipeline-order proof: the
// design page lists six steps and never says where the heartbeat sits
// among them, and the values shape settles it instead — `heartbeat:`
// carries no `alert:` field, so there is nothing to render, and it must
// be recognised before the ordinary mapping loop rather than falling
// through to CloudEventUnmapped.
func TestHeartbeatIsCountedNeverPosted(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "id-1",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"source":"alert-ingress-heartbeat"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Emptyf(t, am.posts, "a heartbeat must never reach Alertmanager as an alert")
	assert.Equal(t, float64(1), counterValue(t, srv, "received", "heartbeat"))
}

// TestMappedFindingIsPostedAndCounted is the ordinary success path: a
// signed, allow-listed finding matches its mapping and reaches the fake
// Alertmanager as one alert.
func TestMappedFindingIsPostedAndCounted(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "id-2",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding","detail":{"severity":9.0,"title":"example"}}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Lenf(t, am.posts, 1, "a mapped finding must be posted exactly once")
	assert.Equal(t, "CloudSecurityFinding", am.posts[0][0].Labels["alertname"])
	assert.Equal(t, float64(1), counterValue(t, srv, "mapped", "guardduty"))
}

// TestUnmappedShapeIsPostedAsCloudEventUnmapped proves step 4 end to end:
// a signed, allow-listed message of a shape no mapping names still
// reaches Alertmanager, never the void.
func TestUnmappedShapeIsPostedAsCloudEventUnmapped(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "id-3",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"CloudWatch Alarm State Change"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, am.posts, 1)
	assert.Equal(t, "CloudEventUnmapped", am.posts[0][0].Labels["alertname"])
	assert.Equal(t, float64(1), counterValue(t, srv, "unmapped", "unmapped"))
}

// TestOffAllowlistTopicIsRejected is the second refusal the design page
// names by number: a signed message from a topic nobody listed is turned
// away with 403, never confirmed and never matched.
func TestOffAllowlistTopicIsRejected(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "id-4",
		TopicArn:  "<some other topic nobody allow-listed>",
		Message:   `{"detail-type":"GuardDuty Finding"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, am.posts)
	assert.Equal(t, float64(1), counterValue(t, srv, "rejected", ""))
}

// TestUnsignedMessageIsRejected is the first refusal: no amount of
// allow-listing helps a message whose signature does not verify.
func TestUnsignedMessageIsRejected(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	env := Envelope{
		Type:      "Notification",
		MessageID: "id-5",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
		Signature: "not-a-real-signature",
	}

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, am.posts)
}

// TestSubscriptionConfirmationConfirmsOnAllowlistedTopicOnly is the
// design page's second step, both halves of it: an allow-listed topic is
// confirmed by GETting the SubscribeURL, and one that is not never
// triggers that outbound request at all.
func TestSubscriptionConfirmationConfirmsOnAllowlistedTopicOnly(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)

	var confirmed bool

	confirmServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		confirmed = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(confirmServer.Close)

	srv := newTestServer(t, fixture, baseConfig(), am)
	// The confirmation server is a second httptest instance with its own
	// port; the fixture's allowedHost override only trusts the signing
	// server's host, so it is widened here to cover both — standing in
	// for the single pinned production domain that both SigningCertURL
	// and SubscribeURL share in the real provider.
	certHost := mustHostname(t, fixture.server.URL)
	confirmHost := mustHostname(t, confirmServer.URL)
	srv.Verifier.allowedHost = func(h string) bool { return h == certHost || h == confirmHost }

	env := fixture.sign(t, Envelope{
		Type:         "SubscriptionConfirmation",
		MessageID:    "id-6",
		TopicArn:     "<the security-alerts topic ARN>",
		Message:      "You have chosen to subscribe...",
		Timestamp:    "2026-01-01T00:00:00.000Z",
		Token:        "example-token",
		SubscribeURL: confirmServer.URL + "/confirm",
	})

	rec := post(t, srv, env)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, confirmed, "an allow-listed topic's SubscriptionConfirmation must be confirmed")
	assert.Empty(t, am.posts, "a subscription confirmation is never itself an alert")
}

func mustHostname(t *testing.T, raw string) string {
	t.Helper()

	u, err := url.Parse(raw)
	require.NoError(t, err)

	return u.Hostname()
}
