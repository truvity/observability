// Per-principal audience and route restriction.
//
// tenancy.principals[] gained two optional fields: a principal's own
// `audience`, overriding `tenancy.audience` for its `matchClaims.aud`
// alone, and `routes` (with `metricsQueryOnly`), narrowing which of the
// three read routes — and, on metrics, which paths — a principal's
// VMUser carries at all.
//
// tests/cases/observability-stack/tenancy-reader-scope holds both
// principals the golden render below is generated from: one that sets
// neither field (a full reader, `tenancy.audience`) and one that sets
// both (a metrics-only machine reader with its own, deliberately
// adversarial, audience). This file is the Go side of proving the
// render did what the values file asked, the same role
// tests/agreement_test.go plays for `pkg/tenancy`.
package tests

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const readerScopeGolden = "golden/observability-stack/tenancy-reader-scope.yaml"

// TestOwnAudienceOverridesOnlyItsOwnPrincipal is the first half of the
// feature: one principal pins its own client id, and every principal
// beside it is unaffected.
func TestOwnAudienceOverridesOnlyItsOwnPrincipal(t *testing.T) {
	users := readVMUsers(t, readerScopeGolden)

	byName := map[string]vmUser{}
	for _, u := range users {
		byName[u.Spec.Name] = u
	}

	viewer, ok := byName["example:k8s:viewer"]
	require.True(t, ok, "the full reader from tests/cases/observability-stack/tenancy-reader-scope is missing from the golden render")
	assert.Equal(t, `^(example-observability-client)$`, viewer.Spec.JWT.MatchClaims["aud"],
		"a principal that sets no `audience` of its own must keep `tenancy.audience` — a second principal setting one must not change what this one is pinned to")

	gate, ok := byName["example:example-app:metrics-gate"]
	require.True(t, ok, "the metrics-only reader from tests/cases/observability-stack/tenancy-reader-scope is missing from the golden render")
	assert.Equal(t, `^(metrics-gate\.example\|other\(prod\))$`, gate.Spec.JWT.MatchClaims["aud"],
		"this principal set its own `audience`; the rendered claim should carry it, escaped and anchored exactly as `tenancy.audience` itself is")
}

// TestAdversarialAudienceIsEscapedNotWidened is the property the escaping
// exists for: a client id containing regex metacharacters must pin
// itself and NOTHING wider, the same guarantee
// TestChartAndLibraryRenderTheSameTenancy already asserts for
// `tenancy.audience` — this proves it holds for the per-principal
// override too, against a value chosen to fail if the escaping were ever
// dropped or narrowed to the wrong character set.
func TestAdversarialAudienceIsEscapedNotWidened(t *testing.T) {
	users := readVMUsers(t, readerScopeGolden)

	var gate *vmUser
	for i := range users {
		if users[i].Spec.Name == "example:example-app:metrics-gate" {
			gate = &users[i]
		}
	}
	require.NotNil(t, gate, "the metrics-only reader is missing from the golden render")

	aud := gate.Spec.JWT.MatchClaims["aud"]
	pattern, err := regexp.Compile(aud)
	require.NoErrorf(t, err, "the rendered aud claim %q is not a valid regular expression", aud)

	assert.True(t, pattern.MatchString("metrics-gate.example|other(prod)"),
		"the pattern must still match the literal audience it was rendered for")

	// If `.` were rendered unescaped it matches any character, so a
	// token minted for a DIFFERENT, merely similarly-shaped client
	// would be admitted here.
	assert.False(t, pattern.MatchString("metrics-gateXexample|other(prod)"),
		"an unescaped `.` in the audience would let ANY character stand in for it — the exact way a token minted for a different client passes this pin")

	// If `|` were rendered unescaped it splits the pattern into two
	// alternatives, either of which alone would satisfy `^(...)$` — a
	// token whose `aud` is only the text on ONE side of the pipe would
	// then also be admitted.
	assert.False(t, pattern.MatchString("other(prod)"),
		"an unescaped `|` in the audience would split the pin into two alternatives, admitting a token whose aud is only the text after the pipe")
	assert.False(t, pattern.MatchString("metrics-gate.example"),
		"an unescaped `|` in the audience would split the pin into two alternatives, admitting a token whose aud is only the text before the pipe")
}

