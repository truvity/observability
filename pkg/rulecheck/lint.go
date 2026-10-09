package rulecheck

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/VictoriaMetrics/metricsql"
)

// The semantic checks. Parsing proves an expression is valid; it says
// nothing about whether it is RIGHT on a store that holds several clusters'
// series. Four releases fixed rules that parsed, rendered and passed every
// golden, and were wrong in production:
//
//   - v0.43.3: a heartbeat alert evaluated per pod fired critical for the
//     quieter replica while heartbeats were arriving;
//   - v0.44.1: a per-cluster absent() guard compared a lookback side and a
//     current side that disagreed about series without the cluster label,
//     and paged on a healthy target;
//   - v0.45.1: self-alerts aggregated with `by (reason)` or a bare `sum()`
//     and so dropped the cluster label, arriving unroutable;
//   - v0.46.0: every self-alert reads rate() of a counter, an empty vector
//     reads as healthy, and nothing said so when the scrape stopped.
//
// Each check below is named by the class of bug it prevents.
const (
	CheckClusterLabel = "cluster-label"
	CheckAbsentGuard  = "absent-guard"
	CheckPodHeartbeat = "pod-heartbeat"
	CheckSourceAbsent = "source-absent"
)

// DefaultClusterLabel is the label the charts stamp on every series
// (observability-stack `tenancy.clusterLabel`).
const DefaultClusterLabel = "k8s_cluster_name"

// LintOptions tune the semantic checks.
type LintOptions struct {
	// ClusterLabel is the label naming a series' cluster. Empty means
	// DefaultClusterLabel.
	ClusterLabel string
	// RequireSourceAbsent makes a self-alert group without a
	// SelfAlertSourceAbsent rule a violation. Off, such a group is only
	// reported by `rulecheck coverage` (the rule is opt-in in the chart, so
	// most renders legitimately lack it); a SelfAlertSourceAbsent that
	// leaves a source out is a violation either way.
	RequireSourceAbsent bool
	// Allow are intentional exceptions; nil means DefaultAllowlist.
	Allow []Allow
}

// Allow is one intentional exception: a rule a check would flag that is
// right as it is. Every entry says why, so the list is reviewable.
type Allow struct {
	Check string
	// Alert is the alert or record name; Resource, when set, narrows it to
	// one VMRule (a suffix match, as release names prefix it).
	Alert, Resource string
	// Contains, when set, narrows the entry to violations whose message
	// contains it (a metric name, for source-absent).
	Contains string
	Reason   string
}

// DefaultAllowlist is the repository's own exceptions. Keep it short and
// narrow: each entry is a rule the check cannot tell from a bug.
var DefaultAllowlist = []Allow{
	{
		Check: CheckClusterLabel, Alert: "WritePathDead", Resource: "platform-alerts",
		Reason: "Sums ONE store's own rows counter across that store's processes; a per-cluster split would also break the " +
			"`or vector(0)` deadman. The store is the subject, not a cluster " +
			"(the same exemption tests/platform_alerts_cluster_test.go carries).",
	},
	{
		Check: CheckSourceAbsent, Alert: "SlackNotificationsFailing", Contains: "alertmanager_notifications_failed_total",
		Reason: "The same counter, in a render with no SelfAlertSourceAbsent at all (only reported under RequireSourceAbsent).",
	},
	{
		Check: CheckSourceAbsent, Alert: sourceAbsentRule, Contains: "alertmanager_notifications_failed_total",
		Reason: "SlackNotificationsFailing reads Alertmanager's own counter, a fixed name the chart states, not a metric name " +
			"set under `selfAlerts`; SelfAlertSourceAbsent watches the configured names (v0.46.0). Alertmanager's absence " +
			"is judged by the platform-alerts scrape rules, not by a self-alert source.",
	},
}

// Violation is one semantic finding.
type Violation struct {
	Rule
	Check string
	Msg   string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s\n  %s\n  expr: %s", v.Check, v.Rule, v.Msg, strings.Join(strings.Fields(v.Expr), " "))
}

