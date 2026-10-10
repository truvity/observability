package rulecheck_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/rulecheck"
)

// vmrule renders one VMRule with one alert per (name, expr) pair, in one
// group. A rule that names the cluster label anywhere makes the whole
// VMRule cluster-aware, so every fixture carries a "Marker" alert that does.
func vmrule(group string, alerts ...string) rulecheck.Source {
	var b strings.Builder

	fmt.Fprintf(&b, "kind: VMRule\nmetadata: {name: fx}\nspec:\n  groups:\n    - name: %s\n      rules:\n", group)
	b.WriteString("        - alert: Marker\n          expr: 'sum by (k8s_cluster_name) (up)'\n")

	for i := 0; i+1 < len(alerts); i += 2 {
		fmt.Fprintf(&b, "        - alert: %s\n          expr: %q\n", alerts[i], alerts[i+1])
	}

	return rulecheck.Source{Name: "fx.yaml", YAML: []byte(b.String())}
}

func lint(t *testing.T, o rulecheck.LintOptions, group string, alerts ...string) []rulecheck.Violation {
	t.Helper()

	rules, err := rulecheck.Rules(vmrule(group, alerts...))
	require.NoError(t, err)

	vs, err := rulecheck.Lint(rules, o)
	require.NoError(t, err)

	return vs
}

func checks(vs []rulecheck.Violation) []string {
	var out []string
	for i := range vs {
		out = append(out, vs[i].Check+":"+vs[i].Name)
	}

	return out
}

// v0.45.1: self-alerts aggregated with `by (reason)` or a bare sum() and
// arrived with no cluster label.
func TestClusterLabelAggregations(t *testing.T) {
	vs := lint(t, rulecheck.LintOptions{}, "g",
		"BareSum", `sum(rate(x_total[5m])) > 0`,
		"ByWithout", `sum by (reason) (rate(x_total[5m])) > 0`,
		"WithoutDrops", `sum without (k8s_cluster_name) (rate(x_total[5m])) > 0`,
		"ByKeeps", `sum by (reason, k8s_cluster_name) (rate(x_total[5m])) > 0`,
		"WithoutKeeps", `sum without (pod, instance) (rate(x_total[5m])) > 0`,
		"PostfixBy", `max(x) by (k8s_cluster_name, job) > 0`,
		"OnNoCluster", `a and on (namespace) b`,
		"OnCluster", `a and on (k8s_cluster_name, namespace) b`,
		"GlobalByDesign", `a unless on() group(max_over_time(b[1d]))`,
		"UnderScalar", `scalar(sum(x)) > 1`,
		"TopkKeepsLabels", `topk(3, x)`,
	)

	assert.ElementsMatch(t, []string{
		"cluster-label:BareSum", "cluster-label:ByWithout", "cluster-label:WithoutDrops", "cluster-label:OnNoCluster",
	}, checks(vs))
}

func TestClusterChecksNeedAClusterAwareVMRule(t *testing.T) {
	// A single-cluster render (the label switched off) names it nowhere: no
	// cluster check applies. Built by hand: vmrule() adds the marker.
	rules, err := rulecheck.Rules(rulecheck.Source{Name: "x.yaml", YAML: []byte(`
kind: VMRule
metadata: {name: single}
spec:
  groups:
    - name: g
      rules:
        - alert: A
          expr: 'sum(rate(x[5m])) > 0'
        - alert: B
          expr: 'absent(up)'
`)})
	require.NoError(t, err)

	vs, err := rulecheck.Lint(rules, rulecheck.LintOptions{})
	require.NoError(t, err)
	assert.Empty(t, vs)

	// Another label name is honoured.
	vs, err = rulecheck.Lint(rules, rulecheck.LintOptions{ClusterLabel: "sum"})
	require.NoError(t, err)
	assert.NotEmpty(t, vs, "an expression naming the configured label makes the VMRule cluster-aware")
}

