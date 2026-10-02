// The kargo group's three rules: the exact MetricsQL each one renders,
// and the `for`/absence-window behaviour that text is supposed to
// produce, proved against a model of vmalert's own algorithm rather than
// by reading the expression and reasoning about it.
//
// Not a live vmalert, and deliberately not
// github.com/prometheus/prometheus's own `rules` package either: that
// package (or even just its `promql/promqltest` test helper) drags the
// FULL Prometheus SERVER dependency tree behind a single import —
// measured here, `go mod tidy` adds github.com/prometheus/alertmanager,
// gophercloud, scaleway, ovh, stackit, azidentity, the full
// aws-sdk-go-v2 client set and k8s.io/client-go, on top of the two lean
// subpackages (model/labels, model/relabel) this repository already uses
// in kubestatemetrics_relabel_test.go. None of that evaluates a
// MetricsQL string; it comes from `rules`' own service-discovery and
// notification plumbing, which this test never touches. So this file
// models the one piece of vmalert's behaviour these three rules actually
// depend on directly: `AlertingRule.Eval`'s pending/firing state machine
// (github.com/prometheus/prometheus/rules/alerting.go, and vmalert's own
// port of the identical, Prometheus-compatible `for:` contract) —
// pending from the first evaluation an expression matches, firing once
// the elapsed time since then reaches `for`, and cleared the instant the
// expression stops matching, whatever it does afterwards.
//
// The expr and for read off tests/golden/platform-alerts/kargo-defaults.yaml
// (tests/cases/platform-alerts/kargo-defaults' golden: the kargo group
// turned on with every other value left at its default) are asserted
// against the literal MetricsQL this group's design specifies below, so a
// template edit that changes either one fails here as well as in the
// golden diff — the boolean series each case drives through
// vmalertHoldWindow are hand-built from what that literal expression
// means, not copied from the template's own reasoning about itself.
package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// kargoRule reads one named rule's expr and for-window off the rendered
// platform-alerts.kargo group in
// tests/golden/platform-alerts/kargo-defaults.yaml.
func kargoRule(t *testing.T, alert string) (expr string, hold time.Duration) {
	t.Helper()

	for _, doc := range splitDocs(t, "golden/platform-alerts/kargo-defaults.yaml") {
		var rule vmRuleDoc
		if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, group := range rule.Spec.Groups {
			if group.Name != "platform-alerts.kargo" {
				continue
			}
			for _, r := range group.Rules {
				if r.Alert != alert {
					continue
				}
				d, err := time.ParseDuration(r.For)
				require.NoErrorf(t, err, "rule %s: `for: %s` is not a Go duration", alert, r.For)
				require.NotEmptyf(t, r.Expr, "rule %s: empty expr", alert)
				return r.Expr, d
			}
		}
	}

	t.Fatalf("no alert named %q in golden/platform-alerts/kargo-defaults.yaml's platform-alerts.kargo group", alert)
	return "", 0
}

// vmalertHoldWindow replays AlertingRule.Eval's algorithm
// (github.com/prometheus/prometheus/rules/alerting.go) for a single,
// unchanging label set, one evaluation per `interval` starting at t=0:
// pending from the first step `active` is true, firing once the elapsed
// time since then reaches `hold`, and cleared — the clock reset — the
// moment `active` reads false. vmalert's own evaluation loop
// (github.com/VictoriaMetrics/VictoriaMetrics/app/vmalert) follows the
// identical, Prometheus-compatible `for:` contract.
func vmalertHoldWindow(active []bool, hold, interval time.Duration) []bool {
	firing := make([]bool, len(active))
	var activeSince time.Time
	var pending bool

	for i, a := range active {
		if !a {
			pending = false
			continue
		}
		ts := time.Unix(0, 0).Add(time.Duration(i) * interval)
		if !pending {
			pending = true
			activeSince = ts
		}
		firing[i] = ts.Sub(activeSince) >= hold
	}
	return firing
}

// requireFiresAfter asserts firing[i] is false for every i < holdSteps
// and true from holdSteps to the end — the shape every "stuck past its
// hold" case below shares.
func requireFiresAfter(t *testing.T, firing []bool, holdSteps int) {
	t.Helper()
	for i, f := range firing {
		if i < holdSteps {
			assert.Falsef(t, f, "step %d (t=%s): fired before the hold elapsed", i, time.Duration(i)*time.Minute)
		} else {
			assert.Truef(t, f, "step %d (t=%s): did not fire once the hold had fully elapsed", i, time.Duration(i)*time.Minute)
		}
	}
}

// requireNeverFires asserts firing is false at every step.
func requireNeverFires(t *testing.T, firing []bool, why string) {
	t.Helper()
	for i, f := range firing {
		assert.Falsef(t, f, "step %d (t=%s): %s", i, time.Duration(i)*time.Minute, why)
	}
}

func allTrue(n int) []bool {
	a := make([]bool, n)
	for i := range a {
		a[i] = true
	}
	return a
}

