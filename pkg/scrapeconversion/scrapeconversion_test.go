// Package scrapeconversion holds one regression test: that the vendored
// VictoriaMetrics operator's Prometheus-Operator converter is actually on
// for the two kinds charts/observability-stack and
// charts/observability-emitters render (ServiceMonitor, PodMonitor), and
// still off for the four this repository has no stake in.
//
// See docs/safety.md, "Scrape objects are always the Prometheus Operator
// kinds", and this release's CHANGELOG entry: `helm template` rendering a
// ServiceMonitor cleanly, with `just golden` matching it byte for byte,
// is not evidence anything scrapes it. Only vmagent's own rendered
// config — or, short of a live cluster, the operator env this test reads
// — can tell the two apart.
package scrapeconversion

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The converters this stack depends on: it renders a ServiceMonitor of
// its own (the metrics store's selfscrape, the log and trace stores'
// serviceMonitor) and charts/observability-emitters renders a PodMonitor
// for every agent it deploys. `true` means the golden render must NOT
// carry an explicit "false" for that name — absent is fine, because the
// operator's own default is enabled.
//
// The remaining four are converters this stack declines on purpose:
// nothing here authors a Probe, ScrapeConfig, PrometheusRule or
// AlertmanagerConfig, and converting one that belongs to an unrelated
// chart or team — on an estate that runs its own Prometheus Operator, or
// a second VictoriaMetrics operator instance — is the exact
// two-controllers-fighting-over-one-scrape failure
// `disable_prometheus_converter` originally existed to prevent. `false`
// means the golden render MUST carry an explicit "false".
var wantConverted = map[string]bool{
	"VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE":      true,
	"VM_ENABLEDPROMETHEUSCONVERTER_PODMONITOR":         true,
	"VM_ENABLEDPROMETHEUSCONVERTER_PROBE":              false,
	"VM_ENABLEDPROMETHEUSCONVERTER_SCRAPECONFIG":       false,
	"VM_ENABLEDPROMETHEUSCONVERTER_PROMETHEUSRULE":     false,
	"VM_ENABLEDPROMETHEUSCONVERTER_ALERTMANAGERCONFIG": false,
}

// TestOperatorConvertsTheKindsThisRepositoryRenders reads the CHECKED-IN
// golden renders under tests/golden/observability-stack rather than
// invoking `helm` itself, so a regression shows up two ways: a golden
// diff nobody explained, AND this test failing on it. `just golden`
// happily regenerates a golden file for a values.yaml that turns a
// converter back off — a byte-identical diff with itself proves nothing
// about what vmagent then sees. This test asserts the values, not merely
// that a golden file agrees with its own regeneration.
func TestOperatorConvertsTheKindsThisRepositoryRenders(t *testing.T) {
	golden, err := filepath.Glob(filepath.Join("..", "..", "tests", "golden", "observability-stack", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, golden, "no observability-stack golden files found; this test would pass vacuously")

	checked := 0
	for _, g := range golden {
		g := g
		t.Run(filepath.Base(g), func(t *testing.T) {
			env := operatorEnv(t, g)
			if env == nil {
				// This case's values.yaml disables
				// victoria-metrics-k8s-stack entirely (e.g. the tenancy
				// cases, which render only the proxy) — nothing to
				// assert here, not a pass this test should count.
				t.Skip("no victoria-metrics-operator Deployment in this golden render (victoria-metrics-k8s-stack.enabled: false in this case)")
			}
			checked++

			for name, mustConvert := range wantConverted {
				value, isSet := env[name]
				explicitlyOff := isSet && value == "false"
				if mustConvert {
					assert.Falsef(t, explicitlyOff,
						"%s: %s is explicitly \"false\" — the operator will not convert a kind this repository renders as a Prometheus-Operator object, and vmagent (which only watches the native VictoriaMetrics kinds) will never see a target for it",
						filepath.Base(g), name)
				} else {
					assert.Truef(t, explicitlyOff,
						"%s: %s is not explicitly \"false\" — this stack renders no object of this kind, and leaving its converter on picks up ServiceMonitor/PodMonitor/etc. objects an unrelated chart or team owns",
						filepath.Base(g), name)
				}
			}
		})
	}

	assert.Positivef(t, checked, "every observability-stack golden case disables victoria-metrics-k8s-stack; this test would pass vacuously")
}

// operatorEnv finds the victoria-metrics-operator Deployment in a
// rendered multi-document manifest and returns its first container's env
// as a name -> value map. Returns nil if no such Deployment is in the
// stream — a case whose values.yaml sets
// `victoria-metrics-k8s-stack.enabled: false`, which the caller skips
// rather than asserting against.
func operatorEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	type container struct {
		Env []struct {
			Name  string `yaml:"name"`
			Value string `yaml:"value"`
		} `yaml:"env"`
	}
	type manifest struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []container `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc manifest
		if err := dec.Decode(&doc); err != nil {
			return nil
		}
		if doc.Kind != "Deployment" || !strings.Contains(doc.Metadata.Name, "victoria-metrics-operator") {
			continue
		}
		if len(doc.Spec.Template.Spec.Containers) == 0 {
			continue
		}
		out := make(map[string]string, len(doc.Spec.Template.Spec.Containers[0].Env))
		for _, e := range doc.Spec.Template.Spec.Containers[0].Env {
			out[e.Name] = e.Value
		}
		return out
	}
}
