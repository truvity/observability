// The rules the stack points at itself, and the contract each one owes.
//
// A rule is cheap to write and expensive to trust. Every one in this
// repository is supposed to carry the failure it watches, a severity the
// routing tree can act on, and a `for` that keeps a blip from paging
// somebody — and none of that is visible in a render unless something
// checks. Three of these rules exist because a real install discarded
// data for days while every object reported Healthy; a fourth of them
// silently not firing would be the same failure again, one level up.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// vmRuleDoc is one rendered VMRule.
type vmRuleDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Groups []struct {
			Name     string `yaml:"name"`
			Interval string `yaml:"interval"`
			Rules    []struct {
				Alert       string            `yaml:"alert"`
				Record      string            `yaml:"record"`
				Expr        string            `yaml:"expr"`
				For         string            `yaml:"for"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	} `yaml:"spec"`
}

// TestEverySelfAlertKeepsItsContract walks the rendered rules rather than
// the template, because what an alerter loads is the render.
func TestEverySelfAlertKeepsItsContract(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var rule vmRuleDoc
			if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
				continue
			}
			if !strings.HasSuffix(rule.Metadata.Name, "-selfalerts") {
				continue
			}

			for _, group := range rule.Spec.Groups {
				assert.NotEmptyf(t, group.Interval, "%s: group %q has no interval", g, group.Name)

				for _, r := range group.Rules {
					if r.Alert == "" {
						continue // a recording rule owes none of this
					}
					seen++

					where := g + " / " + r.Alert

					assert.NotEmptyf(t, strings.TrimSpace(r.Expr), "%s: no expression", where)
					// A severity the routing tree cannot act on routes to
					// the default tier by accident.
					assert.Containsf(t, []string{"critical", "warning"}, r.Labels["severity"],
						"%s: severity is %q; the routing tree carries critical and warning",
						where, r.Labels["severity"])
					// Without `for`, a single scrape blip pages somebody.
					assert.NotEmptyf(t, r.For, "%s: no `for`, so one bad scrape pages somebody", where)
					assert.NotEmptyf(t, r.Annotations["summary"], "%s: no summary", where)

					// The description is where the incident lives. A rule
					// whose description says only what the expression
					// already says is a rule nobody can act on at three in
					// the morning.
					description := r.Annotations["description"]
					assert.NotEmptyf(t, description, "%s: no description", where)
					assert.Greaterf(t, len(description), 80,
						"%s: the description is %d characters; it is meant to carry what went wrong "+
							"and what to look at, not restate the expression", where, len(description))

					// A dynamic value in `labels` breaks vmalert's state
					// tracking: each evaluation looks like a different
					// alert, so `for` never elapses and it never fires.
					for key, value := range r.Labels {
						assert.NotContainsf(t, value, "{{",
							"%s: label %q carries a template. vmalert tracks an alert by its labels, "+
								"so a value that changes per evaluation restarts `for` every time and "+
								"the rule never fires", where, key)
					}
				}
			}
		}
	}

	assert.GreaterOrEqualf(t, seen, 10,
		"only %d self-alerts found across the goldens; this check has gone blind rather than "+
			"the rules having been removed", seen)
}

// TestSelfAlertAggregationsKeepTheClusterLabel: a store holds several
// clusters' series, and an aggregation naming the labels to KEEP
// (`sum by (url)`, or a bare `sum()`) drops the cluster label, so a remote
// cluster's alert arrived with no cluster. Only `without (...)` keeps it.
func TestSelfAlertAggregationsKeepTheClusterLabel(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var rule vmRuleDoc
			if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
				continue
			}
			if !strings.HasSuffix(rule.Metadata.Name, "-selfalerts") {
				continue
			}

			for _, group := range rule.Spec.Groups {
				for _, r := range group.Rules {
					if r.Alert == "" || !strings.Contains(r.Expr, "sum") {
						continue
					}
					seen++

					expr := strings.Join(strings.Fields(r.Expr), " ")
					assert.Containsf(t, expr, "sum without (", "%s / %s: aggregates without `without (...)`: %s", g, r.Alert, expr)
					assert.NotRegexpf(t, `\bby \(`, expr, "%s / %s: `by (...)` drops the cluster label: %s", g, r.Alert, expr)
				}
			}
		}
	}

	assert.Positive(t, seen, "no summing self-alert found; this check has gone blind")
}

// TestSelfAlertSourceAbsentIsPerCluster: the opt-in guard on the self-alert
// source series must be the per-cluster shape (a bare `absent()` stays false
// while any other cluster still reports), and renders nowhere by default.
func TestSelfAlertSourceAbsentIsPerCluster(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var rule vmRuleDoc
			if err := yaml.Unmarshal(doc, &rule); err != nil || rule.Kind != "VMRule" {
				continue
			}

			for _, group := range rule.Spec.Groups {
				for _, r := range group.Rules {
					if r.Alert != "SelfAlertSourceAbsent" {
						continue
					}
					seen++

					assert.Equal(t, "selfalerts-source-absent.yaml", filepath.Base(g), "the guard is opt-in and must not render by default")
					assert.Equal(t, "warning", r.Labels["severity"])
					expr := strings.Join(strings.Fields(r.Expr), " ")
					assert.Contains(t, expr, `k8s_cluster_name!=""`, "%s: the per-cluster branch needs the cluster label on both sides", g)
					assert.Contains(t, expr, "group by (k8s_cluster_name) (max_over_time(", "%s: no per-cluster branch", g)
					assert.Contains(t, expr, "or (absent(", "%s: no whole-store absent() branch", g)
					assert.Contains(t, expr, `label_replace(`, "%s: the alert does not say which source went", g)
				}
			}
		}
	}

	assert.Equal(t, 1, seen, "SelfAlertSourceAbsent should render in exactly the one golden that opts in")
}
