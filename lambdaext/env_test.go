package lambdaext

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigReadsLegacyNames(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{
		"ACCESS_ROSTER_ISSUER": "https://issuer.example/", "ACCESS_ROSTER_AUDIENCE": "aud",
		"ACCESS_ROSTER_OTLP_ENDPOINT": "https://otlp.example", "ACCESS_ROSTER_STS_ALGORITHM": "RS256",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issuer != "https://issuer.example" || cfg.Audience != "aud" || cfg.Algorithm != "RS256" {
		t.Fatalf("legacy names not read: %+v", cfg)
	}
	want := []string{"ACCESS_ROSTER_AUDIENCE", "ACCESS_ROSTER_ISSUER", "ACCESS_ROSTER_OTLP_ENDPOINT", "ACCESS_ROSTER_STS_ALGORITHM"}
	if !slices.Equal(cfg.Deprecated, want) {
		t.Fatalf("Deprecated = %v, want %v", cfg.Deprecated, want)
	}
}

func TestConfigNewNameWinsAndIsSilent(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{
		"SLUIS_ISSUER": "https://new.example", "ACCESS_ROSTER_ISSUER": "https://old.example",
		"SLUIS_AUDIENCE": "aud", "SLUIS_OTLP_ENDPOINT": "https://otlp.example",
		"SLUIS_STS_ALGORITHM": "  ", "ACCESS_ROSTER_STS_ALGORITHM": "RS256", // a blank new name falls back
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issuer != "https://new.example" {
		t.Fatalf("the old name won: %q", cfg.Issuer)
	}
	if cfg.Algorithm != "RS256" || !slices.Equal(cfg.Deprecated, []string{"ACCESS_ROSTER_STS_ALGORITHM"}) {
		t.Fatalf("blank new name: algorithm %q, deprecated %v", cfg.Algorithm, cfg.Deprecated)
	}
}

func TestTelemetryConfigLegacyNames(t *testing.T) {
	tc, err := LoadTelemetryConfig(envMap(map[string]string{
		"ACCESS_ROSTER_FUNCTION_LOGS": "true", "ACCESS_ROSTER_TELEMETRY_BUFFER_QUEUE_ITEMS": "200",
		"SLUIS_PLATFORM_LOGS": "false", "ACCESS_ROSTER_PLATFORM_LOGS": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !tc.Function || tc.Platform || tc.QueueItems != 200 {
		t.Fatalf("got %+v", tc)
	}
	want := []string{"ACCESS_ROSTER_FUNCTION_LOGS", "ACCESS_ROSTER_TELEMETRY_BUFFER_QUEUE_ITEMS"}
	if !slices.Equal(tc.Deprecated, want) {
		t.Fatalf("Deprecated = %v, want %v", tc.Deprecated, want)
	}
}

func TestWarnDeprecatedNamesNoValues(t *testing.T) {
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) }
	cfg, _ := LoadConfig(envMap(map[string]string{"ACCESS_ROSTER_ISSUER": "https://secret-value.example"}))
	warnDeprecated(logf, cfg.Deprecated)
	warnDeprecated(logf, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], "ACCESS_ROSTER_ISSUER") || !strings.Contains(lines[0], "SLUIS_ISSUER") ||
		strings.Contains(lines[0], "secret-value") {
		t.Fatalf("warnings: %q", lines)
	}
}
