// Package dashboardlint holds the six-rule contract docs/dashboards.md
// defines for every dashboard this repository ships, and that an estate
// runs on its own with `just dashboard-lint`.
//
// The rule that matters most: a panel pinned to one datasource UID is
// how a fleet dashboard silently becomes a one-install dashboard. It
// renders, it looks finished, and it answers questions about exactly one
// install for as long as nobody notices the picker is missing. The other
// five rules exist so that once a dashboard clears rule 1, switching
// datasource actually shows a different cluster's data rather than an
// empty variable stuck on whatever upstream shipped.
package dashboardlint

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Finding is one rule violation, tied to the dashboard it was found in.
type Finding struct {
	Dashboard string
	Rule      int
	Message   string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: rule %d: %s", f.Dashboard, f.Rule, f.Message)
}

// variable is the subset of a Grafana template variable this package
// reads. Dashboards vary the shape of `query` across Grafana versions —
// sometimes a bare string, sometimes {query, refId} — so it is decoded
// into RawMessage and read by queryText.
type variable struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Datasource json.RawMessage `json:"datasource"`
	Query      json.RawMessage `json:"query"`
	Definition string          `json:"definition"`
}

func (v variable) queryText() string {
	var s string
	if err := json.Unmarshal(v.Query, &s); err == nil {
		return s
	}
	var obj struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(v.Query, &obj); err == nil {
		return obj.Query
	}
	return ""
}

func (v variable) datasourceRef() string {
	var s string
	if err := json.Unmarshal(v.Datasource, &s); err == nil {
		return s
	}
	var obj struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(v.Datasource, &obj); err == nil {
		return obj.UID
	}
	return ""
}

// datasourceRef is a panel or target's own `datasource` field, which
// Grafana renders either as a bare string or as {type, uid}.
type datasourceRef struct {
	raw json.RawMessage
}

func (d *datasourceRef) UnmarshalJSON(b []byte) error {
	d.raw = append([]byte(nil), b...)
	return nil
}