// Lint runs the semantic checks over MetricsQL rules (LogsQL rules are
// skipped: their cluster field is not a metrics label).
//
// The cluster checks apply only to a VMRule that is cluster-aware, that is,
// names the cluster label somewhere: a render with the label switched off
// (platform-alerts `clusterLabel: ""`) is a single-cluster store, where
// "keep the cluster label" has no meaning. That is also what makes the check
// useful on a render that LOST the label by mistake: one expression naming
// it is enough to hold every sibling to it.
func Lint(rules []Rule, o LintOptions) ([]Violation, error) {
	cl := o.ClusterLabel
	if cl == "" {
		cl = DefaultClusterLabel
	}

	allow := o.Allow
	if allow == nil {
		allow = DefaultAllowlist
	}

	type resKey struct{ source, resource string }

	type parsed struct {
		Rule
		e metricsql.Expr
	}

	var (
		all   []parsed
		aware = map[resKey]bool{}
	)

	for _, r := range rules {
		if r.LogsQL() {
			continue
		}

		e, err := metricsql.Parse(r.Expr)
		if err != nil {
			return nil, fmt.Errorf("%s: parse %q: %w (run the parse check first)", r, r.Expr, err)
		}

		all = append(all, parsed{r, e})

		if strings.Contains(r.Expr, cl) {
			aware[resKey{r.Source, r.Resource}] = true
		}
	}

	var out []Violation

	add := func(r Rule, check, format string, a ...any) {
		out = append(out, Violation{Rule: r, Check: check, Msg: fmt.Sprintf(format, a...)})
	}

	for i := range all {
		p := &all[i]

		if aware[resKey{p.Source, p.Resource}] {
			clusterLabelCheck(p.e, cl, func(f string, a ...any) { add(p.Rule, CheckClusterLabel, f, a...) })
			absentGuardCheck(p.e, cl, func(f string, a ...any) { add(p.Rule, CheckAbsentGuard, f, a...) })
		}

		if !p.Record {
			podHeartbeatCheck(p.Rule, p.e, func(f string, a ...any) { add(p.Rule, CheckPodHeartbeat, f, a...) })
		}
	}

	// source-absent works per VMRule, over the self-alert group(s).
	byRes := map[resKey][]Rule{}

	for i := range all {
		k := resKey{all[i].Source, all[i].Resource}
		byRes[k] = append(byRes[k], all[i].Rule)
	}

	for _, rs := range byRes {
		gaps := sourceAbsentGaps(rs)

		for i := range gaps {
			m := &gaps[i]

			if m.noRule && !o.RequireSourceAbsent {
				continue
			}

			add(m.rule, CheckSourceAbsent, "%s", m.msg)
		}
	}

	out = filterAllowed(out, allow)

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}

		if a.Group != b.Group {
			return a.Group < b.Group
		}

		if a.Name != b.Name {
			return a.Name < b.Name
		}

		return a.Check < b.Check
	})

	return out, nil
}

func filterAllowed(vs []Violation, allow []Allow) []Violation {
	var out []Violation

next:
	for i := range vs {
		v := &vs[i]

		for _, a := range allow {
			if a.Check == v.Check && a.Alert == v.Name &&
				(a.Resource == "" || strings.HasSuffix(v.Resource, a.Resource)) &&
				(a.Contains == "" || strings.Contains(v.Msg, a.Contains)) {
				continue next
			}
		}

		out = append(out, *v)
	}

	return out
}

// pathElem is one step from the root of an expression to a node; side is
// which child of a binary operation the walk went into (0 left, 1 right).
type pathElem struct {
	e    metricsql.Expr
	side int
}

// walk visits every node with the path of its ancestors (root first, the
// node itself not included).
func walk(e metricsql.Expr, path []pathElem, fn func(metricsql.Expr, []pathElem)) {
	fn(e, path)

	child := func(c metricsql.Expr, side int) {
		if c != nil {
			walk(c, append(append([]pathElem(nil), path...), pathElem{e, side}), fn)
		}
	}

	switch t := e.(type) {
	case *metricsql.AggrFuncExpr:
		for _, a := range t.Args {
			child(a, 0)
		}
	case *metricsql.FuncExpr:
		for _, a := range t.Args {
			child(a, 0)
		}
	case *metricsql.BinaryOpExpr:
		child(t.Left, 0)
		child(t.Right, 1)
	case *metricsql.RollupExpr:
		child(t.Expr, 0)
	default:
		// metricsql keeps parentheses in an unexported []Expr type.
		if v := reflect.ValueOf(e); v.Kind() == reflect.Slice {
			for i := range v.Len() {
				if c, ok := v.Index(i).Interface().(metricsql.Expr); ok {
					child(c, 0)
				}
			}
		}
	}
}

func contains(e metricsql.Expr, pred func(metricsql.Expr) bool) bool {
	found := false

	walk(e, nil, func(n metricsql.Expr, _ []pathElem) {
		if pred(n) {
			found = true
		}
	})

	return found
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}

	return false
}

