// notifications.groupByRoutes and notifications.inhibit.rules
// (docs/notifications.md, "Grouping some alerts differently").
//
// A grouping change that also moved where an alert is delivered would be a
// routing change nobody asked for, and nothing fails: the alert still
// arrives, in the wrong channel or without its mention. So the property
// held here is the one the feature promises: for every alert, the
// receivers it reaches with the child routes are exactly the ones it
// reaches without them, and only the grouping of the alerts an entry
// matches differs.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matches reports whether an alert's labels satisfy a route's `match` and
// the `label = "value"` form of `matchers` this chart renders.
func (r amRoute) matches(labels map[string]string) bool {
	for k, v := range r.Match {
		if labels[k] != v {
			return false
		}
	}
	for _, m := range r.Matchers {
		k, v, ok := strings.Cut(m, " = ")
		if !ok || labels[k] != strings.Trim(v, `"`) {
			return false
		}
	}
	return true
}

type delivery struct {
	Receiver string
	GroupBy  []string
}

// deliveries walks the routing tree the way Alertmanager does: the first
// matching child wins unless it sets `continue`, a node with no matching
// child delivers to its own receiver, and `group_by` is inherited.
func deliveries(node amRoute, inherited []string, labels map[string]string) []delivery {
	groupBy := inherited
	if node.GroupBy != nil {
		groupBy = node.GroupBy
	}
	var out []delivery
	for _, child := range node.Routes {
		if !child.matches(labels) {
			continue
		}
		out = append(out, deliveries(child, groupBy, labels)...)
		if !child.Continue {
			break
		}
	}
	if len(out) == 0 {
		return []delivery{{node.Receiver, groupBy}}
	}
	return out
}

// withoutGroupRoutes is the config with every child route that sets its
// own `group_by` removed from the primary tree: what the same values
// render without `groupByRoutes`.
func withoutGroupRoutes(cfg amConfig) amConfig {
	out := cfg
	out.Route.Routes = nil
	for _, r := range cfg.Route.Routes {
		if r.Continue && len(r.Match) == 0 && len(r.Matchers) == 0 {
			var kept []amRoute
			for _, c := range r.Routes {
				if c.GroupBy == nil {
					kept = append(kept, c)
				}
			}
			r.Routes = kept
		}
		out.Route.Routes = append(out.Route.Routes, r)
	}
	return out
}

func TestGroupByRoutesChangeGroupingAndNothingElse(t *testing.T) {
	root := []string{"alertname", "k8s_cluster_name", "k8s_namespace_name"}
	batch := []string{"alertname", "k8s_cluster_name", "k8s_namespace_name", "kind", "target"}

	for _, chart := range []string{"observability-stack", "observability-alerting"} {
		cfg := alertmanagerConfig(t, "golden/"+chart+"/notifications-group-routes.yaml")
		plain := withoutGroupRoutes(cfg)
		_, tree := primaryTree(t, cfg)
		_, plainTree := primaryTree(t, plain)
		require.NotEqual(t, len(tree.Routes), len(plainTree.Routes),
			"%s: the golden renders no group_by child route, so this proves nothing", chart)

		var checked int
		for _, group := range []string{"batch", "ledger", "other", ""} {
			for _, cluster := range []string{"example-cluster", "elsewhere", ""} {
				for _, severity := range []string{"critical", "warning", "info", ""} {
					labels := map[string]string{
						"alertname": "Example", "alertgroup": group,
						"k8s_cluster_name": cluster, "k8s_namespace_name": "example-app", "severity": severity,
					}
					with := deliveries(cfg.Route, cfg.Route.GroupBy, labels)
					without := deliveries(plain.Route, plain.Route.GroupBy, labels)
					require.Len(t, with, len(without), "%s %v", chart, labels)
					for i := range with {
						assert.Equal(t, without[i].Receiver, with[i].Receiver,
							"%s %v: the child route moved where the alert is delivered", chart, labels)
					}
					wantGroupBy := root
					switch group {
					case "batch":
						wantGroupBy = batch
					case "ledger":
						wantGroupBy = []string{"..."}
					}
					for _, d := range with {
						assert.Equal(t, wantGroupBy, d.GroupBy, "%s %v", chart, labels)
					}
					for _, d := range without {
						assert.Equal(t, root, d.GroupBy, "%s %v: an alert no entry matches must keep the root grouping", chart, labels)
					}
					checked++
				}
			}
		}
		require.Equal(t, 48, checked)
	}
}

// TestGroupByRoutesDefaultRendersNoChildRoute: with the key unset no route
// of any golden sets its own group_by, so the default render is the one it
// always was.
func TestGroupByRoutesDefaultRendersNoChildRoute(t *testing.T) {
	var checked int
	for _, chart := range []string{"observability-stack", "observability-alerting"} {
		goldens, err := filepath.Glob("golden/" + chart + "/*.yaml")
		require.NoError(t, err)
		for _, g := range goldens {
			if strings.HasSuffix(g, "/notifications-group-routes.yaml") {
				continue
			}
			var hasAM bool
			for _, d := range renderedDocs(t, g) {
				if d["kind"] == "VMAlertmanager" {
					hasAM = true
				}
			}
			if !hasAM {
				continue
			}
			cfg := alertmanagerConfig(t, g)
			var walk func(r amRoute)
			walk = func(r amRoute) {
				for _, c := range r.Routes {
					assert.Nil(t, c.GroupBy, "%s: a route sets its own group_by with groupByRoutes unset", g)
					walk(c)
				}
			}
			walk(cfg.Route)
			checked++
		}
	}
	require.NotZero(t, checked)
}

// TestExtraInhibitRulesAreRenderedAfterTheBuiltInOne: the extra rule
// follows the built-in critical-over-warning rule, which is unchanged,
// and carries its matchers verbatim.
func TestExtraInhibitRulesAreRenderedAfterTheBuiltInOne(t *testing.T) {
	for _, chart := range []string{"observability-stack", "observability-alerting"} {
		cfg := alertmanagerConfig(t, "golden/"+chart+"/notifications-group-routes.yaml")
		require.Len(t, cfg.InhibitRules, 2, chart)
		assert.Equal(t, []string{`severity = "critical"`, `k8s_cluster_name =~ ".+"`, `k8s_namespace_name =~ ".+"`},
			cfg.InhibitRules[0].SourceMatchers, chart)
		extra := cfg.InhibitRules[1]
		assert.Equal(t, []string{
			`alertname =~ "BatchApiErrors|BatchStoreErrors"`, `k8s_cluster_name =~ ".+"`, `k8s_namespace_name =~ ".+"`,
		}, extra.SourceMatchers, chart)
		assert.Equal(t, []string{`alertname =~ "BatchRunStale"`}, extra.TargetMatchers, chart)
		assert.Equal(t, []string{"k8s_cluster_name", "k8s_namespace_name"}, extra.Equal, chart)
	}
}
