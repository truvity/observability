package lambdaext

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigReadsOldNames(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{
		"ACCESS_ROSTER_ISSUER": "https://issuer.example/", "SLUIS_AUDIENCE": "aud",
		"SLUIS_OTLP_ENDPOINT": "https://otlp.example", "ACCESS_ROSTER_STS_ALGORITHM": "RS256",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issuer != "https://issuer.example" || cfg.Audience != "aud" || cfg.Algorithm != "RS256" ||
		cfg.Endpoint != "https://otlp.example" {
		t.Fatalf("old names not read: %+v", cfg)
	}
	want := []string{"ACCESS_ROSTER_ISSUER", "ACCESS_ROSTER_STS_ALGORITHM", "SLUIS_AUDIENCE", "SLUIS_OTLP_ENDPOINT"}
	if !slices.Equal(cfg.Deprecated, want) {
		t.Fatalf("Deprecated = %v, want %v", cfg.Deprecated, want)
	}
}

func TestConfigNewNameWinsAndIsSilent(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{
		"OTLP_LAMBDA_ISSUER": "https://new.example", "SLUIS_ISSUER": "https://sluis.example", "ACCESS_ROSTER_ISSUER": "https://old.example",
		"OTLP_LAMBDA_AUDIENCE": "aud", "OTLP_LAMBDA_ENDPOINT": "https://otlp.example",
		"OTLP_LAMBDA_STS_ALGORITHM": "  ", "SLUIS_STS_ALGORITHM": "RS256", // a blank new name falls back
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issuer != "https://new.example" {
		t.Fatalf("an old name won: %q", cfg.Issuer)
	}
	if cfg.Algorithm != "RS256" || !slices.Equal(cfg.Deprecated, []string{"SLUIS_STS_ALGORITHM"}) {
		t.Fatalf("blank new name: algorithm %q, deprecated %v", cfg.Algorithm, cfg.Deprecated)
	}
}

func TestSluisNameBeatsAccessRosterName(t *testing.T) {
	cfg, _ := LoadConfig(envMap(map[string]string{"SLUIS_ISSUER": "https://sluis.example", "ACCESS_ROSTER_ISSUER": "https://old.example"}))
	if cfg.Issuer != "https://sluis.example" || !slices.Equal(cfg.Deprecated, []string{"SLUIS_ISSUER"}) {
		t.Fatalf("got %q, %v", cfg.Issuer, cfg.Deprecated)
	}
}

func TestTelemetryConfigOldNames(t *testing.T) {
	tc, err := LoadTelemetryConfig(envMap(map[string]string{
		"ACCESS_ROSTER_FUNCTION_LOGS": "false", "SLUIS_TELEMETRY_BUFFER_QUEUE_ITEMS": "200",
		"OTLP_LAMBDA_PLATFORM_LOGS": "false", "ACCESS_ROSTER_PLATFORM_LOGS": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if tc.Function || tc.Platform || tc.QueueItems != 200 {
		t.Fatalf("got %+v", tc)
	}
	want := []string{"ACCESS_ROSTER_FUNCTION_LOGS", "SLUIS_TELEMETRY_BUFFER_QUEUE_ITEMS"}
	if !slices.Equal(tc.Deprecated, want) {
		t.Fatalf("Deprecated = %v, want %v", tc.Deprecated, want)
	}
}

func TestWarnDeprecatedNamesNoValues(t *testing.T) {
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) }
	cfg, _ := LoadConfig(envMap(map[string]string{
		"ACCESS_ROSTER_ISSUER": "https://secret-value.example", "SLUIS_OTLP_ENDPOINT": "https://secret2.example",
	}))
	warnDeprecated(logf, cfg.Deprecated)
	warnDeprecated(logf, nil)
	if len(lines) != 2 ||
		!strings.Contains(lines[0], "ACCESS_ROSTER_ISSUER") || !strings.Contains(lines[0], "OTLP_LAMBDA_ISSUER") ||
		!strings.Contains(lines[1], "SLUIS_OTLP_ENDPOINT") || !strings.Contains(lines[1], "OTLP_LAMBDA_ENDPOINT") ||
		strings.Contains(strings.Join(lines, ""), "secret") {
		t.Fatalf("warnings: %q", lines)
	}
}

func TestTelemetryConfigTrimsValues(t *testing.T) {
	tc, err := LoadTelemetryConfig(envMap(map[string]string{
		"OTLP_LAMBDA_FUNCTION_LOGS": " false ", "OTLP_LAMBDA_TELEMETRY_BUFFER_TIMEOUT_MS": " 250\n",
		"OTLP_LAMBDA_TELEMETRY_LISTEN": " 127.0.0.1:4243 ",
	}))
	if err != nil || tc.Function || tc.TimeoutMs != 250 || tc.Listen != "127.0.0.1:4243" {
		t.Fatalf("got %+v, %v", tc, err)
	}
}

func TestUpstreamErrorsPrintNoCredentials(t *testing.T) {
	for _, raw := range []string{"http://user:pa55word@otlp.example/path?token=s3cret", "ftp://user:pa55word@otlp.example"} {
		err := checkUpstream(raw)
		if err == nil || strings.Contains(err.Error(), "pa55word") || strings.Contains(err.Error(), "s3cret") ||
			!strings.Contains(err.Error(), "otlp.example") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if err := checkUpstream("://pa55word"); err == nil || strings.Contains(err.Error(), "pa55word") {
		t.Errorf("unparsable: %v", err)
	}
}

func TestCurrentNameIsTheInverseOfOldNames(t *testing.T) {
	for _, name := range []string{EnvIssuer, EnvOTLPEndpoint, EnvOTLPAudience, EnvFunctionLogs} {
		for _, old := range oldNames(name) {
			if got := currentName(old); got != name {
				t.Errorf("currentName(%s) = %s, want %s", old, got, name)
			}
		}
	}
}

func TestCutDoesNotPinTheWholeLine(t *testing.T) {
	line := strings.Repeat("x", 4<<20)
	body, short := cut(line, maxLineBytes)
	if !short || len(body) != maxLineBytes {
		t.Fatalf("cut: %d bytes, short %v", len(body), short)
	}
	if unsafe.StringData(body) == unsafe.StringData(line) {
		t.Fatal("the cut body shares the original's backing array")
	}
	rec := &LogRecord{}
	setText(rec, line)
	if unsafe.StringData(rec.Body) == unsafe.StringData(line) {
		t.Fatal("setText keeps the whole line alive")
	}
}

func TestRedactErrKeepsNoUserOrQuery(t *testing.T) {
	srv := httptest.NewServer(nil)
	addr := srv.Listener.Addr().String()
	srv.Close() // nothing listens: Do fails with a *url.Error
	req, _ := http.NewRequest(http.MethodPost, "http://alice:pw@"+addr+"/v1/logs?token=s3cret", nil)
	_, err := http.DefaultClient.Do(req)
	if err == nil {
		t.Fatal("expected a failure")
	}
	got := redactErr(err).Error()
	for _, leak := range []string{"alice", "s3cret", "pw@", "token"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaks %q", got, leak)
		}
	}
	if !strings.Contains(got, addr) || errors.Unwrap(redactErr(err)) == nil {
		t.Errorf("%q lost the host or the cause", got)
	}
}
