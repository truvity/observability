package rulecheck_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/rulecheck"
)

const stackCharts = "../../charts/observability-stack/charts"

const fixture = `
apiVersion: operator.victoriametrics.com/v1beta1
kind: VMRule
metadata: {name: fixture}
spec:
  groups:
    - name: logs
      type: vlogs
      rules:
        - alert: Bad
          expr: '_msg:"x" | stats count() as failures | filter failures:==0'
        - alert: Good
          expr: '_msg:"x" | stats count() as failures | filter failures:=0'
    - name: metrics
      rules:
        - alert: Bad
          expr: 'sum(rate(up[5m]'
        - alert: Good
          expr: 'sum(rate(up[5m])) > 0'
    - name: metrics-explicit
      type: prometheus
      rules:
        - record: r
          expr: 'up == 0'
---
apiVersion: v1
kind: ConfigMap
metadata: {name: not-a-rule}
`

func TestRulesReadsTypeAndExpr(t *testing.T) {
	rules, err := rulecheck.Rules(rulecheck.Source{Name: "fixture.yaml", YAML: []byte(fixture)})
	require.NoError(t, err)
	require.Len(t, rules, 5)

	logs := 0

	for _, r := range rules {
		assert.Equal(t, "fixture.yaml", r.Source)
		assert.Equal(t, "fixture", r.Resource)
		assert.NotEmpty(t, r.Name)
		assert.NotEmpty(t, r.Expr)

		if r.LogsQL() {
			logs++
			assert.Equal(t, "logs", r.Group)
		}
	}

	assert.Equal(t, 2, logs)
}

func TestRulesRefusesAnUnknownType(t *testing.T) {
	_, err := rulecheck.Rules(rulecheck.Source{Name: "x.yaml", YAML: []byte(`
kind: VMRule
metadata: {name: r}
spec:
  groups:
    - name: g
      type: graphite
      rules:
        - alert: A
          expr: up
`)})
	require.ErrorContains(t, err, `type "graphite"`)
}

func TestCheckWithNothingToCheckIsAnError(t *testing.T) {
	_, err := rulecheck.Check(t.Context(), nil, rulecheck.Options{})
	require.ErrorIs(t, err, rulecheck.ErrNoRules)
}

func TestLoadWalksDirectoriesInOrder(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("a: 1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "a.yml"), []byte("a: 1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("x"), 0o600))

	got, err := rulecheck.Load(dir, dir)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "b.yaml", got[0].Name)
	assert.Equal(t, filepath.Join("sub", "a.yml"), got[1].Name)
}

// The parsers follow the chart: their tags are the appVersion of the
// vendored dependency archives, read here, not written down.
func TestChartVersionsReadTheVendoredStack(t *testing.T) {
	v, err := rulecheck.ChartVersions(stackCharts)
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^v\d+\.\d+\.\d+$`), v.Metrics)
	assert.Regexp(t, regexp.MustCompile(`^v\d+\.\d+\.\d+$`), v.Logs)

	// The chart's own pinned image tag for the metrics side is the same
	// release: the archive and the values must not part.
	values, err := os.ReadFile("../../charts/observability-stack/values.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(values), "tag: "+v.Metrics)
}

func TestChartVersionsNeedsTheArchives(t *testing.T) {
	_, err := rulecheck.ChartVersions(t.TempDir())
	require.Error(t, err)
}

func TestRenderedVersions(t *testing.T) {
	v, err := rulecheck.RenderedVersions([]byte(`
kind: VMSingle
spec:
  image: {tag: v1.2.3}
---
image: "docker.io/victoriametrics/victoria-logs:v4.5.6"
`))
	require.NoError(t, err)
	assert.Equal(t, rulecheck.Versions{Metrics: "v1.2.3", Logs: "v4.5.6"}, v)

	_, err = rulecheck.RenderedVersions([]byte("kind: VMSingle\n"))
	require.Error(t, err)
}

// TestParsersRejectBadExpressions proves the gate has teeth: the parsers
// are real (they refuse a `:==0` LogsQL filter and a broken MetricsQL
// expression, and accept their corrected forms), and Check reports what it
// refused with the rule's coordinates and the parser's own words.
func TestParsersRejectBadExpressions(t *testing.T) {
	v, err := rulecheck.ChartVersions(stackCharts)
	require.NoError(t, err)

	rules, err := rulecheck.Rules(rulecheck.Source{Name: "fixture.yaml", YAML: []byte(fixture)})
	require.NoError(t, err)

	findings, err := rulecheck.Check(t.Context(), rules, rulecheck.Options{
		VMVersion: v.Metrics, VLVersion: v.Logs, BinDir: os.Getenv("RULECHECK_BIN_DIR"), CacheDir: os.Getenv("RULECHECK_CACHE_DIR"),
	})
	if errors.Is(err, rulecheck.ErrUnsupportedOS) {
		t.Skip(err)
	}

	require.NoError(t, err)
	require.Len(t, findings, 2, "exactly the two Bad rules must be refused: %v", findings)

	byGroup := map[string]rulecheck.Finding{}
	for _, f := range findings {
		assert.Equal(t, "Bad", f.Name)
		assert.NotEmpty(t, f.Err)
		byGroup[f.Group] = f
	}

	logs, metrics := byGroup["logs"], byGroup["metrics"]
	assert.True(t, logs.LogsQL())
	assert.Equal(t, "victoria-logs", logs.Parser)
	assert.Equal(t, v.Logs, logs.Version)
	assert.Contains(t, logs.String(),
		"invalid expression\n  fixture.yaml: VMRule \"fixture\" group \"logs\" rule \"Bad\" (LogsQL)\n  parser (victoria-logs "+v.Logs+"): HTTP 4")

	assert.False(t, metrics.LogsQL())
	assert.Equal(t, "victoria-metrics", metrics.Parser)
	assert.Equal(t, v.Metrics, metrics.Version)
	assert.Contains(t, metrics.String(), "expr: sum(rate(up[5m]")
}
