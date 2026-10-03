package lambdaext_test

import (
	"testing"

	"github.com/truvity/observability/lambdaext"
)

// SLUIS_* is the name; ACCESS_ROSTER_* is the name it had before the issuer
// was renamed to sluis, and still works. When both are set the SLUIS_* one wins.
func TestConfigReadsSluisNamesAndFallsBackToTheOldOnes(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		env  map[string]string
		want lambdaext.Config
	}{
		"only the SLUIS_ names": {
			env: map[string]string{
				"SLUIS_ISSUER": "https://new.example/", "SLUIS_AUDIENCE": "aud-new",
				"SLUIS_OTLP_ENDPOINT": "https://otlp-new.example/", "SLUIS_LISTEN": "127.0.0.1:1111",
				"SLUIS_OTLP_AUDIENCE": "otlp-new", "SLUIS_STS_ALGORITHM": "RS256",
				"SLUIS_STS_DURATION_SECONDS": "120", "SLUIS_TOKEN_FILE": "/tmp/new",
			},
			want: lambdaext.Config{
				Issuer: "https://new.example", Audience: "aud-new", Endpoint: "https://otlp-new.example",
				Listen: "127.0.0.1:1111", OTLPAudience: "otlp-new", Algorithm: "RS256", Duration: 120, TokenFile: "/tmp/new",
			},
		},
		"only the ACCESS_ROSTER_ names": {
			env: map[string]string{
				"ACCESS_ROSTER_ISSUER": "https://old.example/", "ACCESS_ROSTER_AUDIENCE": "aud-old",
				"ACCESS_ROSTER_OTLP_ENDPOINT": "https://otlp-old.example/", "ACCESS_ROSTER_LISTEN": "127.0.0.1:2222",
				"ACCESS_ROSTER_OTLP_AUDIENCE": "otlp-old", "ACCESS_ROSTER_STS_ALGORITHM": "RS256",
				"ACCESS_ROSTER_STS_DURATION_SECONDS": "180", "ACCESS_ROSTER_TOKEN_FILE": "/tmp/old",
			},
			want: lambdaext.Config{
				Issuer: "https://old.example", Audience: "aud-old", Endpoint: "https://otlp-old.example",
				Listen: "127.0.0.1:2222", OTLPAudience: "otlp-old", Algorithm: "RS256", Duration: 180, TokenFile: "/tmp/old",
			},
		},
		"both, SLUIS_ wins, and the rest come from the old names": {
			env: map[string]string{
				"SLUIS_ISSUER": "https://new.example", "ACCESS_ROSTER_ISSUER": "https://old.example",
				"ACCESS_ROSTER_AUDIENCE":      "aud-old",
				"SLUIS_OTLP_ENDPOINT":         "https://otlp-new.example",
				"ACCESS_ROSTER_OTLP_ENDPOINT": "https://otlp-old.example",
				"SLUIS_LISTEN":                "",
				"ACCESS_ROSTER_LISTEN":        "127.0.0.1:3333",
			},
			want: lambdaext.Config{
				Issuer: "https://new.example", Audience: "aud-old", Endpoint: "https://otlp-new.example",
				Listen: "127.0.0.1:3333", OTLPAudience: "otlp", Algorithm: "ES384", Duration: 300,
			},
		},
	} {
		got, err := lambdaext.LoadConfig(func(k string) string { return tc.env[k] })
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, tc.want)
		}
	}
}

func TestTelemetryConfigReadsSluisNamesAndFallsBackToTheOldOnes(t *testing.T) {
	t.Parallel()

	load := func(env map[string]string) lambdaext.TelemetryConfig {
		t.Helper()
		c, err := lambdaext.LoadTelemetryConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	if c := load(map[string]string{"SLUIS_FUNCTION_LOGS": "true", "SLUIS_TELEMETRY_BUFFER_TIMEOUT_MS": "250"}); !c.Function || c.TimeoutMs != 250 {
		t.Errorf("SLUIS_ names: %+v", c)
	}
	if c := load(map[string]string{"ACCESS_ROSTER_FUNCTION_LOGS": "true", "ACCESS_ROSTER_TELEMETRY_BUFFER_TIMEOUT_MS": "300"}); !c.Function || c.TimeoutMs != 300 {
		t.Errorf("ACCESS_ROSTER_ names: %+v", c)
	}
	// Both set: the SLUIS_ value wins, including turning a setting off.
	if c := load(map[string]string{
		"SLUIS_PLATFORM_LOGS": "false", "ACCESS_ROSTER_PLATFORM_LOGS": "true",
		"SLUIS_TELEMETRY_BUFFER_TIMEOUT_MS": "400", "ACCESS_ROSTER_TELEMETRY_BUFFER_TIMEOUT_MS": "300",
	}); c.Platform || c.TimeoutMs != 400 {
		t.Errorf("both set: %+v", c)
	}
	// A bad value is reported under the name that was asked for.
	if _, err := lambdaext.LoadTelemetryConfig(func(k string) string {
		if k == "ACCESS_ROSTER_TELEMETRY_BUFFER_MAX_ITEMS" {
			return "5"
		}
		return ""
	}); err == nil {
		t.Error("a value outside its bounds under the old name must still be refused")
	}
}