const (
	lookbackSide = `group by (k8s_cluster_name) (max_over_time(up{job="x", k8s_cluster_name!=""}[1d]))`
	currentSide  = `group by (k8s_cluster_name) (up{job="x", k8s_cluster_name!=""})`
	lookbackBare = `group by (k8s_cluster_name) (max_over_time(up{job="x"}[1d]))`
	currentBare  = `group by (k8s_cluster_name) (up{job="x"})`
	wholeStore   = `(absent(up{job="x"}) unless on() group(max_over_time(up{job="x"}[1d])))`

	guarded = `(` + lookbackSide + ` unless ` + currentSide + `) or ` + wholeStore
)

// v0.44.1: the per-cluster absent guard. A bare absent() is false while any
// cluster still exports the series.
func TestAbsentNeedsThePerClusterGuard(t *testing.T) {
	vs := lint(t, rulecheck.LintOptions{}, "g",
		"Bare", `absent(up{job="x"})`,
		"BareWithComparison", `absent(up{job="x"} == 1)`,
		"WholeStoreOnly", `absent(up{job="x"}) unless on() group(max_over_time(up{job="x"}[1d]))`,
		"Guarded", guarded,
		"GuardNoLabelFilter", `(`+lookbackBare+` unless `+currentBare+`) or `+wholeStore,
		"GuardOneSideLax", `(`+lookbackSide+` unless `+currentBare+`) or `+wholeStore,
		"GuardedLabelled", `label_replace(`+guarded+`, "source", "x", "", "")`,
	)

	assert.ElementsMatch(t, []string{
		"absent-guard:Bare", "absent-guard:BareWithComparison", "absent-guard:WholeStoreOnly",
		"absent-guard:GuardNoLabelFilter", "absent-guard:GuardOneSideLax",
	}, checks(vs))
}

// v0.43.3: a heartbeat is delivered to ONE replica behind the Service.
func TestHeartbeatMustNotBeEvaluatedPerPod(t *testing.T) {
	vs := lint(t, rulecheck.LintOptions{}, "g",
		"HeartbeatMissing", `absent_over_time(m{mapping="heartbeat"}[10m]) or increase(m{mapping="heartbeat"}[10m]) == 0`,
		"HeartbeatFixed", `absent_over_time(m{mapping="heartbeat"}[10m]) or sum without (pod, instance) (increase(m{mapping="heartbeat"}[10m])) == 0`,
		"HeartbeatByCluster", `sum by (k8s_cluster_name) (increase(m{mapping="heartbeat"}[10m])) == 0`,
		"HeartbeatByPod", `sum by (k8s_cluster_name, pod) (increase(m{mapping="heartbeat"}[10m])) == 0`,
		"HeartbeatDropsOnlyPod", `sum without (pod) (increase(m{mapping="heartbeat"}[10m])) == 0`,
		"DeadmanPerPod", `rate(m[5m]) == 0`,
		"DeadmanSummed", `sum without (pod, instance) (rate(m[5m])) == 0`,
		"UnrelatedPerPod", `max by (k8s_cluster_name, pod) (m) < 1`,
	)

	assert.ElementsMatch(t, []string{
		"pod-heartbeat:HeartbeatMissing", "pod-heartbeat:HeartbeatByPod", "pod-heartbeat:HeartbeatDropsOnlyPod",
		"pod-heartbeat:DeadmanPerPod",
	}, checks(vs))
}

func TestAbsentAlertMustNotBeKeyedOnPod(t *testing.T) {
	vs := lint(t, rulecheck.LintOptions{}, "g",
		"ThingAbsent", `max by (k8s_cluster_name, pod) (up) == 0`,
		"OtherAbsent", `max by (k8s_cluster_name) (up) == 0`,
	)

	assert.Equal(t, []string{"pod-heartbeat:ThingAbsent"}, checks(vs))
}

func selfAlerts(sourceAbsent string) []string {
	a := []string{
		"StoreDropping", `sum without (pod, instance) (rate(store_dropped_total[5m])) > 0`,
		"WriterBuffer", `deriv({__name__=~"writer_buf_a|writer_buf_b"}[15m]) > 0`,
		"Snapshot", `time() - kube_cronjob_status_last_successful_time > 100`,
	}
	if sourceAbsent != "" {
		a = append(a, "SelfAlertSourceAbsent", sourceAbsent)
	}

	return a
}