func (d *datasourceRef) uid() (string, bool) {
	if len(d.raw) == 0 || string(d.raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(d.raw, &s); err == nil {
		return s, true
	}
	var obj struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(d.raw, &obj); err == nil {
		return obj.UID, true
	}
	return "", false
}

type target struct {
	Expr       string         `json:"expr"`
	Datasource *datasourceRef `json:"datasource"`
}

type panel struct {
	Title      string         `json:"title"`
	Type       string         `json:"type"`
	Datasource *datasourceRef `json:"datasource"`
	Targets    []target       `json:"targets"`
	Panels     []panel        `json:"panels"`
}

// flatten returns every panel, including a row panel's own nested
// panels, in encounter order.
func (p panel) flatten() []panel {
	out := []panel{p}
	for _, sub := range p.Panels {
		out = append(out, sub.flatten()...)
	}
	return out
}

// dashboardRaw exists because Grafana panels can be either a plain list
// or, on very old exports, absent entirely.
type dashboardRaw struct {
	Title      string  `json:"title"`
	Panels     []panel `json:"panels"`
	Templating struct {
		List []variable `json:"list"`
	} `json:"templating"`
}

// datasourceVariableUID is what a panel or target's datasource must
// equal to count as "uses the datasource variable": `$datasource`,
// `${datasource}`, or Grafana's own "-- Mixed --" sentinel, which means
// "look at each target" rather than naming a store.
const mixedSentinel = "-- Mixed --"

var (
	metricSelector  = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*\{`)
	bareSelector    = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_:}])\{`)
	rangeSelector   = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*\[`)
	clusterRef      = regexp.MustCompile(`\$\{?cluster\}?\b`)
	namespaceLabel  = regexp.MustCompile(`\b(namespace|k8s_namespace_name)\s*[=~]`)
	labelValuesCall = regexp.MustCompile(`label_values\(`)
	envSelectorName = regexp.MustCompile(`(?i)^(env|environment|tier|deployment_environment_name)$`)
)

func namesAMetric(expr string) bool {
	return metricSelector.MatchString(expr) || bareSelector.MatchString(expr) || rangeSelector.MatchString(expr)
}

func isDatasourceVarRef(uid string) bool {
	return uid == "$datasource" || uid == "${datasource}" || uid == mixedSentinel
}

// Lint runs the six-rule contract against one dashboard's raw JSON.
// `name` is used only to label findings.
func Lint(name string, raw []byte) ([]Finding, error) {
	var d dashboardRaw
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%s: invalid dashboard JSON: %w", name, err)
	}

	var findings []Finding
	add := func(rule int, format string, args ...any) {
		findings = append(findings, Finding{Dashboard: name, Rule: rule, Message: fmt.Sprintf(format, args...)})
	}

	byName := map[string]variable{}
	for _, v := range d.Templating.List {
		byName[v.Name] = v
	}

	// Rule 1: a `datasource` variable, and every panel uses it.
	dsVar, hasDS := byName["datasource"]
	if !hasDS || dsVar.Type != "datasource" {
		add(1, "no template variable named `datasource` of type `datasource` — every panel needs one to point at, or it is pinned to whatever Grafana calls default")
	}

	var allPanels []panel
	for _, p := range d.Panels {
		allPanels = append(allPanels, p.flatten()...)
	}

	for _, p := range allPanels {
		if p.Type == "row" {
			continue
		}
		hasQuery := false
		for _, t := range p.Targets {
			if strings.TrimSpace(t.Expr) != "" {
				hasQuery = true
			}
		}
		if !hasQuery {
			continue
		}
		panelUID, panelSet := "", false
		if p.Datasource != nil {
			if u, ok := p.Datasource.uid(); ok {
				panelUID, panelSet = u, true
			}
		}
		if panelSet && !isDatasourceVarRef(panelUID) {
			add(1, "panel %q has a literal datasource %q instead of the `datasource` variable", p.Title, panelUID)
			continue
		}
		if panelSet && panelUID != mixedSentinel {
			continue // panel-level reference covers every target.
		}
		for _, t := range p.Targets {
			if strings.TrimSpace(t.Expr) == "" {
				continue
			}
			if t.Datasource == nil {
				if !panelSet {
					add(1, "panel %q, query %q has no datasource at all (panel or target) — it falls back to whatever Grafana calls default, which is a literal by another name", p.Title, t.Expr)
				}
				continue
			}
			uid, ok := t.Datasource.uid()
			if !ok {
				continue
			}
			if !isDatasourceVarRef(uid) {
				add(1, "panel %q, query %q has a literal datasource %q instead of the `datasource` variable", p.Title, t.Expr, uid)
			}
		}
	}

	// Rule 2: a `cluster` variable, chained off `datasource`, populated
	// by a label-values query, used in every query.
	clusterVar, hasCluster := byName["cluster"]
	if !hasCluster {
		add(2, "no template variable named `cluster` — a dashboard with no cluster variable cannot say which install's data it shows")
	} else {
		if clusterVar.Type != "query" {
			add(2, "the `cluster` variable is type %q, not `query` — it must be populated by a label-values query, never a fixed list", clusterVar.Type)
		}
		if ref := clusterVar.datasourceRef(); !isDatasourceVarRef(ref) {
			add(2, "the `cluster` variable's datasource is %q, not the `datasource` variable — it is not chained, so it lists whatever Grafana's default datasource holds instead of the chosen install's clusters", ref)
		}
		q := clusterVar.queryText()
		if q == "" {
			q = clusterVar.Definition
		}
		if !labelValuesCall.MatchString(q) {
			add(2, "the `cluster` variable's query %q is not a label_values() call — the list must come from what the chosen install actually holds, never a hardcoded set", q)
		}
	}
	if hasCluster {
		for _, p := range allPanels {
			for _, t := range p.Targets {
				if t.Expr == "" || !namesAMetric(t.Expr) {
					continue
				}
				if !clusterRef.MatchString(t.Expr) {
					add(2, "panel %q, query %q does not reference $cluster — it returns every cluster's data at once regardless of the picker", p.Title, t.Expr)
				}
			}
		}
	}

	// Rule 3: a `namespace` variable, chained off `cluster`, where the
	// dashboard is namespace-scoped. Namespace-scoped is detected the
	// same way a reviewer would: some query names the namespace label.
	namespaceScoped := false
	for _, p := range allPanels {
		for _, t := range p.Targets {
			if namespaceLabel.MatchString(t.Expr) {
				namespaceScoped = true
			}
		}
	}
	for _, v := range d.Templating.List {
		if namespaceLabel.MatchString(v.queryText()) || namespaceLabel.MatchString(v.Definition) {
			namespaceScoped = true
		}
	}
	if namespaceScoped {
		nsVar, hasNS := byName["namespace"]
		if !hasNS {
			add(3, "this dashboard filters by namespace but has no `namespace` variable chained off `cluster`")
		} else {
			q := nsVar.queryText()
			if q == "" {
				q = nsVar.Definition
			}
			if !clusterRef.MatchString(q) {
				add(3, "the `namespace` variable's query %q does not reference $cluster — it is not chained off `cluster`, so it lists every namespace on every install rather than the chosen cluster's own", q)
			}
			if !labelValuesCall.MatchString(q) {
				add(3, "the `namespace` variable's query %q is not a label_values() call", q)
			}
		}
	}

	// Rule 4: $cluster in the title.
	if !clusterRef.MatchString(d.Title) {
		add(4, "the dashboard title %q does not contain $cluster — a screenshot of it does not say which cluster it is", d.Title)
	}

	// Rule 5: the environment tier is a display label, never a selector.
	for _, v := range d.Templating.List {
		if envSelectorName.MatchString(v.Name) && (v.Type == "query" || v.Type == "custom") {
			add(5, "template variable %q (type %q) selects by environment tier — two clusters can share a tier, so filtering on it can hand a viewer both", v.Name, v.Type)
		}
	}

	// Rule 6 is not a failure mode of its own: it is what rules 2 and 3
	// must NOT enforce. namespaceLabel already accepts both `namespace`
	// and `k8s_namespace_name` above, which is the whole of rule 6 — a
	// dashboard using either spelling passes rules 2 and 3 the same way.

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Message < findings[j].Message
	})
	return findings, nil
}
