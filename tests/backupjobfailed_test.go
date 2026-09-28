// BackupJobFailed's expression is a JOIN — kube_job_created onto
// kube_job_owner onto kube_job_status_failed, keeping only the newest Job
// per (namespace, CronJob) — and this repository's own hold-window model
// (vmalertHoldWindow in tests/kargo_alerts_test.go) cannot evaluate a
// join: it drives a single boolean series through vmalert's pending/firing
// state machine, not a real MetricsQL engine matching label sets across
// several metrics. So the real proof that the join does what
// docs/safety.md claims lives in hack/platform-alerts-newest-job-proof.sh,
// against a real victoria-metrics binary. What belongs here, in a fast Go
// test with no Docker, is the one thing a hand-reasoned review of the
// template cannot catch on its own: a later edit that changes the
// rendered expression string without anyone noticing, because nothing
// else in `just check` reads it character for character. The golden diff
// already catches this too — this is a second, more legible tripwire
// pointed at the exact string, with the reasoning for each clause written
// next to the assertion.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// backupsRule reads one named rule's expr and for-window off the rendered
// platform-alerts.backups group in the given golden.
func backupsRule(t *testing.T, golden, alert string) (expr string, hold string) {
	t.Helper()

	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, group := range rule.Spec.Groups {
			if group.Name != "platform-alerts.backups" {
				continue
			}
			for _, r := range group.Rules {
				if r.Alert != alert {
					continue
				}
				require.NotEmptyf(t, r.Expr, "rule %s: empty expr", alert)
				return r.Expr, r.For
			}
		}
	}

	t.Fatalf("no alert named %q in %s's platform-alerts.backups group", alert, golden)
	return "", ""
}

// TestBackupJobFailedExprIsTheNewestJobJoin pins the exact MetricsQL this
// repository ships, so a template edit that changes any clause of the
// join — the metric it keys "newest" on, the owner_kind scope, which
// labels the two `on (...)` steps match — fails here as well as in the
// golden diff. hack/platform-alerts-newest-job-proof.sh is what proves
// this string actually behaves as claimed against a real engine; this
// test only proves it did not silently change.
func TestBackupJobFailedExprIsTheNewestJobJoin(t *testing.T) {
	expr, hold := backupsRule(t, "golden/platform-alerts/minimal.yaml", "BackupJobFailed")

	// A YAML folded scalar (`expr: >-`) only folds a newline between two
	// lines at the block's OWN indentation; a line indented further than
	// its neighbours keeps a literal break, and yaml.v3 emits an extra
	// blank line at the transition onto one (already true of this
	// group's own CronJobNotSucceeding, unchanged by this fix — not
	// something introduced here). This is the exact string vmalert reads
	// off the golden, not a hand-folded approximation of it.
	assert.Equal(t, "WITH (\n\n  cronjob_jobs = (\n    kube_job_created{namespace=~\".*\"}\n"+
		"    * on (namespace, job_name) group_left(owner_name)\n"+
		"    kube_job_owner{namespace=~\".*\", owner_kind=\"CronJob\"}\n  ),\n"+
		"  newest_per_cronjob = max by (namespace, owner_name) (cronjob_jobs)\n"+
		") max by (namespace, job_name) (\n\n  kube_job_status_failed{namespace=~\".*\"}\n"+
		"  and on (namespace, job_name) (\n"+
		"    cronjob_jobs == on (namespace, owner_name) group_left() newest_per_cronjob\n  )\n) > 0",
		expr)

	// Unchanged by this fix, and deliberately so: the rule already fires
	// on the first evaluation that sees the newest Job failed — see
	// docs/safety.md, "A failed backup Job that never clears" — a `for`
	// would only delay noticing a real failure, and nothing about joining
	// on the newest Job changes that.
	assert.Empty(t, hold, "BackupJobFailed carries no `for`; this fix does not add one")
}

// TestBackupJobFailedNamespaceSelectorIsThreaded proves namespaceSelector
// still reaches every clause of the join, not only the outermost one — a
// join that forgets the selector on one leg would silently widen the rule
// to every namespace in the cluster for that leg alone.
func TestBackupJobFailedNamespaceSelectorIsThreaded(t *testing.T) {
	expr, _ := backupsRule(t, "golden/platform-alerts/everything.yaml", "BackupJobFailed")

	for _, metric := range []string{"kube_job_created", "kube_job_owner", "kube_job_status_failed"} {
		assert.Containsf(t, expr, metric+`{namespace=~"example-.*"`, "%s: expr does not scope %s by namespaceSelector", expr, metric)
	}
}
