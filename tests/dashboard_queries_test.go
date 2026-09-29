// This file holds the operational dashboards (every catalog entry flagged
// `queryCheck: true` in charts/observability-dashboards/dashboards/
// catalog.yaml) to three claims a lint of the JSON's SHAPE cannot make:
//
//  1. Every panel query and every variable query is valid MetricsQL: parsed
//     by a real VictoriaMetrics, at the version the stack pins, after its
//     Grafana variables are substituted with sample values. Needs Docker
//     (the same reason the hack/*-proof.sh scripts do); with no Docker the
//     step SKIPS, and DASHBOARD_VM=require (the `just dashboard-queries`
//     recipe) turns that skip into a failure.
//  2. Every metric a query reads is on the allow-list of metrics a store
//     actually holds (hack/dashboards/available-metrics.yaml). Needs no
//     Docker, so `go test ./...` always runs it. This is the guard that
//     keeps a dashboard for node-exporter, ArgoCD, cert-manager, CNPG,
//     NATS, Envoy Gateway or Karpenter from shipping empty: it fails until
//     the metric source ships and its family is added to the list.
//  3. Every panel says what it shows (a description) and how it is
//     measured (a unit, on the panel types that have one).
//  4. Every equality matcher on a scrape-derived label (job, metrics_path,
//     instance, node, the tenancy labels, ...) can match: the metric's
//     scrape job carries that label, with that value
//     (hack/dashboards/available-labels.yaml). Applies to EVERY shipped
//     dashboard, not only the queryCheck ones, except those that declare
//     `requires:` an optional source. Claim 2 alone let a kubelet dashboard
//     whose every selector could never match ship with "No data".
//
// Metric names are read from the parsed expression tree (the Prometheus
// parser, which the VictoriaMetrics run then confirms as MetricsQL), not
// from a regular expression: a label or a function name is never mistaken
// for a metric.
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	dashboardsDir     = "../charts/observability-dashboards/dashboards"
	availableMetrics  = "../hack/dashboards/available-metrics.yaml"
	availableLabels   = "../hack/dashboards/available-labels.yaml"
	stackChartArchive = "../charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz"
)

// ---- the allow-list ---------------------------------------------------

type metricSource struct {
	Name         string   `yaml:"name"`
	Names        []string `yaml:"names"`
	Prefixes     []string `yaml:"prefixes"`
	Deny         []string `yaml:"deny"`
	DenySuffixes []string `yaml:"denySuffixes"`
	Keep         []string `yaml:"keep"`
}

