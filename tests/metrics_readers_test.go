// tenancy.readers: static-bearer, read-only METRICS QUERY readers.
//
// tests/cases/observability-stack/tenancy-readers renders two readers beside
// a principal and a writer. This is the Go side of proving the render did
// what the values file asked: a reader's VMUser carries the two query
// routes and nothing else, and its grant is written onto the route as
// literal `extra_filters`, since a static token has no claim for vmauth to
// substitute from.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const metricsReadersGolden = "golden/observability-stack/tenancy-readers.yaml"

func TestMetricsReaderHasOnlyTheQueryRoutesAndItsGrant(t *testing.T) {
	users := readVMUsers(t, metricsReadersGolden)

	byName := map[string]vmUser{}
	for _, u := range users {
		byName[u.Spec.Name] = u
	}

	want := map[string][]string{
		"edge-evaluator": {`{k8s_cluster_name="example-edge",k8s_namespace_name=~"^(example-app|example-jobs)$"}`},
		"wide-evaluator": {`{k8s_cluster_name="example-edge"}`, `{k8s_cluster_name="example-other"}`},
	}
	for name, filters := range want {
		u, ok := byName[name]
		require.True(t, ok, "reader %q missing from the golden render", name)

		// Static token: no OIDC identity, no claim to substitute from.
		assert.Empty(t, u.Spec.JWT.MatchClaims, "%s: a reader is a bearer token, not a JWT identity", name)

		require.Len(t, u.Spec.TargetRefs, 1, "%s: one route group, the metrics store, and no logs, traces, write or vmalert route", name)
		ref := u.Spec.TargetRefs[0]
		assert.Equal(t, []string{"/prometheus/api/v1/query", "/prometheus/api/v1/query_range"}, ref.Paths,
			"%s: exactly the two query paths a vmalert datasource asks for", name)
		for _, p := range ref.Paths {
			assert.NotContains(t, p, "write")
			assert.NotContains(t, p, "export")
			assert.NotContains(t, p, "delete")
			assert.NotContains(t, p, "admin")
			assert.NotContains(t, p, "/select/")
		}

		require.Len(t, ref.QueryArgs, 1, "%s: the grant must be on the wire, as a query argument", name)
		assert.Equal(t, "extra_filters", ref.QueryArgs[0].Name)
		assert.Equal(t, filters, ref.QueryArgs[0].Values,
			"%s: the reader's grant, one entry per cluster (the store ORs them), literal and not a claim placeholder", name)
	}
}