// aggregators are the aggregations that collapse series and so drop every
// label they are not told to keep. topk/bottomk/limitk/any and friends
// return whole series and are left out on purpose.
var aggregators = map[string]bool{
	"sum": true, "sum2": true, "avg": true, "min": true, "max": true, "count": true,
	"group": true, "stddev": true, "stdvar": true, "quantile": true, "quantiles": true,
	"median": true, "mad": true, "mode": true, "geomean": true, "distinct": true,
	"count_values": true, "histogram": true,
}

func emptyOn(b *metricsql.BinaryOpExpr) bool {
	return strings.EqualFold(b.GroupModifier.Op, "on") && len(b.GroupModifier.Args) == 0
}

// clusterLabelCheck: an aggregation must keep the cluster label, and an
// `on(...)` match must include it. Deliberately cluster-blind spots are
// exempt: an operand of `on()` (an empty match is "the whole store", the
// absent() guard's shape) and anything under scalar().
func clusterLabelCheck(e metricsql.Expr, cl string, report func(string, ...any)) {
	walk(e, nil, func(n metricsql.Expr, path []pathElem) {
		switch t := n.(type) {
		case *metricsql.AggrFuncExpr:
			if !aggregators[strings.ToLower(t.Name)] || globalScope(path) {
				return
			}

			m := t.Modifier
			switch {
			case strings.EqualFold(m.Op, "by") && has(m.Args, cl):
			case strings.EqualFold(m.Op, "without") && !has(m.Args, cl):
			case strings.EqualFold(m.Op, "by"):
				report("%s drops the cluster label: `by (%s)` has no %s, so the result cannot be routed or told apart per cluster", t.Name, strings.Join(m.Args, ", "), cl)
			case strings.EqualFold(m.Op, "without"):
				report("%s drops the cluster label: `without (%s)` removes %s", t.Name, strings.Join(m.Args, ", "), cl)
			default:
				report("%s drops the cluster label: no `by (..., %s)` or `without (...)`, so every label is gone", t.Name, cl)
			}
		case *metricsql.BinaryOpExpr:
			if strings.EqualFold(t.GroupModifier.Op, "on") && len(t.GroupModifier.Args) > 0 && !has(t.GroupModifier.Args, cl) {
				report("`%s on (%s)` matches series across clusters: add %s to the match", t.Op, strings.Join(t.GroupModifier.Args, ", "), cl)
			}
		}
	})
}

// globalScope reports whether the node sits under an operand of an empty
// `on()` or under scalar(): places where the whole store is meant.
func globalScope(path []pathElem) bool {
	for _, p := range path {
		switch t := p.e.(type) {
		case *metricsql.BinaryOpExpr:
			if emptyOn(t) {
				return true
			}
		case *metricsql.FuncExpr:
			if strings.EqualFold(t.Name, "scalar") {
				return true
			}
		}
	}

	return false
}

func isFunc(e metricsql.Expr, names ...string) bool {
	f, ok := e.(*metricsql.FuncExpr)
	if !ok {
		return false
	}

	for _, n := range names {
		if strings.EqualFold(f.Name, n) {
			return true
		}
	}

	return false
}

// absentGuardCheck: every absent() carries the per-cluster guard the
// platform-alerts chart renders (see its `absentGuard` helper):
//
//	(group by (<cluster>) (max_over_time(sel{<cluster>!=""}[lookback]))
//	   unless group by (<cluster>) (sel{<cluster>!=""}))
//	or (absent(sel) unless on() group(max_over_time(sel[lookback])))
//
// A bare absent() is false while ANY cluster still exports the series, so
// one cluster going dark is silent. The guard has two parts and both are
// required: the whole-store absent() is left-hand of `unless on()` with a
// max_over_time on the right, and is the arm of an `or` whose other arm is
// the per-cluster comparison (an `unless` over an aggregation that keeps
// the cluster label).
func absentGuardCheck(e metricsql.Expr, cl string, report func(string, ...any)) {
	walk(e, nil, func(n metricsql.Expr, path []pathElem) {
		if !isFunc(n, "absent") {
			return
		}

		whole := false
		perCluster, lax := false, false

		for _, p := range path {
			if b, ok := p.e.(*metricsql.BinaryOpExpr); ok && strings.EqualFold(b.Op, "unless") && emptyOn(b) && p.side == 0 &&
				contains(b.Right, func(x metricsql.Expr) bool { return isFunc(x, "max_over_time") }) {
				whole = true
			}
		}

		for _, p := range path {
			if b, ok := p.e.(*metricsql.BinaryOpExpr); ok && whole && strings.EqualFold(b.Op, "or") {
				other := b.Right
				if p.side == 1 {
					other = b.Left
				}

				if found, strict := keepsClusterUnless(other, cl); found {
					perCluster = true
					lax = lax || !strict
				}
			}
		}

		switch {
		case perCluster && lax:
			report(`the per-cluster arm of this absent() guard does not require %s!="" on both sides: `+
				"a stale series without the label holds the alert true on a healthy target", cl)
		case perCluster:
		case whole:
			report("absent() has the whole-store lookback guard but no per-cluster arm: one cluster going dark is silent while another still exports the series")
		default:
			report("absent() has no per-cluster guard: it is false while any cluster still exports the series. "+
				"Use `(group by (%s) (max_over_time(sel{%s!=\"\"}[lookback])) unless group by (%s) (sel{%s!=\"\"})) "+
				"or (absent(sel) unless on() group(max_over_time(sel[lookback])))`", cl, cl, cl, cl)
		}
	})
}

