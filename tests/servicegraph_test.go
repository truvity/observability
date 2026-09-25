// The Jaeger dependency graph and the failure mode that made it worth a
// test.
//
// `api/dependencies` answers `200` with `{"data":[],"total":0}` whether
// nothing has ever produced a service graph or the background task that
// computes one is simply off — measured against the running store, see
// docs/safety.md. Upstream ships that task, `-servicegraph.enableTask`,
// disabled, and this chart writes the same default out explicitly rather
// than leaving it to upstream: an explicit `false` in the render is a
// value a diff can catch turning into `true`, where upstream's own
// default is not.
//
// So the render is the only place that distinguishes "we chose this" from
// "nobody set it", and this test is what keeps that render honest: the
// flag must read `false` in every golden except the one case that opts
// in, and that one case must actually flip it.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// traceStoreStatefulSet is the minimum needed to reach the trace store's
// own container args out of a rendered StatefulSet.
type traceStoreStatefulSet struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string   `yaml:"name"`
					Args []string `yaml:"args"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// TestServiceGraphTaskOffByDefault holds both halves at once: every
// golden except the opted-in one keeps the explicit `false`, and the
// opted-in one actually renders the flag turned on.
func TestServiceGraphTaskOffByDefault(t *testing.T) {
	t.Parallel()

	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	// The case that opts in, by the name of its golden. `everything`
	// cannot be it: that case turns the store subcharts off on purpose
	// (their own render is `single`'s job — see the comment there), so
	// the trace store's StatefulSet, and the flag on it, would not
	// render at all.
	optedIn := map[string]bool{"single.yaml": true}

	var seenStores, seenOptedIn int

	for _, g := range goldens {
		base := filepath.Base(g)

		for _, doc := range splitDocs(t, g) {
			var sts traceStoreStatefulSet
			if err := yaml.Unmarshal(doc, &sts); err != nil || sts.Kind != "StatefulSet" {
				continue
			}

			var args []string
			var found bool
			for _, c := range sts.Spec.Template.Spec.Containers {
				if c.Name == "vtraces" {
					args = c.Args
					found = true
				}
			}
			if !found {
				continue
			}

			seenStores++
			joined := strings.Join(args, " ")

			if optedIn[base] {
				seenOptedIn++

				// The shared `vm.arg` helper renders a boolean `true`
				// as a bare flag, not `--key=true` — the same
				// convention Go's own flag package uses. Assert the
				// bare form, and that the default's explicit `=false`
				// is gone, rather than assuming a `=true` that never
				// renders.
				assert.Containsf(t, args, "--servicegraph.enableTask",
					"%s: StatefulSet %s does not render `--servicegraph.enableTask` although "+
						"the case sets `servicegraph.enableTask: \"true\"`. The switch renders nothing, "+
						"and the dependency graph stays uncomputed.",
					g, sts.Metadata.Name)
				assert.NotContainsf(t, joined, "--servicegraph.enableTask=false",
					"%s: StatefulSet %s still renders the task explicitly OFF although the case opts in.",
					g, sts.Metadata.Name)
			} else {
				assert.Containsf(t, joined, "--servicegraph.enableTask=false",
					"%s: StatefulSet %s does not render `--servicegraph.enableTask=false`. "+
						"Left unset, upstream's own default is silently the same value today, but a diff "+
						"would not catch it changing: this store's dependency-graph endpoint already "+
						"answers 200 with an empty list whether the task is off or has simply not run "+
						"yet, and an implicit default hides which of those is true.",
					g, sts.Metadata.Name)
			}
		}
	}

	assert.Positive(t, seenStores, "no trace store StatefulSet found in any golden; this check went blind rather than passing")
	assert.Positive(t, seenOptedIn, "no golden exercises `servicegraph.enableTask: \"true\"`; "+
		"the opted-in half of this check never ran")
}
