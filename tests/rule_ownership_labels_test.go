package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvity/observability/pkg/contracts"
	"github.com/truvity/observability/pkg/rulecheck"
)

// Every VMRule this repository's own charts render carries the ownership
// labels (restructure step 2): the evaluator names the vmalert, rule-type is
// alert or recording, and a LogsQL rule carries both spellings of "logs"
// while the old one is accepted (docs/contracts.md, "Rule ownership labels").
func TestOwnChartsStampOwnershipLabels(t *testing.T) {
	var files []string

	for _, pat := range []string{
		"golden/platform-alerts/*.yaml", "golden/observability-rum/*.yaml",
		"golden/observability-projects/*.yaml", "golden/alert-ingress/*.yaml",
	} {
		m, err := filepath.Glob(pat)
		require.NoError(t, err)

		files = append(files, m...)
	}

	// The stack's own VMRules; the vendored ones carry no label by design.
	m, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	files = append(files, m...)
	require.NotEmpty(t, files)

	var seen int

	for _, f := range files {
		srcs, err := rulecheck.Load("", f)
		require.NoError(t, err)

		rules, err := rulecheck.Rules(srcs...)
		require.NoError(t, err)

		stack := filepath.Base(filepath.Dir(f)) == "observability-stack"
		perRes := map[string]rulecheck.Rule{}

		for _, r := range rules {
			perRes[r.Resource] = r
		}

		for res, r := range perRes {
			if stack && !isOwnStackRule(res) {
				continue
			}

			seen++

			ev := r.Labels[contracts.EvaluatorLabel]
			assert.NotEmptyf(t, ev, "%s: VMRule %s has no %s", f, res, contracts.EvaluatorLabel)
			assert.Containsf(t, []string{contracts.RuleTypeAlert, contracts.RuleTypeRecording}, r.Labels[contracts.RuleTypeLabel],
				"%s: VMRule %s has no valid %s", f, res, contracts.RuleTypeLabel)

			if r.LogsQL() {
				assert.Equalf(t, contracts.EvaluatorLogs, ev, "%s: LogsQL VMRule %s", f, res)
				assert.Equalf(t, "vlogs", r.Labels["observability.rule-type"],
					"%s: LogsQL VMRule %s lost the old marker while it is still accepted", f, res)
			}
		}
	}

	assert.Positive(t, seen)
}

func isOwnStackRule(name string) bool {
	for _, suffix := range []string{"-selfalerts", "-store-alerts", "-watchdog"} {
		if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
			return true
		}
	}

	return false
}
