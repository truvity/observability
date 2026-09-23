// Package tests holds the checks that span an artifact boundary: what the
// Go library renders, and what the chart renders, for the same input.
//
// There is exactly one of those, and it is the one that matters. An
// estate can hold the tenant mapping in the proxy's configuration or in
// the token its issuer mints, and pkg/tenancy renders both from one input
// so they cannot disagree. charts/observability-stack renders the same
// mapping a third time, in the operator's spelling — and a third shape is
// a third place to drift. A difference between them is a difference
// between what a token says a person may read and what the proxy lets
// them read, which nobody notices until someone sees data they should
// not.
//
// So this test renders the README's principals through
// tenancy.RenderVMAuth and compares the result, field by field, against
// the VMUser objects in tests/golden/observability-stack/tenancy.yaml.
// The two spellings differ (vmauth's own config file is snake_case, the
// operator's CRD is camelCase) and that is exactly why the comparison is
// written out rather than assumed.
package tests

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/truvity/observability/pkg/tenancy"
)

// The addresses the chart derives from the upstream charts' own naming
// rules, for a release named after the chart in namespace `observability`
// — which is what hack/golden.sh renders. They are written out here
// rather than read back out of the golden, so that a chart which starts
// pointing somewhere else fails this test as well as producing a diff.
const (
	goldenIssuer     = "https://issuer.example"
	goldenMetricsURL = "http://vmsingle-observability-stack-victoria-metrics-k8s-stack.observability.svc:8428"
	goldenLogsURL    = "http://observability-stack-victoria-logs-single-server.observability.svc:9428"

	// The log-path field names from the same values file. They are not
	// the label keys and cannot be: see pkg/tenancy.Config.
	goldenLogsTenantField = "kubernetes.namespace_labels.tenancy.example.com/project"
	goldenLogsEnvField    = "env"
)

// vmUser is the part of the operator's VMUser that carries tenancy.
// Everything else about the object is the chart's business.
type vmUser struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Name string `yaml:"name"`
		JWT  struct {
			OIDC struct {
				Issuer string `yaml:"issuer"`
			} `yaml:"oidc"`
			MatchClaims          map[string]string `yaml:"matchClaims"`
			DefaultVMAccessClaim struct {
				MetricsExtraFilters    []string `yaml:"metricsExtraFilters"`
				LogsExtraStreamFilters []string `yaml:"logsExtraStreamFilters"`
			} `yaml:"defaultVMAccessClaim"`
		} `yaml:"jwt"`
		LoadBalancingPolicy string        `yaml:"load_balancing_policy"`
		RetryStatusCodes    []int         `yaml:"retry_status_codes"`
		TargetRefs          []vmTargetRef `yaml:"targetRefs"`
	} `yaml:"spec"`
}

// vmTargetRef is one route. `query_args` is the part that enforces
// anything: vmauth applies a principal's `vm_access` claim ONLY by
// substituting a placeholder into the route it forwards on, so a route
// without one carries the grant in the manifest and none of it on the
// wire.
type vmTargetRef struct {
	Static struct {
		URL string `yaml:"url"`
	} `yaml:"static"`
	Paths     []string `yaml:"paths"`
	QueryArgs []struct {
		Name   string   `yaml:"name"`
		Values []string `yaml:"values"`
	} `yaml:"query_args"`
}

// urlPrefix rebuilds what the operator writes into vmauth's `url_prefix`
// for this route, which is the shape pkg/tenancy renders directly.
//
// The two artifacts spell the same thing differently — the library
// writes one string, the CRD splits it into an address and a typed
// argument list — so the comparison happens on the reassembled value
// rather than on the raw text of either.
func (r vmTargetRef) urlPrefix() string {
	if len(r.QueryArgs) == 0 {
		return r.Static.URL
	}
	parts := make([]string, 0, len(r.QueryArgs))
	for _, a := range r.QueryArgs {
		for _, v := range a.Values {
			parts = append(parts, a.Name+"="+v)
		}
	}
	return r.Static.URL + "?" + strings.Join(parts, "&")
}