// untilThen returns n booleans: true for the first `before` steps, false
// for the rest — "true, then it stopped at minute `before`".
func untilThen(n, before int) []bool {
	a := make([]bool, n)
	for i := range a {
		a[i] = i < before
	}
	return a
}

func and(a, b []bool) []bool {
	out := make([]bool, len(a))
	for i := range a {
		out[i] = a[i] && b[i]
	}
	return out
}

// TestKargoStagePromotionErroredFiresWhenStuck: a Stage whose Ready
// condition reads LastPromotionErrored for the whole default 15m window
// fires — never before it, always at or after it.
func TestKargoStagePromotionErroredFiresWhenStuck(t *testing.T) {
	expr, hold := kargoRule(t, "KargoStagePromotionErrored")
	assert.Equal(t, `kargo_stage_condition{type="Ready", reason="LastPromotionErrored"} == 0`, expr)
	require.Equal(t, 15*time.Minute, hold, "the documented default; kargo-defaults.yaml sets nothing away from it")

	requireFiresAfter(t, vmalertHoldWindow(allTrue(20), hold, time.Minute), 15)
}

// TestKargoStagePromotionErroredDoesNotFireOnRecovery: a Stage whose
// condition clears well inside the 15m window (recovers at minute 8, 7
// minutes short of the hold) never fires, at that window or any time
// after it.
func TestKargoStagePromotionErroredDoesNotFireOnRecovery(t *testing.T) {
	expr, hold := kargoRule(t, "KargoStagePromotionErrored")
	_ = expr

	requireNeverFires(t, vmalertHoldWindow(untilThen(20, 8), hold, time.Minute),
		"fired despite the Stage having recovered")
}

// TestKargoPromotionErroredJoinsOnStageRecovery: the Promotion-side rule
// reads `kargo_promotion_phase{phase="Errored"} == 1` ANDed on
// (namespace, stage) against the same Stage condition. A Promotion stuck
// in Errored while its Stage has already recovered (a later retry
// succeeded) must not fire — the join is what tells the two apart; a
// broken join (wrong `on` labels, or none at all) would fire on the
// stale Promotion regardless of the Stage.
func TestKargoPromotionErroredJoinsOnStageRecovery(t *testing.T) {
	expr, hold := kargoRule(t, "KargoPromotionErrored")
	assert.Equal(t,
		`kargo_promotion_phase{phase="Errored"} == 1 and on (k8s_cluster_name, namespace, stage) `+
			`kargo_stage_condition{type="Ready", reason="LastPromotionErrored"} == 0`,
		expr)
	require.Equal(t, 15*time.Minute, hold)

	promotionErrored := allTrue(20)       // the Promotion sits in Errored for the whole window
	stageStillErrored := untilThen(20, 8) // but its Stage recovers at minute 8
	joined := and(promotionErrored, stageStillErrored)

	requireNeverFires(t, vmalertHoldWindow(joined, hold, time.Minute),
		"fired on a stale Promotion whose Stage had already recovered")
}

// TestKargoPromotionErroredFiresWhenBothAgree: the corroborating case —
// the Promotion is Errored AND the Stage still reads the matching
// condition throughout, so the join has something to match for the
// entire window and the rule fires at the same 15m boundary as the
// Stage-only rule.
func TestKargoPromotionErroredFiresWhenBothAgree(t *testing.T) {
	expr, hold := kargoRule(t, "KargoPromotionErrored")
	_ = expr

	joined := and(allTrue(20), allTrue(20))

	requireFiresAfter(t, vmalertHoldWindow(joined, hold, time.Minute), 15)
}

// TestKargoStateMetricsAbsentFiresOnAbsence: neither series is exported
// at all — the KSM customResourceState config was never applied, say —
// for longer than the default 30m.
func TestKargoStateMetricsAbsentFiresOnAbsence(t *testing.T) {
	expr, hold := kargoRule(t, "KargoStateMetricsAbsent")
	assert.Equal(t, absentGuard(`kargo_stage_condition{type="Ready"}`, "")+` or `+absentGuard(`kargo_promotion_phase`, ""), expr)
	require.Equal(t, 30*time.Minute, hold, "the documented default")

	// "active" here means the deadman's own condition — both series
	// absent — holds, which `absent(...) or absent(...)` reports as true.
	requireFiresAfter(t, vmalertHoldWindow(allTrue(40), hold, time.Minute), 30)
}

// TestKargoStateMetricsAbsentDoesNotFireWhenPresent: with either series
// present throughout, `absent()` never has anything to report on that
// side and the deadman stays quiet — this rule does not care whether a
// promotion is stuck, only whether kube-state-metrics is still exporting
// anything to say so.
func TestKargoStateMetricsAbsentDoesNotFireWhenPresent(t *testing.T) {
	expr, hold := kargoRule(t, "KargoStateMetricsAbsent")
	_ = expr

	requireNeverFires(t, vmalertHoldWindow(make([]bool, 40), hold, time.Minute),
		"the deadman fired while a series was still being exported")
}
