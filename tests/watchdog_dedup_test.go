// One Watchdog, not two.
//
// `victoria-metrics-k8s-stack` carries its own sync job, which fetches
// rule sources over the network and applies them to the cluster
// DIRECTLY — a mechanism invisible to `helm template` and to every
// golden render in this repository. One of those sources is
// kube-prometheus's own combined rule manifest, and its `general.rules`
// group carries a `Watchdog` alert of its own — the same alert name
// `templates/watchdog.yaml` renders for this chart's own metrics
// alerter. `victoria-metrics-k8s-stack.defaultRules.create: false`
// looked like the switch that kept the vendored one off and was not
// one: the sync job's own gate is `defaultRules.enabled` OR
// `defaultRules.create`, and `enabled` defaults to `true` upstream, so
// every source with no per-source override — that manifest among them —
// was still fetched and applied live on every install this chart's own
// golden renders represent.
//
// This file holds both sides of the fix: that this chart's own Watchdog
// renders at most once per install (the property the coordinator asked
// for directly), and that the vendored rule set that could have
// supplied a second one is verifiably inert — the only half of this
// that a render-time test can see at all, since the vendored one is
// never a static object `helm template` produces in the first place.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestAtMostOneWatchdogPerInstall walks every rendered VMRule in every
// observability-stack golden and fails if more than one `alert: Watchdog`
// rule appears — whatever chart, template or vendored source it came
// from. `vmRuleDoc` is the same type tests/selfalerts_test.go already
// unmarshals rendered rules into.
func TestAtMostOneWatchdogPerInstall(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var sawAny bool

	for _, g := range goldens {
		var count int

		for _, doc := range splitDocs(t, g) {
			var rule vmRuleDoc
			if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
				continue
			}
			for _, group := range rule.Spec.Groups {
				for _, r := range group.Rules {
					if r.Alert == "Watchdog" {
						count++
					}
				}
			}
		}

		if count > 0 {
			sawAny = true
		}

		assert.LessOrEqualf(t, count, 1, "%s: %d `Watchdog` alerts rendered — this chart's own "+
			"(templates/watchdog.yaml) plus a second one is the exact duplicate the vendored "+
			"sync job's default rule set produces when it is not fully turned off", g, count)
	}

	assert.True(t, sawAny, "no golden rendered a Watchdog alert at all; this test went blind rather than passing")
}

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
		Groups  map[string]any `yaml:"groups"`
		Sources []any          `yaml:"sources"`
	} `yaml:"rules"`
}

// TestVendoredDefaultRulesNeverPopulateASource is the other half: proof
// that `victoria-metrics-k8s-stack.defaultRules.enabled: false` (this
// chart's own default, guarded by
// observability-stack.validate.vendoredRules) actually reaches the sync
// job's own config with an empty rule source list, on every golden this
// repository renders — the only place this mechanism is visible to
// `helm template` at all.
func TestVendoredDefaultRulesNeverPopulateASource(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var cm syncJobConfigMap
			if err := yaml.Unmarshal(doc, &cm); err != nil || cm.Kind != "ConfigMap" {
				continue
			}
			if !strings.HasSuffix(cm.Metadata.Name, "-sync-job-config") {
				continue
			}

			seen++

			var cfg syncJobConfig
			require.NoErrorf(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg),
				"%s: %s's own config.yaml does not parse as YAML", g, cm.Metadata.Name)

			assert.Emptyf(t, cfg.Rules.Sources, "%s: %s's rules.sources is non-empty — the vendored "+
				"sync job would fetch at least one rule source over the network and apply it directly "+
				"to the cluster, invisible to this render, and kube-prometheus's own combined manifest "+
				"among them carries a second Watchdog", g, cm.Metadata.Name)
			assert.Emptyf(t, cfg.Rules.Groups, "%s: %s's rules.groups is non-empty — a per-group "+
				"override implies defaultRules is doing something, which it should not be with "+
				"defaultRules.enabled: false", g, cm.Metadata.Name)
		}
	}

	assert.Positive(t, seen, "no victoria-metrics-k8s-stack sync-job ConfigMap found in any "+
		"observability-stack golden; this check went blind rather than passing")
}