func TestChartAndLibraryRenderTheSameTenancy(t *testing.T) {
	// The principals from the repository's README, and from
	// tests/cases/observability-stack/tenancy/values.yaml. The two are
	// the same list on purpose.
	cfg := tenancy.Config{
		ClaimName:       "groups",
		MetricsBackend:  goldenMetricsURL,
		LogsBackend:     goldenLogsURL,
		LogsTenantField: goldenLogsTenantField,
		LogsEnvField:    goldenLogsEnvField,
		Principals: []tenancy.Principal{
			{Group: "example:k8s:viewer", Grants: []tenancy.Grant{
				{Env: "devel", AllTenants: true},
			}},
			{Group: "example:dms:deployer", Grants: []tenancy.Grant{
				{Env: "devel", Tenants: []string{"dms"}},
			}},
		},
	}

	want, err := cfg.RenderVMAuth(goldenIssuer)
	require.NoError(t, err)

	got := readVMUsers(t, "golden/observability-stack/tenancy.yaml")
	require.Len(t, got, len(want.Users),
		"the chart renders one VMUser per principal; a count that differs is a principal the proxy will not admit")

	byName := map[string]vmUser{}
	for _, u := range got {
		byName[u.Spec.Name] = u
	}

	for _, user := range want.Users {
		user := user
		t.Run(user.Name, func(t *testing.T) {
			chart, ok := byName[user.Name]
			require.True(t, ok, "the chart renders no VMUser for %q", user.Name)

			assert.Equal(t, user.JWT.OIDC, chart.Spec.JWT.OIDC.Issuer,
				"the proxy would verify tokens against a different issuer than the claim was minted by")
			assert.Equal(t, user.JWT.MatchClaims, chart.Spec.JWT.MatchClaims,
				"a user selected by different claims is a user a token reaches, or does not, for reasons neither side states")

			// The filters. This is the whole of it: these strings are
			// what the store applies to every query the principal makes.
			assert.Equal(t, user.JWT.DefaultVMAccess.MetricsExtraFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.MetricsExtraFilters,
				"the chart and the library disagree about what this principal may read")
			assert.Equal(t, user.JWT.DefaultVMAccess.LogsExtraStreamFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.LogsExtraStreamFilters,
				"the chart and the library disagree about what this principal may read")

			assert.Equal(t, user.LoadBalancingPol, chart.Spec.LoadBalancingPolicy)
			assert.Equal(t, user.RetryStatusCodes, chart.Spec.RetryStatusCodes)

			require.Len(t, chart.Spec.TargetRefs, len(user.URLMap),
				"one route per backend, in the same order")
			for i, row := range user.URLMap {
				assert.Equal(t, row.URLPrefix[0], chart.Spec.TargetRefs[i].urlPrefix(),
					"the chart and the library send this route somewhere else, or apply a different filter on the way")
				assert.Equal(t, row.SrcPaths, chart.Spec.TargetRefs[i].Paths,
					"a route the chart admits and the library does not is a route nobody reviewed")
			}
		})
	}
}

// The test that would have caught it, on the chart's side.
//
// It walks every route in the rendered VMUser objects and fails on any
// that does not carry the placeholder vmauth substitutes the
// principal's filter into. That substitution is the whole of the
// enforcement: vmauth verifies the token, selects the user, computes the
// `vm_access` claim — and then, on a route with no placeholder, throws
// it away and forwards the request unfiltered.
//
// What makes that defect survive review is that the two cases are
// identical everywhere except in the answer to a question nobody asks.
// The manifest shows the grant either way. The install succeeds either
// way. The proxy is healthy either way. And a query for the ONE tenant
// the reviewer has data for returns the same rows either way, because
// the filter that was not applied would not have removed anything. It
// takes a second tenant, or this assertion, to tell them apart.
func TestEveryRenderedReadRouteCarriesItsFilter(t *testing.T) {
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy.yaml") {
		require.NotEmpty(t, user.Spec.TargetRefs)
		for i, ref := range user.Spec.TargetRefs {
			assert.True(t, carriesAFilter(ref),
				"route %d of %q (%s) forwards with no filter argument: every query it carries reaches the store unscoped, while `defaultVMAccessClaim` on the same object still states the grant",
				i, user.Spec.Name, strings.Join(ref.Paths, " "))
		}
	}
}

