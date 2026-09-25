package dashboardlint_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/dashboardlint"
)

// Every dashboard this repository ships must pass every rule — the same
// check `just dashboard-lint` runs in CI, kept here too so `go test
// ./...` alone catches a regression.
func TestShippedDashboardsPassEveryRule(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "charts", "observability-dashboards", "dashboards", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no shipped dashboards found; this test would pass vacuously")

	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			findings, err := dashboardlint.LintFile(f)
			require.NoError(t, err)
			assert.Emptyf(t, findings, "shipped dashboard failed its own lint: %v", findings)
		})
	}
}

// Each fixture under tests/dashboardlint/invalid isolates exactly one
// rule's failure mode; each must fail, and on the rule it names itself
// after.
func TestInvalidFixturesFailTheirNamedRule(t *testing.T) {
	cases := []struct {
		file string
		rule int
	}{
		{"rule1-literal-datasource.json", 1},
		{"rule2-no-cluster-variable.json", 2},
		{"rule3-namespace-scoped-no-namespace-variable.json", 3},
		{"rule4-title-missing-cluster.json", 4},
		{"rule5-environment-tier-selector.json", 5},
	}

	for _, c := range cases {
		c := c
		t.Run(c.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "tests", "dashboardlint", "invalid", c.file)
			findings, err := dashboardlint.LintFile(path)
			require.NoError(t, err)
			require.NotEmpty(t, findings, "fixture is meant to fail and did not")
			var gotRule bool
			for _, f := range findings {
				if f.Rule == c.rule {
					gotRule = true
				}
			}
			assert.Truef(t, gotRule, "fixture %s did not fail on rule %d, got: %v", c.file, c.rule, findings)
		})
	}
}

// Rule 6: a dashboard using Alertmanager's own `namespace` label, rather
// than this repository's stamped `k8s_namespace_name`, passes rules 2
// and 3 exactly the same way.
func TestNativeNamespaceLabelSpellingPasses(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "dashboardlint", "valid", "rule6-native-namespace-label.json")
	findings, err := dashboardlint.LintFile(path)
	require.NoError(t, err)
	assert.Empty(t, findings)
}
