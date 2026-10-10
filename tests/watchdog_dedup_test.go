// Exactly one Watchdog, from exactly one source.
//
// `victoria-metrics-k8s-stack` carries its own sync job, which fetches
// rule sources over the network and applies them to the cluster
// DIRECTLY — a mechanism invisible to `helm template` and to every
// golden render in this repository. That vendored default rule set is
// ON by default here, deliberately: it is where several rules with no
// `charts/platform-alerts` equivalent come from, and where this
// install's `Watchdog` alert comes from too, in its `general.rules`
// group — the same alert name `templates/watchdog.yaml` would render
// for this chart's own metrics alerter, if that template did not defer
// to the vendored one (`observability-stack.vendoredWatchdogPresent` in
// _helpers.tpl).
//
// A render-time test cannot see the vendored rule's CONTENT — the sync
// job fetches it over the network, at apply time, never at render time —
// so what it CAN check is the other side of the same fact: the sync
// job's own rendered config either asks for rule sources at all (the
// vendored Watchdog is presumed present) or it does not (this chart's
// own template is the only place a Watchdog could come from). Between
// those two, on every install with vmalert's metrics alerter running at
// all, there should be exactly one.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// syncJobConfigMap is the part of victoria-metrics-k8s-stack's own
// rendered ConfigMap this test cares about: `data["config.yaml"]` is
// itself a YAML document, embedded as a string, so it is decoded twice.
type syncJobConfigMap struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data struct {
		ConfigYAML string `yaml:"config.yaml"`
	} `yaml:"data"`
}

type syncJobConfig struct {
	Rules struct {
		Sources []any `yaml:"sources"`
	} `yaml:"rules"`
}

// vmalertDocForWatchdog is the minimum needed to tell whether a golden
// renders a metrics alerter at all — a case with no vmalert (like
// tests/cases/observability-stack/tenancy) has no expectation of a
// Watchdog from either source, and is skipped rather than asserted on.
type vmalertDocForWatchdog struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// TestExactlyOneWatchdogSourcePerInstall is the property the coordinator
// asked for directly: not "this chart's own template renders it once"
// in isolation, but "the install ends up with exactly one Watchdog",
// counting the vendored source this render cannot see the content of —
// only whether it was asked for at all.
func TestExactlyOneWatchdogSourcePerInstall(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checked int

	for _, g := range goldens {
		var (
			hasMetricsAlerter    bool
			chartWatchdogCount   int
			vendoredSourcesFound bool
			vendoredNonEmpty     bool
		)

		for _, doc := range splitDocs(t, g) {
			var a vmalertDocForWatchdog
			if err := yaml.Unmarshal(doc, &a); err == nil && a.Kind == "VMAlert" &&
				!strings.HasSuffix(a.Metadata.Name, "-logs") {
				hasMetricsAlerter = true
			}

			var rule vmRuleDoc
			if err := yaml.Unmarshal(doc, &rule); err == nil && rule.Kind == "VMRule" {
				for _, group := range rule.Spec.Groups {
					for _, r := range group.Rules {
						if r.Alert == "Watchdog" {
							chartWatchdogCount++
						}
					}
				}
			}

			var cm syncJobConfigMap
			if err := yaml.Unmarshal(doc, &cm); err == nil && cm.Kind == "ConfigMap" &&
				strings.HasSuffix(cm.Metadata.Name, "-sync-job-config") {
				vendoredSourcesFound = true

				var cfg syncJobConfig
				require.NoErrorf(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg),
					"%s: %s's own config.yaml does not parse as YAML", g, cm.Metadata.Name)
				vendoredNonEmpty = len(cfg.Rules.Sources) > 0
			}
		}

		assert.LessOrEqualf(t, chartWatchdogCount, 1, "%s: this chart's own template rendered %d "+
			"Watchdog VMRules — templates/watchdog.yaml is supposed to render at most one", g, chartWatchdogCount)

		// Non-empty sources ⇒ chart watchdog absent: the vendored rule
		// set is presumed to be supplying one already, so this
		// chart's own template must have deferred to it.
		if vendoredNonEmpty {
			assert.Zerof(t, chartWatchdogCount, "%s: the vendored sync job's rule sources are "+
				"non-empty (it will supply a Watchdog from its own general.rules group) AND this "+
				"chart's own template also rendered one — two Watchdogs, the duplicate this design "+
				"exists to prevent", g)
		}

		if !hasMetricsAlerter {
			continue // no metrics alerter, no Watchdog expected from either source
		}

		checked++

		// `upstreamRules.source: platform-alerts` (its preset): the third
		// source. The stack renders no Watchdog and its sync job has no rule
		// source; the Watchdog is the vendored pack's `general.rules`, which
		// charts/platform-alerts renders (its `upstream-pack` golden carries it).
		if packOwnsWatchdog[strings.TrimSuffix(filepath.Base(g), ".yaml")] {
			assert.Zerof(t, chartWatchdogCount, "%s: the pack owns the Watchdog, but this chart rendered one too", g)
			assert.Falsef(t, vendoredNonEmpty, "%s: the pack owns the Watchdog, but the sync job still has rule sources", g)

			continue
		}

		gotOne := chartWatchdogCount + boolToInt(vendoredNonEmpty)
		assert.Equalf(t, 1, gotOne, "%s: %d Watchdog sources (chart template + vendored rule set "+
			"presumed present) — a metrics alerter should always end up with exactly one, from "+
			"whichever source observability-stack.vendoredWatchdogPresent decided owns it", g, gotOne)

		if vendoredSourcesFound && !vendoredNonEmpty {
			// The vendored set was explicitly turned off for this case
			// (tests/cases/observability-stack/vendored-rules-off) —
			// the chart's own template is the ONLY source, and the
			// assertion above already required it to be exactly one.
			assert.Equalf(t, 1, chartWatchdogCount, "%s: the vendored rule set is off and this "+
				"chart's own template did not pick up the Watchdog", g)
		}
	}

	assert.Positive(t, checked, "no golden with a metrics alerter was checked; this test went blind rather than passing")
}

// packOwnsWatchdog names the stack goldens whose Watchdog comes from the
// vendored pack in charts/platform-alerts rather than from the sync job or
// this chart's own template.
var packOwnsWatchdog = map[string]bool{"upstream-rules-platform-alerts": true}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