func srcAbsent(names ...string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`label_replace((group by (k8s_cluster_name) (max_over_time(%[1]s{k8s_cluster_name!=""}[1d]))`+
			` unless group by (k8s_cluster_name) (%[1]s{k8s_cluster_name!=""}))`+
			` or (absent(%[1]s) unless on() group(max_over_time(%[1]s[1d]))), "source", "%[1]s", "", "")`, n))
	}

	return strings.Join(parts, " or ")
}

// v0.46.0: every self-alert source needs sourceAbsent coverage.
func TestEverySelfAlertSourceIsWatched(t *testing.T) {
	// Complete (a regexp name counts as each alternative; kube_* is another
	// exporter's and is left to platform-alerts).
	assert.Empty(t, lint(t, rulecheck.LintOptions{RequireSourceAbsent: true}, "stack.selfalerts",
		selfAlerts(srcAbsent("store_dropped_total", "writer_buf_a", "writer_buf_b"))...))

	// A source the rule leaves out is a violation whether or not the rule
	// is required.
	for _, require := range []bool{false, true} {
		vs := lint(t, rulecheck.LintOptions{RequireSourceAbsent: require}, "stack.selfalerts",
			selfAlerts(srcAbsent("store_dropped_total", "writer_buf_a"))...)
		require2 := assert.Len(t, vs, 1)
		if require2 {
			assert.Equal(t, "source-absent", vs[0].Check)
			assert.Contains(t, vs[0].Msg, "writer_buf_b")
		}
	}

	// No SelfAlertSourceAbsent at all: opt-in in the chart, so only a
	// violation when required.
	assert.Empty(t, lint(t, rulecheck.LintOptions{}, "stack.selfalerts", selfAlerts("")...))

	vs := lint(t, rulecheck.LintOptions{RequireSourceAbsent: true}, "stack.selfalerts", selfAlerts("")...)
	assert.Len(t, vs, 3, "store_dropped_total, writer_buf_a, writer_buf_b")

	// Only the self-alert group is held to it.
	assert.Empty(t, lint(t, rulecheck.LintOptions{RequireSourceAbsent: true}, "platform", selfAlerts("")...))
}

func TestAllowlistIsNarrow(t *testing.T) {
	alerts := []string{"Thing", `sum(x) > 1`}
	assert.Len(t, lint(t, rulecheck.LintOptions{Allow: []rulecheck.Allow{}}, "g", alerts...), 1)
	assert.Empty(t, lint(t, rulecheck.LintOptions{Allow: []rulecheck.Allow{{Check: "cluster-label", Alert: "Thing", Reason: "test"}}}, "g", alerts...))
	assert.Len(t, lint(t, rulecheck.LintOptions{Allow: []rulecheck.Allow{{Check: "cluster-label", Alert: "Other", Reason: "test"}}}, "g", alerts...), 1)
	assert.Len(t, lint(t, rulecheck.LintOptions{Allow: []rulecheck.Allow{{Check: "absent-guard", Alert: "Thing", Reason: "test"}}}, "g", alerts...), 1)
}

// Every rule the charts ship, rendered, passes the semantic checks: the
// goldens are what `helm template` renders (`just test` proves it), so this
// is the lint on every rendered chart.
func TestRenderedGoldensPassTheSemanticLint(t *testing.T) {
	sources, err := rulecheck.Load("", "../../tests/golden")
	require.NoError(t, err)

	rules, err := rulecheck.Rules(sources...)
	require.NoError(t, err)
	require.NotEmpty(t, rules)

	vs, err := rulecheck.Lint(rules, rulecheck.LintOptions{})
	require.NoError(t, err)

	for i := range vs {
		t.Error(vs[i].String())
	}
}

