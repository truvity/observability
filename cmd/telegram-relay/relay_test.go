// End to end through the real mux, the real handler, real files on disk and
// the real Telegram client, against a fake Bot API that records what it was
// sent. Only GoAlert is simulated, by the documents its webhook sender
// writes (notification/webhook/sender.go upstream).
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testToken  = "123456:test-bot-token"
	testSecret = "a-shared-secret-of-enough-length"
)

type sent struct {
	Path string
	Body sendMessage
}

// fakeTelegram is the Bot API: it records every sendMessage and answers
// with whatever status/body the test set.
type fakeTelegram struct {
	mu     sync.Mutex
	got    []sent
	status int
	body   string
	delay  time.Duration
	server *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{status: http.StatusOK, body: `{"ok":true,"result":{}}`}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(f.delay)
		var m sendMessage
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &m)
		f.mu.Lock()
		f.got = append(f.got, sent{Path: r.URL.Path, Body: m})
		status, body := f.status, f.body
		f.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelegram) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.got...)
}

type harness struct {
	t       *testing.T
	dir     string
	tg      *fakeTelegram
	metrics *Metrics
	server  *httptest.Server
	logs    *strings.Builder
	secrets FileSecrets
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, tg: newFakeTelegram(t), logs: &strings.Builder{}}
	h.secrets = FileSecrets{
		BotTokenFile:     filepath.Join(dir, "bot-token"),
		SharedSecretFile: filepath.Join(dir, "shared-secret"),
		RecipientsFile:   filepath.Join(dir, "recipients.yaml"),
	}
	h.write("bot-token", testToken+"\n")
	h.write("shared-secret", testSecret+"\n")
	h.write("recipients.yaml", "alice: 1001\nbob: 1002\n")

	h.metrics = NewMetrics(prometheus.NewRegistry())
	relay := &Relay{
		Secrets:  h.secrets,
		Telegram: &TelegramClient{BaseURL: h.tg.server.URL, HTTP: h.tg.server.Client()},
		Timeout:  500 * time.Millisecond,
		Metrics:  h.metrics,
		Logger:   slog.New(slog.NewJSONHandler(h.logs, nil)),
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/notify/{recipient}", relay)
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) write(name, content string) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, name), []byte(content), 0o600))
}

func (h *harness) post(path string, payload any, mutate func(*http.Request)) *http.Response {
	h.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(h.t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.server.URL+path, strings.NewReader(string(raw)))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	resp, err := h.server.Client().Do(req)
	require.NoError(h.t, err)
	_ = resp.Body.Close()
	return resp
}

func (h *harness) count(typ, result string) float64 {
	return value(h.t, h.metrics.Notifications.WithLabelValues(typ, result))
}

// value reads one counter or gauge.
func value(t *testing.T, c prometheus.Metric) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	return m.Gauge.GetValue()
}

func alertPayload() map[string]any {
	return map[string]any{
		"AppName":     "GoAlert",
		"Type":        "Alert",
		"AlertID":     42,
		"Summary":     "KubePodCrashLooping <prod>",
		"Details":     "pod x & y restarting",
		"ServiceID":   "svc-1",
		"ServiceName": "Platform",
		"Meta":        map[string]string{},
		"GoAlertURL":  "https://oncall.example/alerts/42",
	}
}

func TestAlertDeliveredToTheRecipientsChat(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/v1/notify/alice?token="+testSecret, alertPayload(), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	msgs := h.tg.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "/bot"+testToken+"/sendMessage", msgs[0].Path)
	assert.Equal(t, int64(1001), msgs[0].Body.ChatID)
	assert.Equal(t, "HTML", msgs[0].Body.ParseMode)
	assert.True(t, msgs[0].Body.LinkPreviewOptions.IsDisabled)
	assert.Contains(t, msgs[0].Body.Text, "alert #42")
	assert.Contains(t, msgs[0].Body.Text, "KubePodCrashLooping &lt;prod&gt;")
	assert.Contains(t, msgs[0].Body.Text, "pod x &amp; y restarting")
	assert.Contains(t, msgs[0].Body.Text, `<a href="https://oncall.example/alerts/42">`)
	assert.Equal(t, 1.0, h.count("Alert", resultDelivered))
	assert.Positive(t, value(t, h.metrics.LastDelivered))
}

