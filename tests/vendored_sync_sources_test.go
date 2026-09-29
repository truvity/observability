// SQU-381's second defect: `victoria-metrics-k8s-stack`'s vendored sync
// job gates its `alertmanager.rules`/`vmalert.rules` VMRule sources, and
// the `alertmanager-overview`/`victoriametrics-vmalert`/`grafana-overview`
// dashboards, on `.Values.alertmanager.enabled`/`.vmalert.enabled`/
// `.grafana.enabled` — evaluated in the VENDORED subchart's OWN scope,
// which this chart hard-codes `false` UNCONDITIONALLY (it runs its own
// Alertmanager/vmalert/Grafana instead: values.yaml, right above
// `defaultRules`). Left at upstream's own default, those sources are
// gated on a flag that is never true in ANY install of this chart, so
// they vanish from the sync job's rendered ConfigMap with nothing saying
// so — found live, 2026-09-29, on an estate's cutover to this chart,
// which had to force all four back on by hand.
//
// values.yaml now overrides the four sources tied to `alertmanager.
// enabled`/`vmalert.enabled` to track THIS chart's own copies instead
// (MIRROR, enforced by `observability-stack.validate.vendoredSyncSources`
// — tests/invalid/observability-stack/vendored-sync-sources-*.yaml is
// the refusal side of that). This file is the render-time regression
// gate the fix itself needs: it fails if the rendered set of enabled
// rule/dashboard sources ever SHRINKS back to the broken state, on any
// golden whose own Alertmanager/vmalert/Grafana are on.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// syncJobFullConfig is a fuller decode of the same `data["config.yaml"]`
// string tests/watchdog_dedup_test.go reads — that file only needs
// Rules.Sources' length; this one needs each source's URL and resolved
// `enabled`, for both rules and dashboards.
type syncJobSource struct {
	URL     string `yaml:"url"`
	Enabled *bool  `yaml:"enabled"` // absent means "enabled" (vmks's sync-job/config.yaml only omits the key when true)
}