// keepsClusterUnless: e holds an `unless` whose left side aggregates by the
// cluster label over a max_over_time (the lookback side). strict is false
// when some selector on either side of it does not require `<cl>!=""`: a
// stale series without the label would sit on one side under a label set
// the other can never match, and the alert would fire on a healthy target
// until it aged out (v0.44.1).
func keepsClusterUnless(e metricsql.Expr, cl string) (found, strict bool) {
	strict = true

	contains(e, func(n metricsql.Expr) bool {
		b, ok := n.(*metricsql.BinaryOpExpr)
		if !ok || !strings.EqualFold(b.Op, "unless") {
			return false
		}

		if !contains(b.Left, func(x metricsql.Expr) bool {
			a, ok := x.(*metricsql.AggrFuncExpr)

			return ok && strings.EqualFold(a.Modifier.Op, "by") && has(a.Modifier.Args, cl) &&
				contains(a, func(y metricsql.Expr) bool { return isFunc(y, "max_over_time") })
		}) {
			return false
		}

		found = true

		for _, side := range []metricsql.Expr{b.Left, b.Right} {
			contains(side, func(x metricsql.Expr) bool {
				if m, ok := x.(*metricsql.MetricExpr); ok && !requiresCluster(m, cl) {
					strict = false
				}

				return false
			})
		}

		return false
	})

	return found, strict
}

func requiresCluster(m *metricsql.MetricExpr, cl string) bool {
	for _, group := range m.LabelFilterss {
		ok := false

		for _, f := range group {
			if f.Label == cl && !f.IsRegexp && f.IsNegative && f.Value == "" {
				ok = true
			}
		}

		if !ok {
			return false
		}
	}

	return len(m.LabelFilterss) > 0
}

var (
	heartbeatName = regexp.MustCompile(`(?i)heartbeat|dead.?man`)
	absentName    = regexp.MustCompile(`(?i)absent`)
)

// podLabels are the labels that name one replica.
var podLabels = []string{"pod", "instance"}

// podHeartbeatCheck: a heartbeat is delivered to ONE replica behind a
// Service, so an alert on it must be evaluated over the sum of the
// replicas, never per pod. A heartbeat-like alert (by name, or an
// expression naming a heartbeat) must read every series through an
// aggregation that drops pod and instance; an absent/missing alert must
// not key on pod identity.
func podHeartbeatCheck(r Rule, e metricsql.Expr, report func(string, ...any)) {
	hb := heartbeatName.MatchString(r.Name) || strings.Contains(strings.ToLower(r.Expr), "heartbeat")
	ab := absentName.MatchString(r.Name)

	if !hb && !ab {
		return
	}

	walk(e, nil, func(n metricsql.Expr, path []pathElem) {
		switch t := n.(type) {
		case *metricsql.AggrFuncExpr:
			if strings.EqualFold(t.Modifier.Op, "by") && (has(t.Modifier.Args, "pod") || has(t.Modifier.Args, "instance")) {
				report("%s is keyed on pod identity: `by (%s)`. "+
					"A heartbeat or absent alert must sum the replicas (`sum without (pod, instance) (...)`)", t.Name, strings.Join(t.Modifier.Args, ", "))
			}
		case *metricsql.MetricExpr:
			if !hb {
				return
			}

			for _, p := range path {
				if isFunc(p.e, "absent", "absent_over_time") {
					return
				}

				// A `by (..., pod)` ancestor was reported above.
				if a, ok := p.e.(*metricsql.AggrFuncExpr); ok && (dropsPod(a) || strings.EqualFold(a.Modifier.Op, "by")) {
					return
				}
			}

			report("heartbeat series %s is evaluated per pod: wrap it in `sum without (pod, instance) (...)`, "+
				"or the quieter replica pages while heartbeats arrive", selectorName(t))
		}
	})
}

