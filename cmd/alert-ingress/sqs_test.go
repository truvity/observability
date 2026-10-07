package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTopic = "<the security-alerts topic ARN>"

// fakeSQS serves one prepared batch, then empty receives, and records
// every delete and every receive input.
type fakeSQS struct {
	batch      []types.Message
	receiveErr error
	deleteErr  error
	deleted    []string
	inputs     []*sqs.ReceiveMessageInput
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.inputs = append(f.inputs, in)

	if f.receiveErr != nil {
		return nil, f.receiveErr
	}

	out := &sqs.ReceiveMessageOutput{Messages: f.batch}
	f.batch = nil

	return out, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}

	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))

	return &sqs.DeleteMessageOutput{}, nil
}

func sqsMessage(t *testing.T, handle string, env Envelope) types.Message {
	t.Helper()

	body, err := json.Marshal(env)
	require.NoError(t, err)

	return types.Message{MessageId: aws.String("m-" + handle), ReceiptHandle: aws.String(handle), Body: aws.String(string(body))}
}

func newConsumer(t *testing.T, srv *Server, fake *fakeSQS) *SQSConsumer {
	t.Helper()

	return &SQSConsumer{
		Client: fake,
		Config: SQSConfig{
			QueueURL: "https://sqs.eu-west-1.amazonaws.com/ACCOUNT/alerts", MaxMessages: 10,
			WaitTimeSeconds: 20, VisibilityTimeoutSeconds: 60, Concurrency: 1,
		},
		Server:  srv,
		Metrics: srv.Metrics,
		Logger:  srv.Logger,
	}
}

func value(t *testing.T, c prometheus.Metric) float64 {
	t.Helper()

	var m dto.Metric
	require.NoError(t, c.Write(&m))

	if m.Counter != nil {
		return m.GetCounter().GetValue()
	}

	return m.GetGauge().GetValue()
}

