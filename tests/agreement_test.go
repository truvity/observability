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
	"os"
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
		LoadBalancingPolicy string `yaml:"load_balancing_policy"`
		RetryStatusCodes    []int  `yaml:"retry_status_codes"`
		TargetRefs          []struct {
			Static struct {
				URL string `yaml:"url"`
			} `yaml:"static"`
			Paths []string `yaml:"paths"`
		} `yaml:"targetRefs"`
	} `yaml:"spec"`
}

func TestChartAndLibraryRenderTheSameTenancy(t *testing.T) {
	// The principals from the repository's README, and from
	// tests/cases/observability-stack/tenancy/values.yaml. The two are
	// the same list on purpose.
	cfg := tenancy.Config{
		ClaimName:      "groups",
		MetricsBackend: goldenMetricsURL,
		LogsBackend:    goldenLogsURL,
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
			assert.Equal(t, user.DefaultVMAccess.MetricsExtraFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.MetricsExtraFilters,
				"the chart and the library disagree about what this principal may read")
			assert.Equal(t, user.DefaultVMAccess.LogsExtraStreamFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.LogsExtraStreamFilters,
				"the chart and the library disagree about what this principal may read")

			assert.Equal(t, user.LoadBalancingPol, chart.Spec.LoadBalancingPolicy)
			assert.Equal(t, user.RetryStatusCodes, chart.Spec.RetryStatusCodes)

			require.Len(t, chart.Spec.TargetRefs, len(user.URLMap),
				"one route per backend, in the same order")
			for i, row := range user.URLMap {
				assert.Equal(t, row.URLPrefix[0], chart.Spec.TargetRefs[i].Static.URL)
				assert.Equal(t, row.SrcPaths, chart.Spec.TargetRefs[i].Paths,
					"a route the chart admits and the library does not is a route nobody reviewed")
			}
		})
	}
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