func dropsPod(a *metricsql.AggrFuncExpr) bool {
	if !aggregators[strings.ToLower(a.Name)] {
		return false
	}

	switch strings.ToLower(a.Modifier.Op) {
	case "by":
		return !has(a.Modifier.Args, "pod") && !has(a.Modifier.Args, "instance")
	case "without":
		for _, l := range podLabels {
			if !has(a.Modifier.Args, l) {
				return false
			}
		}

		return true
	default:
		return true
	}
}

func selectorName(m *metricsql.MetricExpr) string {
	if len(m.LabelFilterss) > 0 {
		for _, f := range m.LabelFilterss[0] {
			if f.Label == "__name__" {
				return f.Value
			}
		}
	}

	return string(m.AppendString(nil))
}

// selectorNames returns the metric names a selector can match: one, or the
// alternatives of an anchored `__name__=~"a|b"` regexp of plain names.
func selectorNames(m *metricsql.MetricExpr) []string {
	if len(m.LabelFilterss) > 0 {
		for _, f := range m.LabelFilterss[0] {
			if f.Label == "__name__" && f.IsRegexp && !f.IsNegative {
				return strings.Split(f.Value, "|")
			}
		}
	}

	return []string{selectorName(m)}
}

// metricNames returns the metric names selected by an expression.
func metricNames(e metricsql.Expr) []string {
	seen := map[string]bool{}

	var out []string

	walk(e, nil, func(n metricsql.Expr, _ []pathElem) {
		if m, ok := n.(*metricsql.MetricExpr); ok {
			for _, name := range selectorNames(m) {
				if name != "" && !seen[name] && !strings.ContainsAny(name, "{}") {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
	})

	sort.Strings(out)

	return out
}

// sourceAbsentRule is the name of the rule the stack chart renders for
// `selfAlerts.sourceAbsent`.
const sourceAbsentRule = "SelfAlertSourceAbsent"

// externalPrefixes are metric families the self-alerts read from other
// exporters (kube-state-metrics, cAdvisor, node-exporter); the chart leaves
// them out of sourceAbsent on purpose (a snapshot rule over them is judged
// by the platform-alerts absent guards).
var externalPrefixes = []string{"kube_", "container_", "node_", "ALERTS", "up", "vector"}

type sourceGap struct {
	rule   Rule
	msg    string
	noRule bool
}

// selfAlertSources lists, per self-alert rule of a VMRule, the source
// metrics it reads; the coverage report and the check share it.
func selfAlertSources(rs []Rule) (sources map[string][]string, sa *Rule) {
	sources = map[string][]string{}

	for i, r := range rs {
		if r.LogsQL() || r.Record || !strings.HasSuffix(r.Group, "selfalerts") {
			continue
		}

		if r.Name == sourceAbsentRule {
			sa = &rs[i]

			continue
		}

		e, err := metricsql.Parse(r.Expr)
		if err != nil {
			continue
		}

	names:
		for _, n := range metricNames(e) {
			for _, p := range externalPrefixes {
				if n == p || (strings.HasSuffix(p, "_") && strings.HasPrefix(n, p)) {
					continue names
				}
			}

			sources[n] = append(sources[n], r.Name)
		}
	}

	return sources, sa
}

// sourceAbsentGaps: every metric a self-alert reads must appear in
// SelfAlertSourceAbsent, or the day the scrape stops that alert reads an
// empty vector as healthy.
func sourceAbsentGaps(rs []Rule) []sourceGap {
	sources, sa := selfAlertSources(rs)
	if len(sources) == 0 {
		return nil
	}

	var out []sourceGap

	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}

	sort.Strings(names)

	if sa == nil {
		// Report once per source, on the first alert that reads it.
		for _, n := range names {
			r := firstRule(rs, sources[n][0])
			out = append(out, sourceGap{rule: r, noRule: true,
				msg: fmt.Sprintf("self-alert source %s (read by %s) has no sourceAbsent coverage: "+
					"no SelfAlertSourceAbsent rule is rendered (selfAlerts.sourceAbsent.enabled)", n, strings.Join(sources[n], ", "))})
		}

		return out
	}

	e, err := metricsql.Parse(sa.Expr)
	if err != nil {
		return nil
	}

	covered := map[string]bool{}
	for _, n := range metricNames(e) {
		covered[n] = true
	}

	for _, n := range names {
		if !covered[n] {
			out = append(out, sourceGap{rule: *sa,
				msg: fmt.Sprintf("self-alert source %s (read by %s) is not watched by SelfAlertSourceAbsent", n, strings.Join(sources[n], ", "))})
		}
	}

	return out
}

func firstRule(rs []Rule, name string) Rule {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}

	return Rule{}
}
