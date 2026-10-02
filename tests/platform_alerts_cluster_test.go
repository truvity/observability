// Every join and every per-object aggregation in charts/platform-alerts
// must match on the cluster label.
//
// One metrics store can hold several clusters' series, told apart only by
// that label, and two clusters can share a namespace, a Job name or a PVC
// name. A join that leaves the label out fails the query outright with a
// duplicate-series error (vmalert health `err`, HTTP 422: the rule is
// dead and says nothing), or, for `and` / `unless` / `max by`, pairs and
// merges series across clusters without complaint. Where the label is
// absent on both sides it matches on empty, so a single-cluster store is
// unaffected. hack/platform-alerts-cluster-proof.sh evaluates all of this
// against a real VictoriaMetrics; this test is the fast tripwire that a
// rule added later cannot skip the label unnoticed.
package tests

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// clusterLabel is platform-alerts' `clusterLabel` default, the same name
// charts/observability-stack's `tenancy.clusterLabel` defaults to.
const clusterLabel = "k8s_cluster_name"

// matchLists finds every label list a rule expression uses to decide which
// series belong together: on(...), ignoring(...), by(...), without(...).
var matchLists = regexp.MustCompile(`\b(on|ignoring|by|without)\s*\(([^)]*)\)`)

// platformAlertRules returns every alerting rule of every group of a
// rendered platform-alerts golden, keyed "group / alert".
func platformAlertRules(t *testing.T, golden string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, group := range rule.Spec.Groups {
			for _, r := range group.Rules {
				require.NotEmptyf(t, r.Expr, "%s / %s: empty expr", group.Name, r.Alert)
				out[group.Name+" / "+r.Alert] = r.Expr
			}
		}
	}
	require.NotEmpty(t, out, "%s renders no rules", golden)
	return out
}

// exempt names the rules whose expression has no join or per-object
// aggregation to match on, or whose aggregation is deliberately over every
// series: each entry says why. A rule not listed here has every match list
// checked, so a new rule that adds `on (` without the label fails.
var exempt = map[string]string{
	// Sums one store's own rows counter across that store's processes; a
	// per-cluster split would also break the `or vector(0)` deadman.
	"platform-alerts.write-path / WritePathDead": "sums one store's own counter",
	// Absent guards (every `*Absent` alert): per cluster by construction,
	// each clause groups by the cluster label, and the whole-store fallback
	// is `absent(...) unless on() group(...)`, an `on()` that is empty on
	// purpose: "no cluster has the series" is the one question that must
	// not be asked per cluster. hack/platform-alerts-absent-proof.sh
	// evaluates them against a real VictoriaMetrics.
	"platform-alerts.kargo / KargoStateMetricsAbsent": "absent guard: per-cluster groups plus a deliberate empty on()",
	// Two metrics of the same store, matched one-to-one on every label,
	// cluster included.
	"platform-alerts.store-limits / StoreApproachingReadOnly": "one-to-one on all labels",
}

func TestEveryPlatformAlertJoinIncludesTheClusterLabel(t *testing.T) {
	goldens := []string{
		"golden/platform-alerts/minimal.yaml",
		"golden/platform-alerts/everything.yaml",
		"golden/platform-alerts/kargo-defaults.yaml",
		"golden/platform-alerts/suspended-not-ignored.yaml",
	}

	seen := 0
	for _, golden := range goldens {
		for name, expr := range platformAlertRules(t, golden) {
			if _, ok := exempt[name]; ok {
				continue
			}
			for _, m := range matchLists.FindAllStringSubmatch(expr, -1) {
				seen++
				assert.Containsf(t, strings.Split(strings.ReplaceAll(m[2], " ", ""), ","), clusterLabel,
					"%s (%s): `%s (%s)` does not match on %s — on a store holding two clusters that share a name "+
						"this fails with a duplicate-series error or pairs series across clusters",
					name, golden, m[1], strings.TrimSpace(m[2]), clusterLabel)
			}
		}
	}
	// Guards the guard: the regexp must actually be finding joins.
	assert.Greater(t, seen, 10, "found suspiciously few on()/by() lists; is the match regexp still right?")
}

// TestEveryPlatformAlertMatchListIsAccountedFor fails when a rule with no
// on()/by() list at all is not in `exempt` — a new rule must either carry
// the label or say, in `exempt`, why it need not.
func TestEveryPlatformAlertMatchListIsAccountedFor(t *testing.T) {
	for name, expr := range platformAlertRules(t, "golden/platform-alerts/everything.yaml") {
		if _, ok := exempt[name]; ok {
			continue
		}
		hasList := matchLists.MatchString(expr)
		// Plain selector comparisons (KargoStagePromotionErrored, the
		// absent() deadman, VolumeSmallerThanClaimed's operands) hold no
		// list of their own; they are accepted only when they name no
		// second metric to pair with.
		if !hasList {
			assert.NotRegexpf(t, `\b(and|or|unless)\b|\s[*/]\s`, expr,
				"%s pairs two metrics with no on()/by() list, so the cluster label is not part of the match; add one or list the rule in `exempt`", name)
		}
	}
}

// TestPlatformAlertsClusterLabelOptOutIsTheOldRule: `clusterLabel: ""`
// renders no trace of the label in any expression, the expressions 0.11.2
// shipped.
func TestPlatformAlertsClusterLabelOptOutIsTheOldRule(t *testing.T) {
	for name, expr := range platformAlertRules(t, "golden/platform-alerts/cluster-label-off.yaml") {
		assert.NotContainsf(t, expr, clusterLabel, name)
	}
}