func findingEnv(f *signingFixture, t *testing.T) Envelope {
	return f.sign(t, Envelope{
		Type: "Notification", MessageID: "id", TopicArn: testTopic,
		Message:   `{"detail-type":"GuardDuty Finding","detail":{"severity":9.0}}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})
}

func TestSQSDeletesAfterAlertmanagerAccepts(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)
	fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", findingEnv(fixture, t))}}
	c := newConsumer(t, srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))

	require.Len(t, am.posts, 1)
	assert.Equal(t, "CloudSecurityFinding", am.posts[0][0].Labels["alertname"])
	assert.Equal(t, []string{"h1"}, fake.deleted)
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSReceived))
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSProcessed))
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSDeleted))
	assert.Equal(t, float64(1), counterValue(t, srv, "mapped", "guardduty"))

	in := fake.inputs[0]
	assert.Equal(t, int32(10), in.MaxNumberOfMessages)
	assert.Equal(t, int32(20), in.WaitTimeSeconds)
	assert.Equal(t, int32(60), in.VisibilityTimeout)
}

func TestSQSKeepsMessageWhenAlertmanagerFails(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)

	srv.Alertmanager = &AlertmanagerClient{URL: down.URL, HTTPClient: down.Client()}

	fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", findingEnv(fixture, t))}}
	c := newConsumer(t, srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))

	assert.Empty(t, fake.deleted, "a message Alertmanager did not accept must stay on the queue")
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSFailed))
	assert.Equal(t, float64(0), value(t, srv.Metrics.SQSProcessed))
	assert.Equal(t, float64(0), value(t, srv.Metrics.SQSDeleted))
}

func TestSQSRejectionsAreDeleted(t *testing.T) {
	fixture := newSigningFixture(t)

	badSig := findingEnv(fixture, t)
	badSig.Signature = "not-a-real-signature"

	offList := fixture.sign(t, Envelope{
		Type: "Notification", MessageID: "id", TopicArn: "<some other topic>",
		Message: `{}`, Timestamp: "2026-01-01T00:00:00.000Z",
	})

	cases := []struct {
		name   string
		msg    types.Message
		reason string
		kept   bool
	}{
		{"signature", sqsMessage(t, "h1", badSig), ReasonSignature, true},
		{"topic", sqsMessage(t, "h2", offList), ReasonTopic, true},
		{"malformed", types.Message{MessageId: aws.String("m3"), ReceiptHandle: aws.String("h3"), Body: aws.String("SECRET-BODY not json")}, ReasonMalformed, false},
		{"nobody", types.Message{MessageId: aws.String("m4"), ReceiptHandle: aws.String("h4")}, ReasonMalformed, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am := newFakeAlertmanager(t)
			srv := newTestServer(t, fixture, baseConfig(), am)

			var logs lockedBuffer

			srv.Logger = slog.New(slog.NewTextHandler(&logs, nil))

			fake := &fakeSQS{batch: []types.Message{tc.msg}}
			c := newConsumer(t, srv, fake)

			require.NoError(t, c.PollOnce(context.Background()))

			if tc.kept {
				assert.Empty(t, fake.deleted, "a topic or signature rejection stays on the queue for the dead-letter queue")
			} else {
				assert.Equal(t, []string{aws.ToString(tc.msg.ReceiptHandle)}, fake.deleted)
			}

			assert.Empty(t, am.posts)
			assert.Equal(t, float64(1), value(t, srv.Metrics.SQSRejected.WithLabelValues(tc.reason)))
			assert.Equal(t, float64(1), value(t, srv.Metrics.Rejected.WithLabelValues(tc.reason)))
			if tc.kept {
				assert.Equal(t, float64(0), value(t, srv.Metrics.SQSDeleted))
			} else {
				assert.Equal(t, float64(1), value(t, srv.Metrics.SQSDeleted))
			}

			assert.NotContains(t, logs.String(), "SECRET-BODY")
		})
	}
}

// confirmFixture is a consumer whose Verifier may reach a stand-in for the
// SubscribeURL host; status is what that host answers.
type confirmFixture struct {
	fixture *signingFixture
	srv     *Server
	hits    int
	status  int
	url     string
}

func newConfirmFixture(t *testing.T) *confirmFixture {
	t.Helper()

	cf := &confirmFixture{fixture: newSigningFixture(t), status: http.StatusOK}
	am := newFakeAlertmanager(t)

	confirmServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cf.hits++
		w.WriteHeader(cf.status)
	}))
	t.Cleanup(confirmServer.Close)

	cf.srv = newTestServer(t, cf.fixture, baseConfig(), am)
	certHost := mustHostname(t, cf.fixture.server.URL)
	confirmHost := mustHostname(t, confirmServer.URL)
	cf.srv.Verifier.allowedHost = func(h string) bool { return h == certHost || h == confirmHost }
	cf.url = confirmServer.URL + "/confirm"

	return cf
}

func (cf *confirmFixture) envelope(t *testing.T, typ, topic string) Envelope {
	t.Helper()

	return cf.fixture.sign(t, Envelope{
		Type: typ, MessageID: "a", TopicArn: topic, Message: "x",
		Timestamp: "2026-01-01T00:00:00.000Z", Token: "t", SubscribeURL: cf.url,
	})
}

// A signed, allow-listed SubscriptionConfirmation in the queue is confirmed
// through Verifier.Confirm (the default), then deleted and counted.
func TestSQSConfirmsAllowlistedSignedSubscription(t *testing.T) {
	cf := newConfirmFixture(t)
	fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", cf.envelope(t, "SubscriptionConfirmation", testTopic))}}
	c := newConsumer(t, cf.srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))

	assert.Equal(t, 1, cf.hits)
	assert.Equal(t, []string{"h1"}, fake.deleted)
	assert.Equal(t, float64(1), value(t, cf.srv.Metrics.SQSConfirmed))
}

// A failed confirmation leaves the message for the visibility timeout and
// the redrive policy.
func TestSQSConfirmFailureKeepsMessage(t *testing.T) {
	cf := newConfirmFixture(t)
	cf.status = http.StatusInternalServerError
	fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", cf.envelope(t, "SubscriptionConfirmation", testTopic))}}
	c := newConsumer(t, cf.srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))

	assert.Equal(t, 1, cf.hits)
	assert.Empty(t, fake.deleted)
	assert.Equal(t, float64(0), value(t, cf.srv.Metrics.SQSConfirmed))
}

// A confirmation for a topic off the allow-list, or with a bad signature,
// is never followed and is kept for the dead-letter queue.
func TestSQSConfirmationRejectionsAreKept(t *testing.T) {
	cf := newConfirmFixture(t)

	offList := cf.envelope(t, "SubscriptionConfirmation", "<some other topic ARN>")
	badSig := cf.envelope(t, "SubscriptionConfirmation", testTopic)
	badSig.Signature = "AAAA"

	fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", offList), sqsMessage(t, "h2", badSig)}}
	c := newConsumer(t, cf.srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))

	assert.Zero(t, cf.hits, "the SubscribeURL must not be fetched")
	assert.Empty(t, fake.deleted)
}

// With the option off, the old behaviour: log and delete, never follow.
// An UnsubscribeConfirmation is deleted without action in either setting.
func TestSQSConfirmationsIgnoredWhenOptionOff(t *testing.T) {
	for _, off := range []bool{true, false} {
		cf := newConfirmFixture(t)
		sub := cf.envelope(t, "SubscriptionConfirmation", testTopic)
		unsub := cf.envelope(t, "UnsubscribeConfirmation", testTopic)

		fake := &fakeSQS{batch: []types.Message{sqsMessage(t, "h1", sub), sqsMessage(t, "h2", unsub)}}
		c := newConsumer(t, cf.srv, fake)

		wantHits := 1

		if off {
			c.Config.ConfirmSubscriptions = aws.Bool(false)
			wantHits = 0
		}

		require.NoError(t, c.PollOnce(context.Background()))

		assert.Equal(t, wantHits, cf.hits)
		assert.Equal(t, []string{"h1", "h2"}, fake.deleted)
	}
}

func TestSQSReceiveErrorAndAge(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)
	fake := &fakeSQS{receiveErr: errors.New("boom")}
	c := newConsumer(t, srv, fake)

	require.Error(t, c.PollOnce(context.Background()))
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSReceiveErrors))

	now := time.Unix(2_000_000, 0)
	c.Now = func() time.Time { return now }

	old := sqsMessage(t, "h1", findingEnv(fixture, t))
	old.Attributes = map[string]string{"SentTimestamp": strconv.FormatInt(now.Add(-10*time.Minute).UnixMilli(), 10)}
	young := sqsMessage(t, "h2", findingEnv(fixture, t))
	young.Attributes = map[string]string{"SentTimestamp": strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10)}

	fake.receiveErr = nil
	fake.batch = []types.Message{young, old}
	require.NoError(t, c.PollOnce(context.Background()))
	assert.InDelta(t, 600, value(t, srv.Metrics.SQSOldestAge), 0.001)

	require.NoError(t, c.PollOnce(context.Background()))
	assert.Equal(t, float64(0), value(t, srv.Metrics.SQSOldestAge), "an empty receive resets the age")
}

func TestSQSDeleteFailureIsCounted(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)
	fake := &fakeSQS{deleteErr: errors.New("denied"), batch: []types.Message{sqsMessage(t, "h1", findingEnv(fixture, t))}}
	c := newConsumer(t, srv, fake)

	require.NoError(t, c.PollOnce(context.Background()))
	assert.Equal(t, float64(1), value(t, srv.Metrics.SQSDeleteErrors))
	assert.Equal(t, float64(0), value(t, srv.Metrics.SQSDeleted))
}

// The HTTP input is unchanged by the shared pipeline: a failing
// Alertmanager is a 502 (so SNS retries), an unsubscribe confirmation is a
// 403, and a confirmation is still followed.
func TestHTTPPathUnchanged(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	srv := newTestServer(t, fixture, baseConfig(), am)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)

	srv.Alertmanager = &AlertmanagerClient{URL: down.URL, HTTPClient: down.Client()}
	assert.Equal(t, http.StatusBadGateway, post(t, srv, findingEnv(fixture, t)).Code)
	assert.Equal(t, float64(0), counterValue(t, srv, "mapped", "guardduty"), "a failed delivery is not counted")

	unsub := fixture.sign(t, Envelope{
		Type: "UnsubscribeConfirmation", MessageID: "b", TopicArn: testTopic, Message: "x",
		Timestamp: "2026-01-01T00:00:00.000Z", Token: "t", SubscribeURL: "https://example.com/x",
	})
	assert.Equal(t, http.StatusForbidden, post(t, srv, unsub).Code)
	assert.Equal(t, float64(1), value(t, srv.Metrics.Rejected.WithLabelValues(ReasonType)))
	assert.Equal(t, float64(0), value(t, srv.Metrics.SQSReceived), "the queue series stay untouched on the HTTP path")
}

func TestInputConfig(t *testing.T) {
	var in Input
	require.NoError(t, in.validate())
	assert.Equal(t, ModeHTTP, in.Mode)
	assert.True(t, in.HTTP())
	assert.False(t, in.UsesSQS())

	in = Input{Mode: "sqs", SQS: SQSConfig{QueueURL: "https://sqs.eu-west-1.amazonaws.com/ACCOUNT/alerts"}}
	require.NoError(t, in.validate())
	assert.Equal(t, "eu-west-1", in.SQS.Region)
	assert.Equal(t, 20, in.SQS.WaitTimeSeconds)
	assert.Equal(t, 10, in.SQS.MaxMessages)
	assert.Equal(t, 60, in.SQS.VisibilityTimeoutSeconds)
	assert.Equal(t, 1, in.SQS.Concurrency)
	assert.False(t, in.HTTP())

	assert.ErrorContains(t, (&Input{Mode: "sqs"}).validate(), "queueURL")
	assert.ErrorContains(t, (&Input{Mode: "both", SQS: SQSConfig{QueueURL: "http://localhost:4566/q"}}).validate(), "region")
	assert.ErrorContains(t, (&Input{Mode: "sqs", SQS: SQSConfig{QueueURL: "https://sqs.eu-west-1.amazonaws.com/0/q", MaxMessages: 11}}).validate(), "maxMessages")
	assert.ErrorContains(t, (&Input{Mode: "nope"}).validate(), "input.mode")
}

// lockedBuffer is a log sink safe for concurrent use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