// And the one signal that cannot carry one, held in its own render so
// that the exception is a file somebody has to change rather than a
// case this test quietly tolerates.
//
// tests/cases/observability-stack/tenancy-traces sets
// `allowUnfilteredTraceReads`, which is the only way the trace route
// renders at all — see
// tests/invalid/observability-stack/traces-without-unfiltered-optin.yaml
// for the refusal when it is not set.
func TestOnlyTheTraceRouteIsUnfiltered(t *testing.T) {
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy-traces.yaml") {
		require.Len(t, user.Spec.TargetRefs, 3, "metrics, logs and traces")

		var unfiltered []string
		for _, ref := range user.Spec.TargetRefs {
			if !carriesAFilter(ref) {
				unfiltered = append(unfiltered, strings.Join(ref.Paths, " "))
			}
		}

		require.Len(t, unfiltered, 1,
			"exactly one route in this render may be unfiltered, and it is the trace route; these are: %v", unfiltered)
		assert.Contains(t, unfiltered[0], "/select/jaeger/",
			"the unfiltered route is not the trace route, so a route that could be scoped is not being scoped")
	}
}

// The filter arguments and placeholders the chart renders are the ones
// pkg/tenancy declares.
//
// A chart naming `extra_label` where the library names `extra_filters`,
// or `{{.MetricsExtraLabels}}` where it names `{{.MetricsExtraFilters}}`,
// would render, install, and enforce something other than the grant — or
// nothing at all, since vmauth forwards an unknown placeholder to the
// store as a literal string.
func TestChartUsesTheLibrarysFilterArguments(t *testing.T) {
	want := map[string]string{
		tenancy.MetricsFilterArg: tenancy.MetricsFilterPlaceholder,
		tenancy.LogsFilterArg:    tenancy.LogsFilterPlaceholder,
	}

	seen := map[string]string{}
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy.yaml") {
		for _, ref := range user.Spec.TargetRefs {
			for _, a := range ref.QueryArgs {
				require.Len(t, a.Values, 1,
					"a route carries one filter argument with one value; vmauth substitutes only a value that IS the placeholder")
				seen[a.Name] = a.Values[0]
			}
		}
	}
	assert.Equal(t, want, seen,
		"the chart enforces with different arguments than the library renders")
}

// carriesAFilter reports whether this route would actually apply a
// principal's grant: one of the library's filter arguments, carrying its
// placeholder as the WHOLE value.
//
// The whole-value part is not pedantry. vmauth substitutes a query
// argument by looking its complete value up in a map, not by replacing a
// substring, so `extra_filters=x{{.MetricsExtraFilters}}` is forwarded
// to the store exactly as written — and the check is the library's own,
// so the chart cannot be judged by a looser rule than the library is.
func carriesAFilter(ref vmTargetRef) bool {
	prefix := ref.urlPrefix()
	for arg, placeholder := range map[string]string{
		tenancy.MetricsFilterArg: tenancy.MetricsFilterPlaceholder,
		tenancy.LogsFilterArg:    tenancy.LogsFilterPlaceholder,
	} {
		if tenancy.CarriesFilter(prefix, arg, placeholder) {
			return true
		}
	}
	return false
}

func readVMUsers(t *testing.T, path string) []vmUser {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the golden render is the chart's side of this comparison; regenerate it with `just golden`")

	var users []vmUser
	for _, doc := range strings.Split(string(raw), "\n---") {
		var u vmUser
		require.NoError(t, yaml.Unmarshal([]byte(doc), &u))
		if u.Kind == "VMUser" {
			users = append(users, u)
		}
	}
	require.NotEmpty(t, users, "no VMUser in %s", path)
	return users
}

// The second boundary: what the collectors STAMP and what the proxy
// FILTERS ON.
//
// charts/observability-emitters writes `tenant` and `env` onto every
// series, log stream and span; pkg/tenancy renders the filters that select
// on those names. The two are one vocabulary, held in two places, and a
// difference between them is not an error anywhere — it is an empty
// result. A person asks for their tenant's data, gets nothing back, and
// reads it as "nothing is running" rather than as "the filter names a
// label that does not exist".
//
// So: the chart's defaults must be the library's defaults, and the values
// must actually reach every site that stamps. The second half matters more
// than it looks — a key that is a value in one template and a constant in
// another renders correctly for anyone who leaves the default alone and
// silently wrong for anyone who does not.
const emittersValues = "../charts/observability-emitters/values.yaml"