// TestMetricsOnlyRendersExactlyTheTwoQueryPaths is the second half: a
// principal restricted to `routes: [metrics]` with `metricsQueryOnly:
// true` gets exactly one targetRef, to the metrics store, carrying
// exactly the two query paths — no series, labels, label values, tsdb
// status, vmui, and no logs or traces targetRef at all.
func TestMetricsOnlyRendersExactlyTheTwoQueryPaths(t *testing.T) {
	users := readVMUsers(t, readerScopeGolden)

	var gate *vmUser
	for i := range users {
		if users[i].Spec.Name == "example:example-app:metrics-gate" {
			gate = &users[i]
		}
	}
	require.NotNil(t, gate, "the metrics-only reader is missing from the golden render")

	require.Len(t, gate.Spec.TargetRefs, 1,
		"routes: [metrics] should render exactly one targetRef — a second one (logs, or traces) is a restriction that did not restrict anything")

	only := gate.Spec.TargetRefs[0]
	assert.Equal(t, []string{"/prometheus/api/v1/query", "/prometheus/api/v1/query_range"}, only.Paths,
		"metricsQueryOnly should render exactly the two query paths and nothing from the full metrics reader list")
}

// TestFullReaderIsUnaffectedByANeighboursRestriction is the negative
// space of the two tests above: the principal that set neither new
// field still gets every route the chart has always given a principal
// with no `routes` — proving the default (nil `routes`) truly means
// "all three", not "whatever the last principal in the list asked for".
func TestFullReaderIsUnaffectedByANeighboursRestriction(t *testing.T) {
	users := readVMUsers(t, readerScopeGolden)

	var viewer *vmUser
	for i := range users {
		if users[i].Spec.Name == "example:k8s:viewer" {
			viewer = &users[i]
		}
	}
	require.NotNil(t, viewer, "the full reader is missing from the golden render")

	// metrics + logs; this case turns the trace store off, the same way
	// tests/cases/observability-stack/tenancy does, so traces is not
	// among them here regardless of `routes`.
	require.Len(t, viewer.Spec.TargetRefs, 2,
		"a principal with no `routes` restricts nothing: it should still carry every route this install's stores support")
	assert.Greater(t, len(viewer.Spec.TargetRefs[0].Paths), 2,
		"the full reader's metrics route should be the full readPaths.metrics list, not the query-only pair a neighbour asked for")
}

// TestPreExistingTenancyCaseIsUnchanged pins the shape
// tests/cases/observability-stack/tenancy has always rendered — no
// principal in it sets `audience` or `routes` — against v0.6.2's own
// output. Checked directly against the v0.6.2 tag while preparing this
// release (`git worktree add --detach v0.6.2`, rendered, diffed
// byte-for-byte: identical); this test pins the same invariant in CI so
// a later change to this template cannot silently narrow or repin a
// principal that asked for neither.
func TestPreExistingTenancyCaseIsUnchanged(t *testing.T) {
	for _, g := range []string{"golden/observability-stack/tenancy.yaml", "golden/observability-stack/tenancy-traces.yaml"} {
		t.Run(g, func(t *testing.T) {
			for _, u := range readVMUsers(t, g) {
				assert.Equalf(t, `^(example-observability-client)$`, u.Spec.JWT.MatchClaims["aud"],
					"%s: %s carries an aud other than tenancy.audience — this case sets no per-principal audience anywhere", g, u.Spec.Name)

				var metricsPaths int
				for _, ref := range u.Spec.TargetRefs {
					if ref.Static.URL != "" && len(ref.Paths) > 0 && ref.Paths[0] == "/prometheus/api/v1/query" {
						metricsPaths = len(ref.Paths)
					}
				}
				assert.Greaterf(t, metricsPaths, 2,
					"%s: %s's metrics route is the query-only pair — this case sets no metricsQueryOnly anywhere, so every principal should still carry the full "+
						"readPaths.metrics list", g, u.Spec.Name)
			}
		})
	}
}
