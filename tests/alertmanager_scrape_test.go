// The Slack delivery alert needs a series to evaluate.
//
// `SlackNotificationsFailing` watches a counter only Alertmanager's own
// /metrics carries. With no scrape object the series never lands and the
// rule can never fire: a rule that looks wired up and reaches nobody.
// This holds three things together against every golden:
//
//   - wherever the rule renders, a ServiceMonitor for the Alertmanager
//     renders too, selecting the labels the operator puts on that
//     Alertmanager's Service;
//   - the operator's own VMServiceScrape for the VMAlertmanager is off
//     (docs/safety.md: scrape objects are always the Prometheus Operator
//     kinds);
//   - no NetworkPolicy selects the Alertmanager pods, so the agent is not
//     refused on the metrics port.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackFailureAlertHasAScrapeForItsMetric(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var withAlert int

	for _, g := range goldens {
		docs := renderedDocs(t, g)

		var am, sm map[string]any
		var rule bool
		for _, d := range docs {
			switch d["kind"] {
			case "VMAlertmanager":
				am = d
			case "ServiceMonitor":
				if dig(d, "spec", "selector", "matchLabels", "app.kubernetes.io/name") == "vmalertmanager" {
					sm = d
				}
			case "VMRule":
				groups, _ := dig(d, "spec", "groups").([]any)
				for _, grp := range groups {
					rules, _ := dig(grp, "rules").([]any)
					for _, r := range rules {
						if dig(r, "alert") == "SlackNotificationsFailing" {
							rule = true
						}
					}
				}
			}
		}

		if am != nil {
			assert.Equalf(t, true, dig(am, "spec", "disableSelfServiceScrape"),
				"%s: the operator's own VMServiceScrape for the VMAlertmanager is not disabled", g)
			assert.NotNilf(t, sm, "%s: an Alertmanager is rendered but nothing scrapes it", g)

			// No policy may select the Alertmanager pods.
			for _, d := range docs {
				if d["kind"] != "NetworkPolicy" {
					continue
				}
				assert.NotEqualf(t, "vmalertmanager",
					dig(d, "spec", "podSelector", "matchLabels", "app.kubernetes.io/name"),
					"%s: NetworkPolicy %v selects the Alertmanager pods and must admit the metrics agent on 9093", g, dig(d, "metadata", "name"))
			}
		}

		if !rule {
			continue
		}
		withAlert++

		require.NotNilf(t, sm, "%s: SlackNotificationsFailing renders but no ServiceMonitor scrapes Alertmanager — the rule could never fire", g)
		assert.Equalf(t, "vmalertmanager", dig(sm, "spec", "selector", "matchLabels", "app.kubernetes.io/name"), g)
		assert.NotEmptyf(t, dig(sm, "spec", "selector", "matchLabels", "app.kubernetes.io/instance"),
			"%s: the selector is not pinned to this release's Alertmanager", g)
		endpoints, _ := dig(sm, "spec", "endpoints").([]any)
		require.Lenf(t, endpoints, 1, "%s: expected one endpoint", g)
		assert.Equalf(t, "/metrics", dig(endpoints[0], "path"), g)
	}

	require.Positive(t, withAlert, "no golden renders SlackNotificationsFailing; this test would pass vacuously")
}
