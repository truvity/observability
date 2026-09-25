// One endpoint an estate may admit, three it may not, and the default.
//
// `/api/v1/metadata` returns every metric NAME in the store with its type
// and help string, and no filter reaches it — measured against the store,
// an `extra_filters` naming a namespace that matches nothing returns the
// same body as no filter at all. So the route is the same list for every
// principal whatever their grant says, which on an install with
// per-namespace grants is an inventory of what another tenant runs.
//
// It is off by default, and that default is VISIBLE rather than silent:
// Grafana's Prometheus-family datasources ask for this endpoint to put
// descriptions on metric names and write a 401 into their own logs when
// the proxy does not route it. An operator who reads that 401 should find
// a value, not a mystery — which is why this is a switch and not a rule.
//
// Three siblings are admitted by nothing, because what they leak is
// per-principal rather than bounded: two return other principals' query
// TEXT and one returns names with per-tenant counts. A test that only
// checked the switch would not notice a wildcard that let those in, and a
// wildcard here is exactly how all four arrived together once before.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const metricMetadataPath = "/prometheus/api/v1/metadata"

// neverRouted are the endpoints no value in this chart admits.
var neverRouted = []string{
	"/prometheus/api/v1/status/active_queries",
	"/prometheus/api/v1/status/top_queries",
	"/prometheus/api/v1/status/metric_names_stats",
}

// vmuserRoles is the golden's own record of which VMUsers opted in, so
// this check does not have to re-read the values files to know.
type vmuserRoles struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		TargetRefs []struct {
			Paths []string `yaml:"paths"`
		} `yaml:"targetRefs"`
	} `yaml:"spec"`
}

// TestMetricMetadataIsOptInAndSiblingsNever holds both halves at once.
func TestMetricMetadataIsOptInAndSiblingsNever(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	// The case that opts in, by the name of its golden. Every other
	// golden must not carry the path at all.
	optedIn := map[string]bool{"everything.yaml": true}

	var seenReaders, seenOptedIn int

	for _, g := range goldens {
		base := filepath.Base(g)

		for _, doc := range splitDocs(t, g) {
			var u vmuserRoles
			if err := yaml.Unmarshal(doc, &u); err != nil || u.Kind != "VMUser" {
				continue
			}

			role := u.Metadata.Labels["observability.role"]

			var paths []string
			for _, ref := range u.Spec.TargetRefs {
				paths = append(paths, ref.Paths...)
			}

			joined := strings.Join(paths, " ")

			for _, never := range neverRouted {
				assert.NotContainsf(t, joined, never,
					"%s: %s (%s) routes %q. It is admitted by no value in this chart: two of these return "+
						"other principals' query text and one returns metric names with per-tenant counts. "+
						"A wildcard on the metrics route is how all of them arrive at once.",
					g, u.Metadata.Name, role, never)
			}

			hasMetadata := strings.Contains(joined, metricMetadataPath)

			switch role {
			case "reader":
				seenReaders++

				if optedIn[base] {
					seenOptedIn++

					assert.Truef(t, hasMetadata,
						"%s: reader %s does not route %q although the case sets "+
							"`tenancy.allowUnfilteredMetricMetadata`. The switch renders nothing, "+
							"and the 401 it was set to remove stays.",
						g, u.Metadata.Name, metricMetadataPath)
				} else {
					assert.Falsef(t, hasMetadata,
						"%s: reader %s routes %q with the value unset. The endpoint takes no filter, "+
							"so every principal would read the same metric inventory whatever their grant says.",
						g, u.Metadata.Name, metricMetadataPath)
				}
			case "writer":
				// A writer's token lives in a DaemonSet on every node. It
				// gets write paths and nothing else, whatever any read
				// switch says.
				assert.Falsef(t, hasMetadata,
					"%s: writer %s routes %q. A collector's token is the one most likely to leak, "+
						"and a read switch must not widen it.",
					g, u.Metadata.Name, metricMetadataPath)
			}
		}
	}

	assert.Positive(t, seenReaders, "no reader VMUser in any golden; this check went blind rather than passing")
	assert.Positive(t, seenOptedIn, "no golden exercises `tenancy.allowUnfilteredMetricMetadata`; "+
		"the opted-in half of this check never ran")
}
