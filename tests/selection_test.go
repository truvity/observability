// Two alerters, two query languages, and one namespace of rules between
// them.
//
// vmalert parses every rule it selects at startup and EXITS on the first
// one it cannot parse. So a rule reaching the wrong alerter is not ignored
// and does not degrade anything gracefully: the alerter crash-loops, and
// every rule it was supposed to evaluate stops being evaluated with it.
//
// `selectAllByDefault: true` with no selector is how that happens, and the
// reason it survives review is that an install with no rules yet looks
// perfectly healthy. It only fires once somebody creates the first rule —
// which, for the metrics subchart, is the moment its own sync job runs.
// Measured on a live install: 23 PromQL rules, every one of them loaded by
// BOTH alerters, and the logs alerter in CrashLoopBackOff.
//
// This check is therefore about the pair, not about either alerter: the
// property worth holding is that no rule can ever be selected by two
// alerters at once.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// labelSelector is the part of a Kubernetes label selector these charts
// use.
type labelSelector struct {
	MatchLabels      map[string]string `yaml:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `yaml:"key"`
		Operator string   `yaml:"operator"`
		Values   []string `yaml:"values"`
	} `yaml:"matchExpressions"`
}

// selects reports whether an object carrying these labels is selected.
//
// The operators follow Kubernetes' own semantics, including the one this
// design leans on: NotIn and DoesNotExist match an object that does not
// carry the key at all. That was verified against a live API server
// before it was relied on, because getting it backwards would silently
// hand every unlabelled rule to the wrong alerter.
func (s labelSelector) selects(labels map[string]string) bool {
	for k, v := range s.MatchLabels {
		if labels[k] != v {
			return false
		}
	}

	for _, e := range s.MatchExpressions {
		value, present := labels[e.Key]

		switch e.Operator {
		case "In":
			if !present || !contains(e.Values, value) {
				return false
			}
		case "NotIn":
			if present && contains(e.Values, value) {
				return false
			}
		case "Exists":
			if !present {
				return false
			}
		case "DoesNotExist":
			if present {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}

	return false
}

// vmalertDoc is one rendered alerter.
type vmalertDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		RuleSelector          *labelSelector `yaml:"ruleSelector"`
		RuleNamespaceSelector *labelSelector `yaml:"ruleNamespaceSelector"`
		SelectAllByDefault    bool           `yaml:"selectAllByDefault"`
	} `yaml:"spec"`
}

// ruleShapes are the rules an install actually contains: the unlabelled
// PromQL ones other charts ship, and the two this repository's own label
// can carry.
var ruleShapes = []struct {
	what   string
	labels map[string]string
}{
	{"an unlabelled PromQL rule, as the metrics subchart ships 23 of", nil},
	{"a rule marked as PromQL", map[string]string{"observability.rule-type": "prometheus"}},
	{"a rule marked as LogsQL", map[string]string{"observability.rule-type": "vlogs"}},
}

// TestNoRuleReachesTwoAlerters is the check the crash-loop needed.
func TestNoRuleReachesTwoAlerters(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		alerters := map[string]vmalertDoc{}

		for _, doc := range splitDocs(t, g) {
			var a vmalertDoc
			if err := yaml.Unmarshal(doc, &a); err != nil || a.Kind != "VMAlert" {
				continue
			}

			alerters[a.Metadata.Name] = a
		}

		if len(alerters) == 0 {
			continue
		}

		t.Run(filepath.Base(g), func(t *testing.T) {
			for name, a := range alerters {
				seen++

				require.NotNilf(t, a.Spec.RuleSelector, "%s: %s sets no ruleSelector. With "+
					"`selectAllByDefault: true` that selects every rule in the cluster, including "+
					"the ones written in the other query language", g, name)
				assert.Falsef(t, a.Spec.SelectAllByDefault, "%s: %s still sets selectAllByDefault "+
					"alongside a selector; the two together are what nobody can reason about", g, name)
			}

			// Only a pair can overlap, and the overlap is the defect.
			if len(alerters) < 2 {
				return
			}

			for _, shape := range ruleShapes {
				var takers []string

				for name, a := range alerters {
					if a.Spec.RuleSelector != nil && a.Spec.RuleSelector.selects(shape.labels) {
						takers = append(takers, name)
					}
				}

				assert.LessOrEqualf(t, len(takers), 1, "%s: %s is selected by %v. vmalert EXITS on a "+
					"rule it cannot parse, so the alerter speaking the other language crash-loops and "+
					"takes every rule it owns down with it", g, shape.what, takers)
			}
		})
	}

	assert.Positive(t, seen, "no VMAlert found in any observability-stack golden")
}

// TestEveryRuleShapeIsEvaluatedSomewhere is the other half. A selector
// pair that overlaps nowhere is trivially satisfied by two selectors that
// match nothing at all — an install where no rule is ever evaluated and
// every pod is healthy.
func TestEveryRuleShapeIsEvaluatedSomewhere(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	for _, g := range goldens {
		var alerters []vmalertDoc

		for _, doc := range splitDocs(t, g) {
			var a vmalertDoc
			if err := yaml.Unmarshal(doc, &a); err != nil || a.Kind != "VMAlert" {
				continue
			}

			alerters = append(alerters, a)
		}

		if len(alerters) == 0 {
			continue
		}

		t.Run(filepath.Base(g), func(t *testing.T) {
			for _, shape := range ruleShapes {
				// A release with only the metrics alerter is a release
				// that has no business evaluating LogsQL.
				if shape.labels["observability.rule-type"] == "vlogs" && len(alerters) < 2 {
					continue
				}

				var taken bool

				for _, a := range alerters {
					if a.Spec.RuleSelector != nil && a.Spec.RuleSelector.selects(shape.labels) {
						taken = true
					}
				}

				assert.Truef(t, taken, "%s: %s is selected by NO alerter, so it would never be "+
					"evaluated while everything reports healthy", g, shape.what)
			}
		})
	}
}