func TestSecretAcceptedAsBasicAuthAndBearer(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/v1/notify/bob", alertPayload(), func(r *http.Request) { r.SetBasicAuth("relay", testSecret) })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp = h.post("/v1/notify/bob", alertPayload(), func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testSecret) })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, h.tg.messages(), 2)
}

func TestRefusesWithoutTheSecret(t *testing.T) {
	h := newHarness(t)
	for name, mutate := range map[string]func(*http.Request){
		"none":         nil,
		"wrong query":  func(r *http.Request) { r.URL.RawQuery = "token=nope" },
		"wrong basic":  func(r *http.Request) { r.SetBasicAuth("relay", "nope") },
		"wrong bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
	} {
		resp := h.post("/v1/notify/alice", alertPayload(), mutate)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, name)
	}
	// An unauthenticated caller learns nothing about recipients: an unknown
	// one is refused the same way.
	resp := h.post("/v1/notify/nobody", alertPayload(), nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, h.tg.messages())
	assert.Equal(t, 5.0, h.count("unknown", resultUnauthorized))
}

func TestUnknownRecipientIsRefusedNotGuessed(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/v1/notify/carol?token="+testSecret, alertPayload(), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Empty(t, h.tg.messages())
	assert.Equal(t, 1.0, h.count("Alert", resultUnknownRecipient))
}

func TestRecipientAddedWithoutRestart(t *testing.T) {
	h := newHarness(t)
	h.write("recipients.yaml", "alice: 1001\ncarol: 1003\n")
	resp := h.post("/v1/notify/carol?token="+testSecret, alertPayload(), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int64(1003), h.tg.messages()[0].Body.ChatID)
}

func TestUnsupportedTypeIsRefused(t *testing.T) {
	h := newHarness(t)
	resp := h.post("/v1/notify/alice?token="+testSecret, map[string]any{"Type": "SomethingNew"}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 1.0, h.count("unknown", resultBadRequest))
	assert.Empty(t, h.tg.messages())
}

func TestTelegramRefusalIsCountedAndTokenNeverLogged(t *testing.T) {
	h := newHarness(t)
	h.tg.status = http.StatusForbidden
	h.tg.body = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
	resp := h.post("/v1/notify/alice?token="+testSecret, alertPayload(), nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, 1.0, h.count("Alert", resultTelegramError))
	assert.Contains(t, h.logs.String(), "bot was blocked by the user")
	assert.Contains(t, h.logs.String(), "press Start")
	assert.NotContains(t, h.logs.String(), testToken)
	assert.NotContains(t, h.logs.String(), testSecret)
}

func TestTelegramTimeoutIsCountedAndTokenNeverLogged(t *testing.T) {
	h := newHarness(t)
	h.tg.delay = time.Second
	resp := h.post("/v1/notify/alice?token="+testSecret, alertPayload(), nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, 1.0, h.count("Alert", resultTelegramError))
	assert.NotContains(t, h.logs.String(), testToken)
}

func TestMissingSecretFileIsAConfigError(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, os.Remove(h.secrets.BotTokenFile))
	resp := h.post("/v1/notify/alice?token="+testSecret, alertPayload(), nil)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, 1.0, h.count("unknown", resultConfigError))
	assert.Error(t, h.secrets.Check())
}

func TestOversizedBodyIsRefused(t *testing.T) {
	h := newHarness(t)
	p := alertPayload()
	p["Details"] = strings.Repeat("x", maxBody)
	resp := h.post("/v1/notify/alice?token="+testSecret, p, nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	assert.Empty(t, h.tg.messages())
}