type syncJobFullConfig struct {
	Dashboards struct {
		Dashboards map[string]struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"dashboards"`
		Sources []syncJobSource `yaml:"sources"`
	} `yaml:"dashboards"`
	Rules struct {
		Sources []syncJobSource `yaml:"sources"`
	} `yaml:"rules"`
}

func (c syncJobFullConfig) ruleSourceEnabled(urlSuffix string) (bool, bool) {
	for _, s := range c.Rules.Sources {
		if strings.HasSuffix(s.URL, urlSuffix) {
			return s.Enabled == nil || *s.Enabled, true
		}
	}
	return false, false
}

func (c syncJobFullConfig) dashSourceEnabled(urlSuffix string) (bool, bool) {
	for _, s := range c.Dashboards.Sources {
		if strings.HasSuffix(s.URL, urlSuffix) {
			return s.Enabled == nil || *s.Enabled, true
		}
	}
	return false, false
}

func (c syncJobFullConfig) dashboardDisabled(name string) bool {
	d, ok := c.Dashboards.Dashboards[name]
	return ok && d.Enabled != nil && !*d.Enabled
}

// TestVendoredRuleAndDashboardSourcesTrackTheStacksOwnComponents is the
// SQU-381 regression gate: on every golden whose OWN VMAlertmanager/
// VMAlert(metrics)/Grafana Deployment is rendered — this chart's own
// objects, `templates/vmalertmanager.yaml`/`vmalert.yaml`/the `grafana`
// dependency, never the vendored copies, which are permanently off — the
// matching vendored sync-job source(s) must be enabled too. A future
// edit that reintroduces the untranslated upstream default (or any other
// regression that flips these back to the vendored, always-off flag)
// shrinks the enabled set on these exact goldens, and this test catches
// it the same render-time way the golden diff would, but for content the
// golden diff alone does not call out by name.
func TestVendoredRuleAndDashboardSourcesTrackTheStacksOwnComponents(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checkedRules, checkedDashboards int

	for _, g := range goldens {
		var (
			hasOwnAlertmanager   bool // templates/vmalertmanager.yaml's own VMAlertmanager
			hasOwnMetricsVMAlert bool // templates/vmalert.yaml's metrics VMAlert (not "-logs")
			hasGrafana           bool // the `grafana` dependency's own Deployment
			cfg                  syncJobFullConfig
			cfgFound             bool
		)

		for _, doc := range splitDocs(t, g) {
			var meta struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := yaml.Unmarshal(doc, &meta); err != nil {
				continue
			}
			switch meta.Kind {
			case "VMAlertmanager":
				hasOwnAlertmanager = true
			case "VMAlert":
				if !strings.HasSuffix(meta.Metadata.Name, "-logs") {
					hasOwnMetricsVMAlert = true
				}
			case "Deployment":
				if strings.Contains(meta.Metadata.Name, "grafana") {
					hasGrafana = true
				}
			case "ConfigMap":
				if strings.HasSuffix(meta.Metadata.Name, "-sync-job-config") {
					var cm struct {
						Data struct {
							ConfigYAML string `yaml:"config.yaml"`
						} `yaml:"data"`
					}
					require.NoError(t, yaml.Unmarshal(doc, &cm))
					require.NoErrorf(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg),
						"%s: sync-job-config's own config.yaml does not parse", g)
					cfgFound = true
				}
			}
		}

		if !cfgFound {
			continue
		}

		if hasOwnAlertmanager && len(cfg.Rules.Sources) > 0 {
			checkedRules++
			enabled, found := cfg.ruleSourceEnabled("alertmanager-prometheusRule.yaml")
			assert.Truef(t, found, "%s: this chart's own VMAlertmanager is rendered, but the vendored sync job's rule sources have no "+
				"alertmanager-prometheusRule.yaml entry at all", g)
			assert.Truef(t, enabled, "%s: this chart's own VMAlertmanager is rendered, but the vendored `alertmanager.rules` source is disabled — it is gated "+
				"on the VENDORED alertmanager.enabled (always false), not this chart's own", g)
		}
		if hasOwnMetricsVMAlert && len(cfg.Rules.Sources) > 0 {
			checkedRules++
			enabled, found := cfg.ruleSourceEnabled("alerts-vmalert.yml")
			assert.Truef(t, found, "%s: this chart's own metrics VMAlert is rendered, but the vendored sync job's rule sources have no alerts-vmalert.yml "+
				"entry at all", g)
			assert.Truef(t, enabled, "%s: this chart's own metrics VMAlert is rendered, but the vendored `vmalert.rules` source is disabled — it is gated on "+
				"the VENDORED vmalert.enabled (always false), not this chart's own", g)
		}

		if len(cfg.Dashboards.Sources) == 0 && len(cfg.Dashboards.Dashboards) == 0 {
			continue // defaultDashboards.enabled is false on this golden (the chart default) — nothing to check
		}

		if hasOwnMetricsVMAlert {
			checkedDashboards++
			enabled, found := cfg.dashSourceEnabled("dashboards/vmalert.json")
			assert.Truef(t, found, "%s: dashboard syncing is on and this chart's own metrics VMAlert is rendered, but the vendored sync job's dashboard "+
				"sources have no vmalert.json entry at all", g)
			assert.Truef(t, enabled, "%s: dashboard syncing is on and this chart's own metrics VMAlert is rendered, but the vendored `victoriametrics-vmalert` "+
				"dashboard's source is disabled", g)
			assert.Falsef(t, cfg.dashboardDisabled("victoriametrics-vmalert"), "%s: the victoriametrics-vmalert dashboard entry is explicitly disabled", g)
		}
		if hasOwnAlertmanager {
			checkedDashboards++
			assert.Falsef(t, cfg.dashboardDisabled("alertmanager-overview"), "%s: this chart's own VMAlertmanager is rendered and dashboard syncing is on, but "+
				"the alertmanager-overview dashboard is explicitly disabled — it is gated on the VENDORED alertmanager.enabled (always false), not this chart's own", g)
		}
		if hasGrafana {
			checkedDashboards++
			assert.Falsef(t, cfg.dashboardDisabled("grafana-overview"), "%s: Grafana is rendered and dashboard syncing is on, but the grafana-overview "+
				"dashboard is explicitly disabled — it is gated on the VENDORED grafana.enabled (always false), not this chart's own", g)
		}
	}

	assert.Positivef(t, checkedRules, "no golden with this chart's own Alertmanager/vmalert and the vendored rule sync on was checked; this test went "+
		"blind rather than passing")
	assert.Positivef(t, checkedDashboards, "no golden with dashboard syncing on was checked; this test went blind rather than passing — add/keep a "+
		"`defaultDashboards.enabled: true` case (tests/cases/observability-stack/everything)")
}