func TestCoverageNamesDisabledGroupsAndRules(t *testing.T) {
	mk := func(rs ...string) []rulecheck.Rule {
		var out []rulecheck.Rule
		for _, r := range rs {
			g, n, _ := strings.Cut(r, "/")
			out = append(out, rulecheck.Rule{Source: "s", Resource: "r", Group: g, Name: n, Expr: "up"})
		}

		return out
	}

	full := mk("a/One", "a/Two", "b/Three")
	rep := rulecheck.Coverage([]rulecheck.ClusterInput{
		{Name: "dev", Rules: mk("a/One", "b/Three")},
		{Name: "prod", Rules: mk("a/One", "a/Two")},
	}, full)

	assert.Equal(t, 2, rep.CatalogGroups)
	assert.Equal(t, 3, rep.CatalogRules)
	require.Len(t, rep.Clusters, 2)

	dev, prod := rep.Clusters[0], rep.Clusters[1]
	assert.Equal(t, "dev", dev.Cluster)
	assert.Equal(t, []string{"a/Two"}, dev.DisabledRules)
	assert.Empty(t, dev.DisabledGroups)
	assert.Equal(t, []string{"b"}, prod.DisabledGroups)
	assert.Empty(t, prod.DisabledRules)

	var md, js bytes.Buffer
	require.NoError(t, rep.WriteMarkdown(&md))
	require.NoError(t, rep.WriteJSON(&js))
	assert.Contains(t, md.String(), "a/Two")
	assert.Contains(t, js.String(), `"disabledGroups"`)
}

func labelled(labels string, groupType string) rulecheck.Source {
	return rulecheck.Source{Name: "lb.yaml", YAML: []byte(`kind: VMRule
metadata:
  name: lb
  labels: ` + labels + `
spec:
  groups:
    - name: g
      type: ` + groupType + `
      rules:
        - alert: A
          expr: 'up == 0'
        - alert: B
          expr: 'up == 0'
`)}
}

func lintLabels(t *testing.T, o rulecheck.LintOptions, s rulecheck.Source) []rulecheck.Violation {
	t.Helper()

	rules, err := rulecheck.Rules(s)
	require.NoError(t, err)

	vs, err := rulecheck.Lint(rules, o)
	require.NoError(t, err)

	var out []rulecheck.Violation

	for i := range vs {
		if vs[i].Check == rulecheck.CheckEvaluatorLabel || vs[i].Check == rulecheck.CheckRuleTypeLabel {
			out = append(out, vs[i])
		}
	}

	return out
}

// An unlabelled VMRule is fine until a consumer asks for the evaluator, and
// then it is one finding per VMRule, not per rule.
func TestEvaluatorLabelRequired(t *testing.T) {
	assert.Empty(t, lintLabels(t, rulecheck.LintOptions{}, labelled("{}", "prometheus")))

	vs := lintLabels(t, rulecheck.LintOptions{RequireEvaluator: true}, labelled("{}", "prometheus"))
	require.Len(t, vs, 1)
	assert.Equal(t, rulecheck.CheckEvaluatorLabel, vs[0].Check)

	assert.Empty(t, lintLabels(t, rulecheck.LintOptions{RequireEvaluator: true},
		labelled(`{observability.truvity.io/evaluator: metrics}`, "prometheus")))
	assert.Empty(t, lintLabels(t, rulecheck.LintOptions{RequireEvaluator: true},
		labelled(`{observability.truvity.io/evaluator: other-store}`, "prometheus")))
}

// A LogsQL rule is evaluated by the logs alerter only.
func TestEvaluatorLabelLogsQL(t *testing.T) {
	o := rulecheck.LintOptions{RequireEvaluator: true}

	assert.Empty(t, lintLabels(t, o, labelled(`{observability.truvity.io/evaluator: logs}`, "vlogs")))

	vs := lintLabels(t, o, labelled(`{observability.truvity.io/evaluator: metrics}`, "vlogs"))
	require.Len(t, vs, 1)
	assert.Contains(t, vs[0].Msg, "LogsQL")
}

// rule-type is checked whenever it is present, flag or not.
func TestRuleTypeLabelValue(t *testing.T) {
	for _, v := range []string{"alert", "recording"} {
		assert.Empty(t, lintLabels(t, rulecheck.LintOptions{}, labelled(`{observability.truvity.io/rule-type: `+v+`}`, "prometheus")))
	}

	vs := lintLabels(t, rulecheck.LintOptions{}, labelled(`{observability.truvity.io/rule-type: alerts}`, "prometheus"))
	require.Len(t, vs, 1)
	assert.Equal(t, rulecheck.CheckRuleTypeLabel, vs[0].Check)
}
