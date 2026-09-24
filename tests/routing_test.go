// One proxy, three stores, and one ordered list of routes between them.
//
// vmauth matches a VMUser's `src_paths` in the order its `targetRefs`
// render and stops at the first hit. So a path on an earlier store that
// also matches a later store's path does not merely overlap: it TAKES
// that traffic, and the later store becomes unreachable through the
// proxy while every object involved stays perfectly healthy.
//
// This install shipped with `/insert/.*` on the log store, declared
// before the trace store's `/insert/opentelemetry/v1/traces`. Every span
// any writer sent was posted to the log store, which answered 400; the
// collector recorded a permanent rejection and dropped the batch. The
// trace store held nothing for as long as it took somebody to send a
// span and then go and ASK the store whether it had arrived — because a
// 200 from a sender is not evidence, and an empty trace store looks
// exactly like an estate that emits no spans.
//
// The chart refuses the shape at render time. This holds the same
// property one step further out, against the routes as they are actually
// ORDERED in a rendered VMUser — which is the thing vmauth reads, and
// which the render-time check has to assume.
package tests

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// vmuserDoc is one rendered VMUser, with its routes in declaration order.
type vmuserDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		TargetRefs []struct {
			Static struct {
				URL string `yaml:"url"`
			} `yaml:"static"`
			Paths []string `yaml:"paths"`
		} `yaml:"targetRefs"`
	} `yaml:"spec"`
}

// probe is a concrete stand-in for whatever a path's own wildcards would
// accept. Two regular expressions cannot be compared directly, so a later
// route is tested as the text an earlier route might match.
func probe(path string) string {
	for _, wildcard := range []string{".*", ".+", "[^/]+"} {
		path = strings.ReplaceAll(path, wildcard, "x")
	}

	return path
}

// TestNoRouteSwallowsAnother is the check the empty trace store needed.
func TestNoRouteSwallowsAnother(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var u vmuserDoc
			if err := yaml.Unmarshal(doc, &u); err != nil || u.Kind != "VMUser" {
				continue
			}

			if len(u.Spec.TargetRefs) < 2 {
				continue
			}

			seen++

			t.Run(filepath.Base(g)+"/"+u.Metadata.Name, func(t *testing.T) {
				// Flattened in declaration order, because that is the
				// order vmauth reads them in.
				type route struct {
					path string
					url  string
				}

				var routes []route

				for _, ref := range u.Spec.TargetRefs {
					for _, p := range ref.Paths {
						routes = append(routes, route{path: p, url: ref.Static.URL})
					}
				}

				for i, earlier := range routes {
					pattern, err := regexp.Compile("^" + earlier.path + "$")
					require.NoErrorf(t, err, "%s: route %q is not a valid regular expression", g, earlier.path)

					for _, later := range routes[i+1:] {
						if earlier.url == later.url {
							// One store's own routes may overlap: it
							// answers either way.
							continue
						}

						assert.Falsef(t, pattern.MatchString(probe(later.path)),
							"%s: %s declares %q (to %s) BEFORE %q (to %s), and the first matches the "+
								"second. vmauth stops at the first match, so every request meant for the "+
								"second store is answered by the first — and the store that never hears "+
								"them stays empty, which is indistinguishable from having nothing to say.",
							g, u.Metadata.Name, earlier.path, earlier.url, later.path, later.url)
					}
				}
			})
		}
	}

	assert.Positive(t, seen, "no multi-store VMUser found in any observability-stack golden; "+
		"this check went blind rather than passing")
}
