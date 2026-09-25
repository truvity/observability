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
	"gopkg.in/yaml.v3"
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
			if !strings.HasSuffix(rule.Metadata.Name, "-self") {
				continue
			}

			// Every expression here is MetricsQL, so the label that keeps
			// it away from the LogsQL alerter is load-bearing: without it
			// the logs alerter parses these and EXITS, taking every log
			// rule down with it.
			assert.Equalf(t, "prometheus", rule.Metadata.Labels["observability.rule-type"],
				"%s: the self-alert rules are MetricsQL and must be marked as such, or the logs "+
					"alerter loads them and crash-loops", g)

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