func loadAvailableMetrics(t testing.TB) []metricSource {
	t.Helper()
	raw, err := os.ReadFile(availableMetrics)
	require.NoError(t, err)
	var doc struct {
		Sources []metricSource `yaml:"sources"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Sources)
	return doc.Sources
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func metricAvailable(sources []metricSource, name string) bool {
	for i := range sources {
		s := &sources[i]
		if has(s.Deny, name) {
			continue
		}
		suffixed := false
		for _, suf := range s.DenySuffixes {
			if strings.HasSuffix(name, suf) {
				suffixed = true
			}
		}
		if suffixed && !has(s.Keep, name) {
			continue
		}
		if has(s.Names, name) || has(s.Keep, name) {
			return true
		}
		for _, p := range s.Prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
	}
	return false
}

// The allow-list must say what the survey says: the families a store holds
// are in, and every family that does not exist yet is out. If a source
// ships and this list grows, this test grows with it, in the same change.
func TestAvailableMetricsAllowListMatchesTheSurvey(t *testing.T) {
	src := loadAvailableMetrics(t)
	for _, m := range []string{
		"container_cpu_usage_seconds_total", "container_memory_working_set_bytes",
		"container_cpu_cfs_throttled_seconds_total", "container_oom_events_total",
		"go_sched_latencies_seconds_bucket", "kubelet_volume_stats_used_bytes",
		"kube_pod_status_phase", "kube_node_status_condition", "kube_persistentvolumeclaim_info",
		"kube_horizontalpodautoscaler_labels", "kargo_stage_condition", "kargo_promotion_phase",
		"ALERTS", "ALERTS_FOR_STATE", "vm_rows_inserted_total", "vmagent_remotewrite_requests_total",
		"vmalert_alerts_firing", "vl_rows_ingested_total", "vt_rows_dropped_total", "up",
	} {
		assert.Truef(t, metricAvailable(src, m), "%s is held by a store and must be on the allow-list", m)
	}
	for _, m := range []string{
		// node-exporter: no install runs it yet
		"node_cpu_seconds_total", "node_memory_MemAvailable_bytes", "node_network_receive_bytes_total",
		// the cadvisor churn drop (0.9.1)
		"container_tasks_state", "container_memory_failures_total", "container_blkio_device_usage_total",
		"container_cpu_cfs_periods_bucket",
		// kube-state-metrics kinds outside the 11-collector allow-list
		"kube_configmap_info", "kube_secret_info", "kube_service_info", "kube_endpoint_info",
		"kube_ingress_info", "kube_networkpolicy_labels", "kube_hpa_labels",
		// later phases
		"argocd_app_info", "certmanager_certificate_ready_status", "cnpg_collector_up",
		"nats_server_connections", "envoy_cluster_upstream_rq_total", "karpenter_nodes_total",
	} {
		assert.Falsef(t, metricAvailable(src, m), "%s is not held by any store today and must stay off the allow-list until its source ships", m)
	}
}

// ---- reading the dashboards ------------------------------------------

type catalogEntry struct {
	File       string   `yaml:"file"`
	QueryCheck bool     `yaml:"queryCheck"`
	Requires   []string `yaml:"requires"`
}

// allDashboards returns every catalog entry, checked or not.
func allDashboards(t testing.TB) map[string]catalogEntry {
	t.Helper()
	raw, err := os.ReadFile(dashboardsDir + "/catalog.yaml")
	require.NoError(t, err)
	var cat map[string]catalogEntry
	require.NoError(t, yaml.Unmarshal(raw, &cat))
	require.NotEmpty(t, cat)
	return cat
}

func checkedDashboards(t testing.TB) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(dashboardsDir + "/catalog.yaml")
	require.NoError(t, err)
	var cat map[string]catalogEntry
	require.NoError(t, yaml.Unmarshal(raw, &cat))
	out := map[string]string{}
	for name, e := range cat {
		if e.QueryCheck {
			out[name] = dashboardsDir + "/" + e.File
		}
	}
	require.NotEmpty(t, out, "no dashboard is flagged queryCheck: this test would pass vacuously")
	return out
}

type dashPanel struct {
	Title       string `json:"title"`
	Type        string `json:"type"`
	Description string `json:"description"`
	FieldConfig struct {
		Defaults struct {
			Unit string `json:"unit"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	Panels []dashPanel `json:"panels"`
}

type dashboard struct {
	Title      string      `json:"title"`
	Panels     []dashPanel `json:"panels"`
	Templating struct {
		List []struct {
			Name       string          `json:"name"`
			Type       string          `json:"type"`
			Definition string          `json:"definition"`
			Query      json.RawMessage `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

func (p dashPanel) flatten() []dashPanel {
	out := []dashPanel{p}
	for _, s := range p.Panels {
		out = append(out, s.flatten()...)
	}
	return out
}

func loadDashboard(t testing.TB, path string) dashboard {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var d dashboard
	require.NoError(t, json.Unmarshal(raw, &d))
	return d
}

// A query, with where it came from, so a failure names the panel.
type dashQuery struct {
	where string
	expr  string
}

var (
	labelValuesRe = regexp.MustCompile(`^\s*label_values\((.*),\s*[a-zA-Z_][a-zA-Z0-9_]*\s*\)\s*$`)
	intervalVarRe = regexp.MustCompile(`\$\{?__(?:rate_interval|interval|range)\}?|\$\{?resolution\}?`)
	msVarRe       = regexp.MustCompile(`\$\{?__interval_ms\}?`)
)

// queries returns every expression the dashboard sends to the store: each
// panel target, and each query variable (a label_values(<selector>, label)
// contributes its selector).
func (d dashboard) queries() []dashQuery {
	var out []dashQuery
	for _, top := range d.Panels {
		for _, p := range top.flatten() {
			for i, tg := range p.Targets {
				if strings.TrimSpace(tg.Expr) != "" {
					out = append(out, dashQuery{fmt.Sprintf("panel %q target %d", p.Title, i), tg.Expr})
				}
			}
		}
	}
	for _, v := range d.Templating.List {
		if v.Type != "query" {
			continue
		}
		q := v.Definition
		if q == "" {
			var obj struct {
				Query string `json:"query"`
			}
			if json.Unmarshal(v.Query, &obj) == nil {
				q = obj.Query
			}
		}
		if m := labelValuesRe.FindStringSubmatch(q); m != nil {
			q = m[1]
		}
		if strings.TrimSpace(q) != "" {
			out = append(out, dashQuery{fmt.Sprintf("variable %q", v.Name), q})
		}
	}
	return out
}

// sampleValue is the value a template variable takes in a sample query.
func sampleValue(name string) string {
	switch name {
	case "cluster":
		return "cluster-a"
	case "namespace":
		return "namespace-a"
	case "pod":
		return "pod-a"
	}
	return "sample-" + name
}

// substitute replaces Grafana variables with sample values a real query
// could take: intervals become durations, every template variable a single
// sample value.
func (d dashboard) substitute(expr string) string {
	return d.substituteWith(expr, sampleValue)
}

func (d dashboard) substituteWith(expr string, sampleFor func(string) string) string {
	expr = msVarRe.ReplaceAllString(expr, "60000")
	expr = intervalVarRe.ReplaceAllString(expr, "5m")
	names := []string{}
	for _, v := range d.Templating.List {
		names = append(names, v.Name)
	}
	// Longest first, so $namespace is not read as $name + "space".
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		sample := sampleFor(n)
		expr = strings.ReplaceAll(expr, "${"+n+"}", sample)
		expr = regexp.MustCompile(`\$`+regexp.QuoteMeta(n)+`\b`).ReplaceAllString(expr, sample)
	}
	return expr
}

// metricNames parses one expression and returns every metric name it
// selects.
func metricNames(expr string) ([]string, error) {
	e, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	parser.Inspect(e, func(n parser.Node, _ []parser.Node) error {
		if vs, ok := n.(*parser.VectorSelector); ok {
			if vs.Name != "" {
				seen[vs.Name] = true
			}
			for _, m := range vs.LabelMatchers {
				if m.Name == model.MetricNameLabel && m.Type == labels.MatchEqual {
					seen[m.Value] = true
				}
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func TestOperationalDashboardsReadOnlyAvailableMetrics(t *testing.T) {
	src := loadAvailableMetrics(t)
	for name, path := range checkedDashboards(t) {
		d := loadDashboard(t, path)
		qs := d.queries()
		require.NotEmptyf(t, qs, "%s: no queries found; this test would pass vacuously", name)
		for _, q := range qs {
			names, err := metricNames(d.substitute(q.expr))
			require.NoErrorf(t, err, "%s: %s: query does not parse: %s", name, q.where, q.expr)
			for _, m := range names {
				assert.Truef(t, metricAvailable(src, m),
					"%s: %s reads %q, which no store holds today (hack/dashboards/available-metrics.yaml). "+
						"Ship the metric source first, then add its family to that list.\n  query: %s", name, q.where, m, q.expr)
			}
		}
	}
}

// The allow-list guard must fail on exactly what it exists to catch.
func TestTheAllowListGuardRejectsAnAbsentMetric(t *testing.T) {
	src := loadAvailableMetrics(t)
	names, err := metricNames(`sum(rate(node_cpu_seconds_total{mode!="idle",k8s_cluster_name=~"c"}[5m])) / sum(machine_cpu_cores)`)
	require.NoError(t, err)
	assert.Equal(t, []string{"machine_cpu_cores", "node_cpu_seconds_total"}, names)
	assert.False(t, metricAvailable(src, "node_cpu_seconds_total"))
	assert.True(t, metricAvailable(src, "machine_cpu_cores"))
}

func TestOperationalDashboardPanelsAreDescribedAndHaveUnits(t *testing.T) {
	for name, path := range checkedDashboards(t) {
		d := loadDashboard(t, path)
		for _, top := range d.Panels {
			for _, p := range top.flatten() {
				if p.Type == "row" {
					continue
				}
				assert.NotEmptyf(t, strings.TrimSpace(p.Description), "%s: panel %q has no description (what it shows, what to do)", name, p.Title)
				switch p.Type {
				case "stat", "gauge", "bargauge", "timeseries":
					assert.NotEmptyf(t, p.FieldConfig.Defaults.Unit, "%s: panel %q has no unit", name, p.Title)
				}
			}
		}
	}
}

// ---- a real VictoriaMetrics -------------------------------------------

func pinnedVMVersion(t testing.TB) string {
	t.Helper()
	// The version charts/observability-stack pins: the vendored stack
	// chart's appVersion, the same source hack/*-proof.sh read.
	out, err := exec.Command("bash", "-c",
		`f=$(ls `+stackChartArchive+` | head -1); tar -xzOf "$f" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ {print $2}'`).Output()
	require.NoError(t, err, "cannot read the pinned VictoriaMetrics version from the vendored stack chart")
	v := strings.TrimSpace(string(out))
	require.Regexp(t, `^v\d+\.\d+\.\d+$`, v)
	return v
}

func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestOperationalDashboardQueriesParseOnTheRealStore(t *testing.T) {
	skip := func(msg string) {
		if os.Getenv("DASHBOARD_VM") == "require" {
			t.Fatal(msg)
		}
		t.Skip(msg + " (set DASHBOARD_VM=require, or run `just dashboard-queries`, to make this a failure)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skip("docker is not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		skip("the docker daemon is not reachable")
	}

	version := pinnedVMVersion(t)
	port := freePort(t)
	name := fmt.Sprintf("dashboard-queries-vm-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:8428", port),
		"victoriametrics/victoria-metrics:"+version, "-retentionPeriod=1d").CombinedOutput()
	require.NoErrorf(t, err, "starting victoria-metrics %s: %s", version, out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	t.Logf("parsing against victoria-metrics %s", version)

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	require.Eventually(t, func() bool {
		r, err := http.Get(base + "/health")
		if err != nil {
			return false
		}
		defer func() { _ = r.Body.Close() }()
		return r.StatusCode == 200
	}, 60*time.Second, 500*time.Millisecond, "victoria-metrics did not become healthy")

	total := 0
	for dname, path := range checkedDashboards(t) {
		d := loadDashboard(t, path)
		for _, q := range d.queries() {
			expr := d.substitute(q.expr)
			// An empty store answers a valid query with an empty result; an
			// invalid one with 4xx and the parse error. Instant queries use
			// the same parser as range queries.
			resp, err := http.PostForm(base+"/api/v1/query", url.Values{"query": {expr}})
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var r struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			_ = json.Unmarshal(body, &r)
			assert.Truef(t, resp.StatusCode == 200 && r.Status == "success",
				"%s: %s does not parse on victoria-metrics %s: %s\n  query: %s", dname, q.where, version, strings.TrimSpace(string(body)), expr)
			total++
		}
	}
	t.Logf("%d queries parsed on victoria-metrics %s", total, version)
}

// ---- label matchers ---------------------------------------------------

type labelJob struct {
	metricSource `yaml:",inline"`
	Labels       []string            `yaml:"labels"`
	Values       map[string][]string `yaml:"values"`
}

type labelSpec struct {
	ScrapeLabels    []string          `yaml:"scrapeLabels"`
	OptionalSources map[string]string `yaml:"optionalSources"`
	Jobs            []labelJob        `yaml:"jobs"`
}

func loadAvailableLabels(t testing.TB) labelSpec {
	t.Helper()
	raw, err := os.ReadFile(availableLabels)
	require.NoError(t, err)
	var spec labelSpec
	require.NoError(t, yaml.Unmarshal(raw, &spec))
	require.NotEmpty(t, spec.Jobs)
	require.NotEmpty(t, spec.ScrapeLabels)
	return spec
}

type selMatcher struct{ name, value string }

// selector is one metric selector, with its equality matchers.
type selector struct {
	metric string
	eq     []selMatcher
}

var (
	selectorRe = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]*)\s*\{([^{}]*)\}`)
	matcherRe  = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"`)
)

// selectors returns every metric selector of expr with its equality
// matchers. The Prometheus parser reads what it can; MetricsQL-only syntax
// (`default`, `keep_metric_names`, ...) it cannot, and those queries fall
// back to a scan for `name{...}`, which is all this check needs.
func selectors(expr string, parseable func(string) string) []selector {
	var out []selector
	if e, err := parser.NewParser(parser.Options{}).ParseExpr(parseable(expr)); err == nil {
		parser.Inspect(e, func(n parser.Node, _ []parser.Node) error {
			if vs, ok := n.(*parser.VectorSelector); ok {
				sel := selector{metric: vs.Name}
				for _, m := range vs.LabelMatchers {
					switch {
					case m.Name == model.MetricNameLabel && m.Type == labels.MatchEqual:
						sel.metric = m.Value
					case m.Type == labels.MatchEqual && m.Name != model.MetricNameLabel:
						sel.eq = append(sel.eq, selMatcher{m.Name, m.Value})
					}
				}
				out = append(out, sel)
			}
			return nil
		})
		return out
	}
	text := parseable(expr)
	for _, m := range selectorRe.FindAllStringSubmatch(text, -1) {
		sel := selector{metric: m[1]}
		for _, mm := range matcherRe.FindAllStringSubmatch(m[2], -1) {
			if mm[2] == "=" {
				sel.eq = append(sel.eq, selMatcher{mm[1], mm[3]})
			}
		}
		out = append(out, sel)
	}
	return out
}

// unsatisfiable returns one message per equality matcher in expr that no
// job can ever satisfy. Metrics no job selects are not judged here (the
// allow-list test owns names); nor are label values that are Grafana
// variables (sample values), nor `!=`/regex matchers.
func (spec labelSpec) unsatisfiable(d dashboard, expr string) []string {
	// A variable used as a number (`topk($topk, ...)`) does not parse with
	// a string sample; try that first, then a number.
	sampleFor := sampleValue
	if _, err := parser.NewParser(parser.Options{}).ParseExpr(d.substitute(expr)); err != nil {
		if _, err := parser.NewParser(parser.Options{}).ParseExpr(d.substituteWith(expr, func(string) string { return "1" })); err == nil {
			sampleFor = func(string) string { return "1" }
		}
	}
	scrape := map[string]bool{}
	for _, l := range spec.ScrapeLabels {
		scrape[l] = true
	}
	samples := []string{}
	for _, v := range d.Templating.List {
		samples = append(samples, sampleFor(v.Name))
	}
	isVariable := func(val string) bool {
		for _, s := range samples {
			if strings.Contains(val, s) && (s != "1" || val == s) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, sel := range selectors(expr, func(x string) string { return d.substituteWith(x, sampleFor) }) {
		if sel.metric == "" {
			continue
		}
		var jobs []*labelJob
		for i := range spec.Jobs {
			if metricAvailable([]metricSource{spec.Jobs[i].metricSource}, sel.metric) {
				jobs = append(jobs, &spec.Jobs[i])
			}
		}
		if len(jobs) == 0 {
			continue
		}
		for _, m := range sel.eq {
			if !scrape[m.name] || m.value == "" || isVariable(m.value) {
				continue
			}
			ok := false
			for _, j := range jobs {
				if !has(j.Labels, m.name) {
					continue
				}
				if vals, fixed := j.Values[m.name]; fixed && !has(vals, m.value) {
					continue
				}
				ok = true
			}
			if ok {
				continue
			}
			var why []string
			for _, j := range jobs {
				if !has(j.Labels, m.name) {
					why = append(why, fmt.Sprintf("job %q never carries label %q", j.Name, m.name))
				} else {
					why = append(why, fmt.Sprintf("job %q sets %s only to %v", j.Name, m.name, j.Values[m.name]))
				}
			}
			out = append(out, fmt.Sprintf("%s{%s=%q}: %s", sel.metric, m.name, m.value, strings.Join(why, "; ")))
		}
	}
	return out
}

// TestDashboardLabelMatchersCanMatch is claim 4: no shipped dashboard
// selects on a scrape-derived label its metric's job never carries.
func TestDashboardLabelMatchersCanMatch(t *testing.T) {
	spec := loadAvailableLabels(t)
	total, exempt := 0, 0
	for name, e := range allDashboards(t) {
		if len(e.Requires) > 0 {
			for _, r := range e.Requires {
				_, known := spec.OptionalSources[r]
				assert.Truef(t, known, "%s: requires %q, which is not an optionalSource in hack/dashboards/available-labels.yaml", name, r)
			}
			exempt++
			continue
		}
		d := loadDashboard(t, dashboardsDir+"/"+e.File)
		qs := d.queries()
		require.NotEmptyf(t, qs, "%s: no queries found; this test would pass vacuously", name)
		for _, q := range qs {
			for _, b := range spec.unsatisfiable(d, q.expr) {
				assert.Failf(t, "unsatisfiable selector",
					"%s: %s can never match: %s\n  (hack/dashboards/available-labels.yaml; if the chart is what is missing, fix its relabel config)\n  query: %s",
					name, q.where, b, q.expr)
			}
			total++
		}
	}
	require.NotZero(t, total)
	t.Logf("%d queries checked; %d dashboards exempt through requires:", total, exempt)
}

// The guard must fail on exactly the bug it exists for: the kubelet
// dashboard's selectors against a job that does not carry metrics_path.
func TestTheLabelGuardRejectsWhatItExistsFor(t *testing.T) {
	spec := loadAvailableLabels(t)
	d := dashboard{}
	bad := spec.unsatisfiable(d, `up{job="kubelet", metrics_path="/metrics", k8s_cluster_name="c"}`)
	assert.Empty(t, bad, "the chart's kubelet job carries job=kubelet and metrics_path=/metrics")

	// The pre-fix state: the job's label set without metrics_path.
	for i := range spec.Jobs {
		var kept []string
		for _, l := range spec.Jobs[i].Labels {
			if l != "metrics_path" {
				kept = append(kept, l)
			}
		}
		spec.Jobs[i].Labels = kept
	}
	bad = spec.unsatisfiable(d, `up{job="kubelet", metrics_path="/metrics"}`)
	require.Len(t, bad, 1)
	assert.Contains(t, bad[0], `never carries label "metrics_path"`)

	spec = loadAvailableLabels(t)
	// A value the chart never produces, and a label of another job.
	bad = spec.unsatisfiable(d, `kubelet_running_pods{metrics_path="/metrics/cadvisor"}`)
	assert.Len(t, bad, 1)
	bad = spec.unsatisfiable(d, `container_cpu_usage_seconds_total{job="kubelet"}`)
	assert.Len(t, bad, 1)
	bad = spec.unsatisfiable(d, `kube_pod_info{cluster="c"}`)
	assert.Len(t, bad, 1, "a bare `cluster` label is carried by no job")
	// Native labels, variables and negative matchers are not judged.
	bad = spec.unsatisfiable(d, `kube_pod_status_phase{phase="Running", job!="x", k8s_cluster_name=~"a|b"}`)
	assert.Empty(t, bad)
}

// The label file must say what the chart renders. The node jobs' claims
// (job, metrics_path, and which labels they carry) are derived from the
// golden renders' own relabel configs, so the two cannot drift: a chart
// that stops writing `metrics_path` fails here, and so does a claim in
// available-labels.yaml the chart does not back.
func TestNodeJobLabelsAreWhatTheChartRenders(t *testing.T) {
	spec := loadAvailableLabels(t)
	type step struct {
		Sources []string `yaml:"source_labels"`
		Target  string   `yaml:"target_label"`
	}
	type job struct {
		Name          string `yaml:"job_name"`
		MetricsPath   string `yaml:"metrics_path"`
		Relabel       []step `yaml:"relabel_configs"`
		MetricRelabel []step `yaml:"metric_relabel_configs"`
	}
	goldens, err := filepath.Glob("golden/observability-emitters/*.yaml")
	require.NoError(t, err)
	checked := 0
	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var agent vmagentDoc
			if yaml.Unmarshal(doc, &agent) != nil || agent.Kind != "VMAgent" || agent.Spec.InlineScrapeConfig == "" {
				continue
			}
			var jobs []job
			require.NoError(t, yaml.Unmarshal([]byte(agent.Spec.InlineScrapeConfig), &jobs))
			for _, j := range jobs {
				var claim *labelJob
				for i := range spec.Jobs {
					if spec.Jobs[i].Name == j.Name {
						claim = &spec.Jobs[i]
					}
				}
				require.NotNilf(t, claim, "%s: the chart renders a %q job that hack/dashboards/available-labels.yaml does not describe", g, j.Name)
				carried := map[string]bool{"job": true, "instance": true}
				path := j.MetricsPath
				if path == "" {
					path = "/metrics" // the scrape default
				}
				gotPath := ""
				for _, s := range append(append([]step{}, j.Relabel...), j.MetricRelabel...) {
					carried[s.Target] = true
					if s.Target == "metrics_path" && len(s.Sources) == 1 && s.Sources[0] == "__metrics_path__" {
						gotPath = path
					}
				}
				for _, l := range claim.Labels {
					assert.Truef(t, carried[l], "%s: job %q: available-labels.yaml says its series carry %q, the rendered relabel configs never write it", g, j.Name, l)
				}
				assert.Equalf(t, claim.Values["job"], []string{j.Name}, "%s: job %q: fixed job value", g, j.Name)
				assert.Equalf(t, claim.Values["metrics_path"], []string{gotPath},
					"%s: job %q: metrics_path is what __metrics_path__ holds at scrape time (%q)", g, j.Name, path)
				checked++
			}
		}
	}
	require.NotZero(t, checked)
}
