package lambdaext

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