func TestEmittersStampWhatTheLibraryFilters(t *testing.T) {
	var chart struct {
		Tenancy struct {
			TenantLabel string `yaml:"tenantLabel"`
			EnvLabel    string `yaml:"envLabel"`
		} `yaml:"tenancy"`
	}
	raw, err := os.ReadFile(emittersValues)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &chart))

	// The library's own defaults, read back out of a rendered filter rather
	// than from an unexported function: the filter is what the store
	// applies, so it is the only statement of these names that can be
	// wrong.
	claim, err := tenancy.Config{
		ClaimName:       "groups",
		LogsTenantField: goldenLogsTenantField,
		LogsEnvField:    goldenLogsEnvField,
		Principals: []tenancy.Principal{
			{Group: "example:reader", Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example"}}}},
		},
	}.RenderClaim(tenancy.Principal{
		Group:  "example:reader",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example"}}},
	})
	require.NoError(t, err)
	require.Len(t, claim.MetricsExtraFilters, 1)

	matchers := regexp.MustCompile(`([a-z0-9-]+)=~?"`).FindAllStringSubmatch(claim.MetricsExtraFilters[0], -1)
	require.Len(t, matchers, 2, "the filter should carry one env matcher and one tenant matcher: %s", claim.MetricsExtraFilters[0])

	assert.Equal(t, matchers[0][1], chart.Tenancy.EnvLabel,
		"the emitters stamp an environment label the proxy's filters do not select on: every query scoped to an environment would return nothing, and nothing would report it")
	assert.Equal(t, matchers[1][1], chart.Tenancy.TenantLabel,
		"the emitters stamp a tenant label the proxy's filters do not select on: every tenant-scoped query would return nothing, and nothing would report it")
}

// And the values are plumbed, not decorative.
//
// tests/cases/observability-emitters/everything sets `tenantLabel: owner`
// and `envLabel: cell` for exactly this: every place the chart stamps has
// to carry those names, and none of them may carry the defaults. A site
// that hardcoded `tenant` renders identically for everyone who leaves the
// default alone, which is how it survives review.
func TestEmittersLabelKeysReachEverySite(t *testing.T) {
	raw, err := os.ReadFile("golden/observability-emitters/everything.yaml")
	require.NoError(t, err, "regenerate the golden renders with `just golden`")
	golden := string(raw)

	for _, site := range []struct{ what, needle string }{
		{"the metrics agent's env stamp", "target_label: cell"},
		{"the metrics agent's tenant stamp", "target_label: owner"},
		{"the label the agent drops when a target exports its own", "regex: exported_(owner|cell)"},
		{"the gateway's env statement", `set(attributes["cell"], "example-two")`},
		{"the gateway's tenant statement", `set(attributes["owner"], "platform")`},
		{"the log stream fields the gateway declares", `VL-Stream-Fields: "owner,cell,`},
		{"the log agent's env field", `--kubernetesCollector.extraFields={"cell":"example-two"}`},
	} {
		assert.Contains(t, golden, site.needle,
			"%s does not use the configured label key, so that key is a constant somewhere it should be a value", site.what)
	}

	// The defaults must not survive anywhere the chart stamps. Checked as
	// exact rendered fragments rather than as bare words, because `tenant`
	// and `env` legitimately appear in this file's own comments.
	for _, leak := range []string{
		"target_label: tenant",
		"target_label: env",
		`set(attributes["tenant"]`,
		`set(attributes["env"]`,
	} {
		assert.NotContains(t, golden, leak,
			"a stamping site renders the DEFAULT label key while the values set another one — it would look correct for everyone who never changed it")
	}
}

