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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
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

			// A twin must select exactly what its original does: identical
			// rules on both replicas of a pair, or the pair is two opinions.
			for name, a := range alerters {
				if twin, ok := alerters[strings.TrimSuffix(name, "-peer")]; ok && name != twin.Metadata.Name {
					assert.Equalf(t, twin.Spec.RuleSelector, a.Spec.RuleSelector,
						"%s: %s selects different rules from %s", g, name, twin.Metadata.Name)
				}
			}

			// Only a pair can overlap, and the overlap is the defect.
			if len(alerters) < 2 {
				return
			}

			for _, shape := range ruleShapes {
				// An HA pair renders a "-peer" twin of each alerter, reading the
				// other replica's store with the same rules on purpose. The twin
				// counts as its original: the defect here is two DIFFERENT
				// query languages selecting one rule.
				takerSet := map[string]bool{}

				for name, a := range alerters {
					if a.Spec.RuleSelector != nil && a.Spec.RuleSelector.selects(shape.labels) {
						takerSet[strings.TrimSuffix(name, "-peer")] = true
					}
				}

				var takers []string
				for name := range takerSet {
					takers = append(takers, name)
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

// TestRemoteEvaluatorRulesHaveExactlyOneOwner: a rule carrying
// `observability.truvity.io/evaluator: <name>` is evaluated by that
// evaluator's alerter and by no other, in particular not by the main
// metrics alerter, which would evaluate it against the local store and see
// its metric absent forever. And a rule without the label is still never
// taken by an evaluator.
func TestRemoteEvaluatorRulesHaveExactlyOneOwner(t *testing.T) {
	const key = "observability.truvity.io/evaluator"

	alerters := map[string]vmalertDoc{}

	for _, doc := range splitDocs(t, "golden/observability-stack/remote-evaluators.yaml") {
		var a vmalertDoc
		if err := yaml.Unmarshal(doc, &a); err != nil || a.Kind != "VMAlert" {
			continue
		}

		alerters[a.Metadata.Name] = a
	}

	require.Len(t, alerters, 4, "two main alerters and two remote evaluators")

	owners := func(labels map[string]string) []string {
		var out []string

		for name, a := range alerters {
			if a.Spec.RuleSelector != nil && a.Spec.RuleSelector.selects(labels) {
				out = append(out, name)
			}
		}

		return out
	}

	for _, name := range []string{"other-store", "third-store"} {
		for _, extra := range []map[string]string{nil, {"observability.rule-type": "prometheus"}, {"observability.rule-type": "vlogs"}} {
			labels := map[string]string{key: name}
			for k, v := range extra {
				labels[k] = v
			}

			got := owners(labels)

			if extra["observability.rule-type"] == "vlogs" {
				assert.Empty(t, got, "a LogsQL rule with an evaluator label is evaluated by nobody, never by the wrong alerter")

				continue
			}

			require.Len(t, got, 1, "%v is owned by %v", labels, got)
			assert.True(t, strings.HasSuffix(got[0], "-remote-"+name), "%v owned by %s", labels, got[0])
		}
	}

	for _, labels := range []map[string]string{nil, {"observability.rule-type": "prometheus"}, {"observability.rule-type": "vlogs"}} {
		for _, o := range owners(labels) {
			assert.NotContains(t, o, "-remote-", "an unlabelled rule must never reach a remote evaluator")
		}
	}
}