// The same boundary, on the log path, which is where it was missing.
//
// The metrics half above compares two label keys and stops there, because
// on the metrics path the collector's label key IS the name the filter
// selects on. On the log path it is not, and cannot be made to be:
// vlagent delivers a namespace label as `kubernetes.namespace_labels.<key>`
// and can rename no field. So the agreement is between three things —
// what the log agent is told to make a STREAM FIELD, what the chart
// derives from the namespace label key, and what pkg/tenancy puts in the
// filter — and it is checked against the rendered manifest rather than
// against a value, because the flag is the only one of the three that the
// store ever sees.
//
// The last assertion is the defect this test exists for. `tenant` is not
// among the agent's stream fields and cannot be, so a logs filter naming
// it selects nothing: an empty result, no error, and a reader who
// concludes their service logged nothing.
const (
	emittersLogsCase   = "cases/observability-emitters/minimal/values.yaml"
	emittersLogsGolden = "golden/observability-emitters/minimal.yaml"
)

func TestEmittersStampTheLogFieldsTheLibraryFilters(t *testing.T) {
	var defaults struct {
		Tenancy struct {
			TenantLabel string `yaml:"tenantLabel"`
			EnvLabel    string `yaml:"envLabel"`
		} `yaml:"tenancy"`
	}
	raw, err := os.ReadFile(emittersValues)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &defaults))

	var values struct {
		Tenancy struct {
			NamespaceLabels struct {
				Project string `yaml:"project"`
			} `yaml:"namespaceLabels"`
		} `yaml:"tenancy"`
	}
	raw, err = os.ReadFile(emittersLogsCase)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &values))
	require.NotEmpty(t, values.Tenancy.NamespaceLabels.Project)

	// What the chart derives, spelled out here rather than imported, so
	// that a chart which starts deriving something else fails this test
	// and not only the golden diff.
	wantTenantField := "kubernetes.namespace_labels." + values.Tenancy.NamespaceLabels.Project
	wantEnvField := defaults.Tenancy.EnvLabel

	// What the agent is actually told, read out of the rendered manifest.
	golden, err := os.ReadFile(emittersLogsGolden)
	require.NoError(t, err, "regenerate the golden renders with `just golden`")

	streamFields := flagList(t, string(golden), "--kubernetesCollector.streamFields=")
	assert.Contains(t, streamFields, wantTenantField,
		"the tenant field the proxy filters on is not one of the log agent's stream fields; a LogsQL stream filter selects only on stream fields, so every tenant-scoped log query would return nothing")
	assert.Contains(t, streamFields, wantEnvField,
		"the environment field the proxy filters on is not one of the log agent's stream fields")
	assert.Contains(t, string(golden), fmt.Sprintf("--kubernetesCollector.extraFields={%q:", wantEnvField),
		"the environment reaches the log store under the name the agent was given, and this is where it is given")

	// And what the library puts in the filter, for the same two names.
	claim, err := tenancy.Config{
		ClaimName:       "groups",
		LogsTenantField: wantTenantField,
		LogsEnvField:    wantEnvField,
		Principals: []tenancy.Principal{
			{Group: "example:reader", Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example"}}}},
		},
	}.RenderClaim(tenancy.Principal{
		Group:  "example:reader",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example"}}},
	})
	require.NoError(t, err)
	require.Len(t, claim.LogsExtraStreamFilters, 1)
	filter := claim.LogsExtraStreamFilters[0]

	assert.Contains(t, filter, strconv.Quote(wantTenantField))
	assert.Contains(t, filter, strconv.Quote(wantEnvField))

	// The defect, asserted from the collector's side.
	assert.NotContains(t, streamFields, defaults.Tenancy.TenantLabel,
		"%q is not a stream field on the log path and cannot be one — vlagent can rename no field — so a filter naming it returns an empty result with no error at all", defaults.Tenancy.TenantLabel)
	assert.NotContains(t, filter, strconv.Quote(defaults.Tenancy.TenantLabel),
		"the logs filter names the metrics tenant label; nothing on the log path carries it")
}

// flagList returns the comma-separated value of the first container
// argument in the rendered manifest that starts with prefix.
func flagList(t *testing.T, golden, prefix string) []string {
	t.Helper()

	for _, line := range strings.Split(golden, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return strings.Split(after, ",")
		}
	}
	t.Fatalf("no %s argument in the rendered manifest", prefix)
	return nil
}
