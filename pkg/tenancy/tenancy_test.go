package tenancy_test

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/tenancy"
)

// The log-path field names a Kubernetes collector actually produces. They
// are written out rather than defaulted because the package refuses to
// default them, and the shape is the point: a namespace label reaches the
// log store prefixed, and its key carries dots and a slash.
const (
	logsTenantField = "kubernetes.namespace_labels.tenancy.example.com/project"
	logsEnvField    = "env"
)

func base() tenancy.Config {
	return tenancy.Config{
		ClaimName:       "groups",
		MetricsBackend:  "http://metrics.example:8428",
		LogsBackend:     "http://logs.example:9428",
		TracesBackend:   "http://traces.example:10428",
		LogsTenantField: logsTenantField,
		LogsEnvField:    logsEnvField,
		Principals: []tenancy.Principal{{
			Group:  "example:k8s:viewer",
			Grants: []tenancy.Grant{{Env: "devel", AllTenants: true}},
		}},
	}
}

func twoGrants() tenancy.Principal {
	return tenancy.Principal{
		Group: "example:app:deployer",
		Grants: []tenancy.Grant{
			// Deliberately unsorted, and the tenants too: the render must
			// be stable, or every unrelated change produces a diff and
			// people stop reading them.
			{Env: "prod", Tenants: []string{"other-app", "example-app"}},
			{Env: "devel", Tenants: []string{"example-app"}},
		},
	}
}

func TestClaimRendersOneMetricsFilterPerGrant(t *testing.T) {
	claim, err := base().RenderClaim(twoGrants())
	require.NoError(t, err)

	assert.Equal(t, []string{
		`{env="devel",tenant=~"^(example-app)$"}`,
		`{env="prod",tenant=~"^(example-app|other-app)$"}`,
	}, claim.MetricsExtraFilters)
}

// One stream filter for the whole principal, with the grants as
// alternatives inside it.
//
// Not one per grant: VictoriaLogs AND-s every `extra_stream_filters`
// argument into the query as its own global constraint, so two entries
// naming two environments intersect in nothing and the principal with the
// most access is the one who gets an empty screen.
func TestClaimRendersOneLogsFilterForEveryGrant(t *testing.T) {
	claim, err := base().RenderClaim(twoGrants())
	require.NoError(t, err)

	require.Len(t, claim.LogsExtraStreamFilters, 1,
		"a second entry would be AND-ed with the first, not OR-ed: %v", claim.LogsExtraStreamFilters)
	assert.Equal(t,
		`_stream:{"env"="devel","kubernetes.namespace_labels.tenancy.example.com/project"=~"^(example-app)$"`+
			` or "env"="prod","kubernetes.namespace_labels.tenancy.example.com/project"=~"^(example-app|other-app)$"}`,
		claim.LogsExtraStreamFilters[0])
}

// The inversion of the test this replaces, which asserted that the two
// filter sets were identical and so encoded the defect as a requirement.
//
// They are not identical and must not be. The metrics store carries the
// tenant in a label; the log store carries it in a field whose name the
// log agent chose, and vlagent cannot rename a field. A logs filter
// naming the metrics label selects a field the streams do not have, which
// returns an empty result rather than an error — the one failure this
// repository exists to prevent.
func TestLogsFiltersNeverNameTheMetricsFields(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(twoGrants())
	require.NoError(t, err)

	require.NotEmpty(t, claim.LogsExtraStreamFilters)
	for _, f := range claim.LogsExtraStreamFilters {
		assert.NotContains(t, f, `"tenant"=`,
			"the logs filter names the METRICS tenant label; on the log path no such field exists, and the query would return nothing with no error at all: %s", f)
		assert.Contains(t, f, strconv.Quote(c.LogsTenantField),
			"the logs filter does not name the field the caller stated: %s", f)
	}

	assert.NotEqual(t, claim.MetricsExtraFilters, claim.LogsExtraStreamFilters,
		"the two paths name different fields and combine their entries differently; identical lists mean one of them is not being rendered")
}

func TestAllTenantsOmitsTheTenantMatcher(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(c.Principals[0])
	require.NoError(t, err)

	require.Len(t, claim.MetricsExtraFilters, 1)
	assert.Equal(t, `{env="devel"}`, claim.MetricsExtraFilters[0])
	assert.NotContains(t, claim.MetricsExtraFilters[0], "tenant",
		"a match-all tenant matcher would drop series that carry no tenant label at all, which mid-rollout is exactly the data someone is looking for")

	// The same on the log path, where it matters more: a namespace with no
	// project label produces streams with no tenancy field at all, and a
	// match-all matcher would hide every one of them.
	require.Len(t, claim.LogsExtraStreamFilters, 1)
	assert.Equal(t, `_stream:{"env"="devel"}`, claim.LogsExtraStreamFilters[0])
}

func TestLabelKeysAreInputs(t *testing.T) {
	c := base()
	c.TenantLabel = "owner"
	c.EnvLabel = "stage"
	c.LogsTenantField = "kubernetes.namespace_labels.owner"
	c.LogsEnvField = "stage"
	claim, err := c.RenderClaim(tenancy.Principal{
		Group:  "g",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example-app"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, `{stage="devel",owner=~"^(example-app)$"}`, claim.MetricsExtraFilters[0])
	assert.Equal(t, `_stream:{"stage"="devel","kubernetes.namespace_labels.owner"=~"^(example-app)$"}`,
		claim.LogsExtraStreamFilters[0])
}

// The log field names are held to a different shape from the tenant
// names, and this is the shape a real one has: prefixed by the collector,
// and carrying the dots and the slash of a Kubernetes label key with a
// domain prefix. nameRE would refuse every one of these, which is why
// there are two shapes and not one loosened one.
func TestRealLogFieldNamesAreAccepted(t *testing.T) {
	for _, field := range []string{
		"kubernetes.namespace_labels.tenancy.example.com/project",
		"kubernetes.namespace_labels.project",
		"kubernetes.pod_namespace",
		"tenant",
		"resource_attr_service.namespace",
		"a-b_c.d/e",
	} {
		t.Run(field, func(t *testing.T) {
			c := base()
			c.LogsTenantField = field
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example-app"}}},
			}}
			require.NoError(t, c.Validate())

			claim, err := c.RenderClaim(c.Principals[0])
			require.NoError(t, err)
			assert.Contains(t, claim.LogsExtraStreamFilters[0], strconv.Quote(field))
		})
	}
}

// The same security property as TestHostileNamesAreRefused, on the new
// surface: the field name is interpolated into the same filter, so a name
// that is not a plain field name must never reach the renderer.
func TestHostileLogFieldNamesAreRefused(t *testing.T) {
	hostile := map[string]string{
		`quote closes the name`:     `tenant"=~"^(.*)$"} or {"env`,
		`brace ends the filter`:     `tenant"} or {"x`,
		`comma opens a second term`: `tenant,x`,
		`equals ends the name`:      `tenant=x`,
		`alternation`:               `tenant|other`,
		`backslash`:                 `tenant\x`,
		`space`:                     `tenant field`,
		`colon is logsql syntax`:    `tenant:x`,
		`leading dot`:               `.tenant`,
		`leading slash`:             `/tenant`,
		`leading dash`:              `-tenant`,
		`newline`:                   "tenant\nx",
		`a lone glue character`:     `.`,
		`regexp that matches all`:   `.*`,
		`backtick`:                  "tenant`x",
		`single quote`:              `tenant'x`,
		`parenthesis`:               `tenant(x)`,
		`tilde`:                     `tenant~x`,
	}

	for what, field := range hostile {
		t.Run("tenantField/"+what, func(t *testing.T) {
			c := base()
			c.LogsTenantField = field
			err := c.Validate()
			require.Error(t, err, "a log field named %q must be refused, not escaped", field)
			assert.Contains(t, err.Error(), "not a valid log field name")

			_, renderErr := c.RenderClaim(c.Principals[0])
			require.Error(t, renderErr, "and it must not survive as far as a rendered filter")
		})

		t.Run("envField/"+what, func(t *testing.T) {
			c := base()
			c.LogsEnvField = field
			require.Error(t, c.Validate(), "a log field named %q must be refused", field)
		})
	}
}

// The tenant-name rule is NOT loosened to make a field name fit. A field
// name may carry a dot and a slash; a tenant may not, because a tenant
// name goes inside the alternation where a `.` is a regular-expression
// metacharacter.
func TestTheFieldShapeDoesNotLoosenTheTenantShape(t *testing.T) {
	c := base()
	c.Principals = []tenancy.Principal{{
		Group:  "g",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"example.com/app"}}},
	}}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid name")
}

// A log path nobody has named is refused rather than guessed, from both
// entry points.
func TestTheLogPathMustBeNamed(t *testing.T) {
	for _, tc := range []struct {
		what   string
		mutate func(*tenancy.Config)
		want   string
	}{
		{"tenant field", func(c *tenancy.Config) { c.LogsTenantField = "" }, "logsTenantField is empty"},
		{"env field", func(c *tenancy.Config) { c.LogsEnvField = "" }, "logsEnvField is empty"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			c := base()
			tc.mutate(&c)

			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)

			_, err = c.RenderClaim(c.Principals[0])
			require.Error(t, err, "RenderClaim must refuse rather than render a filter on a guessed field")

			_, err = c.RenderVMAuth("https://issuer.example")
			require.Error(t, err, "RenderVMAuth must refuse for the same reason")
		})
	}
}

// The refusal has to teach, because the failure it prevents teaches
// nothing: an empty result carries no message at all.
func TestTheRefusalExplainsWhyTheLogPathIsDifferent(t *testing.T) {
	c := base()
	c.LogsTenantField = ""

	err := c.Validate()
	require.Error(t, err)
	msg := err.Error()

	for _, phrase := range []string{
		"has no default",
		"kubernetes.namespace_labels",
		"rename no field",
		"empty result rather than an error",
	} {
		assert.Contains(t, msg, phrase,
			"the refusal should leave the reader knowing why the log path is not the metrics path")
	}
}

// The security property this package exists for: a name is interpolated
// into a regular expression, so a name that is not a plain name must never
// reach the renderer.
func TestHostileNamesAreRefused(t *testing.T) {
	hostile := []string{
		"dms|url-shortener", // alternation: would grant a second tenant
		".*",                // would grant every tenant
		"dms)|(.*",          // would close the group and open a new one
		"dms$|^",            // would defeat the anchors
		"DMS",               // uppercase is not the label shape
		"dms ",              // trailing space
		"-dms",              // must start alphanumeric
		"",                  // empty
	}

	for _, name := range hostile {
		t.Run("tenant/"+name, func(t *testing.T) {
			c := base()
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{name}}},
			}}
			err := c.Validate()
			require.Error(t, err, "a tenant named %q must be refused, not escaped", name)
			assert.Contains(t, err.Error(), "not a valid name")
		})

		t.Run("env/"+name, func(t *testing.T) {
			c := base()
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Env: name, Tenants: []string{"dms"}}},
			}}
			require.Error(t, c.Validate(), "an env named %q must be refused", name)
		})
	}
}

// Whatever a hostile name would have tried to do, it must not survive as
// far as a rendered filter.
func TestHostileNameNeverReachesAFilter(t *testing.T) {
	c := base()
	_, err := c.RenderClaim(tenancy.Principal{
		Group:  "g",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"dms)|(.*"}}},
	})
	require.Error(t, err)
}

func TestRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*tenancy.Config)
		want   string
	}{
		"empty tenant list is not everything": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Env: "devel"}}
			},
			want: "refused rather than read as",
		},
		"allTenants and a list together": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Env: "devel", AllTenants: true, Tenants: []string{"dms"}}}
			},
			want: "One of them is wrong",
		},
		"a principal with no grants": {
			mutate: func(c *tenancy.Config) { c.Principals[0].Grants = nil },
			want:   "no grants",
		},
		"no principals at all": {
			mutate: func(c *tenancy.Config) { c.Principals = nil },
			want:   "admit nobody",
		},
		"a duplicated group": {
			mutate: func(c *tenancy.Config) {
				c.Principals = append(c.Principals, c.Principals[0])
			},
			want: "appears twice",
		},
		"the same env granted twice to one principal": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{
					{Env: "devel", Tenants: []string{"dms"}},
					{Env: "devel", Tenants: []string{"url-shortener"}},
				}
			},
			want: "granted twice",
		},
		"a tenant listed twice": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Env: "devel", Tenants: []string{"dms", "dms"}}}
			},
			want: "listed twice",
		},
		"no claim name": {
			mutate: func(c *tenancy.Config) { c.ClaimName = "" },
			want:   "claimName is empty",
		},
		"an empty group": {
			mutate: func(c *tenancy.Config) { c.Principals[0].Group = "" },
			want:   "group is empty",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Validate reports every problem, not just the first: a derivation that
// produced three bad names should not have to be run four times.
func TestValidateReportsEveryProblem(t *testing.T) {
	c := base()
	c.ClaimName = ""
	c.Principals = []tenancy.Principal{{
		Group: "g",
		Grants: []tenancy.Grant{
			{Env: "devel", Tenants: []string{"BAD"}},
			{Env: "PROD", Tenants: []string{"dms"}},
		},
	}}

	err := c.Validate()
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "claimName is empty")
	assert.Contains(t, msg, `"BAD"`)
	assert.Contains(t, msg, `"PROD"`)
	assert.GreaterOrEqual(t, strings.Count(msg, "\n"), 2, "each problem on its own line")
}

func TestVMAuthConfig(t *testing.T) {
	c := base()
	// The trace route cannot be scoped, so it renders only when the
	// caller has said so. See TestTracesRouteIsRefusedUntilItIsAskedFor.
	c.AllowUnfilteredTraceReads = true
	c.Principals = append(c.Principals, tenancy.Principal{
		Group:  "example:dms:deployer",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"dms"}}},
	})

	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	require.Len(t, cfg.Users, 2)

	viewer, deployer := cfg.Users[0], cfg.Users[1]

	assert.Equal(t, map[string]string{"groups": "example:k8s:viewer"}, viewer.JWT.MatchClaims)
	assert.Equal(t, "https://issuer.example", viewer.JWT.OIDC)

	// The whole point, asserted: two principals, two different reaches.
	assert.Equal(t, []string{`{env="devel"}`}, viewer.DefaultVMAccess.MetricsExtraFilters)
	assert.Equal(t, []string{`{env="devel",tenant=~"^(dms)$"}`}, deployer.DefaultVMAccess.MetricsExtraFilters)

	assert.Equal(t, "first_available", viewer.LoadBalancingPol,
		"a read balanced onto the replica still replaying its buffer returns a gap, and a gap reads as an outage")
	assert.Equal(t, []int{500, 502, 503}, viewer.RetryStatusCodes)

	require.Len(t, viewer.URLMap, 3, "metrics, logs and traces")

	// The two routes that can be scoped carry the placeholder vmauth
	// substitutes the filters above into; without it the claim is
	// computed and discarded.
	assert.Equal(t,
		[]string{"http://metrics.example:8428?extra_filters={{.MetricsExtraFilters}}"},
		viewer.URLMap[0].URLPrefix)
	assert.Equal(t,
		[]string{"http://logs.example:9428?extra_stream_filters={{.LogsExtraStreamFilters}}"},
		viewer.URLMap[1].URLPrefix)

	// And the one that cannot carries nothing, rather than something that
	// looks like enforcement.
	assert.Equal(t, []string{"http://traces.example:10428"}, viewer.URLMap[2].URLPrefix)
}

func TestVMAuthWithoutTracesOmitsItsRoute(t *testing.T) {
	c := base()
	c.TracesBackend = ""
	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	assert.Len(t, cfg.Users[0].URLMap, 2)
}

func TestVMAuthRequiresItsInputs(t *testing.T) {
	t.Run("issuer", func(t *testing.T) {
		_, err := base().RenderVMAuth("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could verify no token")
	})

	t.Run("backends", func(t *testing.T) {
		c := base()
		c.LogsBackend = ""
		_, err := c.RenderVMAuth("https://issuer.example")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required to render a vmauth configuration")
	})

	t.Run("an invalid config never renders", func(t *testing.T) {
		c := base()
		c.Principals[0].Grants = []tenancy.Grant{{Env: "devel"}}
		_, err := c.RenderVMAuth("https://issuer.example")
		require.Error(t, err)
	})
}

// Rendering must not mutate its input: a caller that renders twice, or
// renders and then writes the config back out, must see what it passed in.
func TestRenderDoesNotMutateItsInput(t *testing.T) {
	c := base()
	c.Principals[0].Grants = []tenancy.Grant{
		{Env: "prod", Tenants: []string{"url-shortener", "dms"}},
		{Env: "devel", Tenants: []string{"dms"}},
	}

	_, err := c.RenderClaim(c.Principals[0])
	require.NoError(t, err)

	assert.Equal(t, "prod", c.Principals[0].Grants[0].Env, "grant order changed under the caller")
	assert.Equal(t, []string{"url-shortener", "dms"}, c.Principals[0].Grants[0].Tenants, "tenant order changed under the caller")
}

// A reader's route must not also be a writer's route. The shapes this
// guards against are the convenient ones: a prefix that covers the whole
// API surface covers the write and delete endpoints with it, and a route
// that reaches /internal/* reaches endpoints whose own authKey flags
// replace the store's basic auth rather than adding to it.
func TestReadPathsAdmitNoWrites(t *testing.T) {
	forbidden := []string{"write", "import", "delete", "admin", "internal", "insert"}

	for _, paths := range [][]string{tenancy.MetricsReadPaths, tenancy.LogsReadPaths, tenancy.TracesReadPaths} {
		require.NotEmpty(t, paths)
		for _, p := range paths {
			assert.NotEqual(t, "/.*", p, "a route that matches everything is not a read route")
			for _, word := range forbidden {
				assert.NotContains(t, p, word, "read route %q reaches a write path", p)
			}
			re, err := regexp.Compile("^" + p + "$")
			require.NoError(t, err, "every route is a regular expression vmauth has to compile")
			for _, write := range []string{
				"/prometheus/api/v1/write",
				"/api/v1/write",
				"/prometheus/api/v1/import",
				"/prometheus/api/v1/admin/tsdb/delete_series",
				"/internal/force_merge",
				"/insert/jsonline",
			} {
				assert.False(t, re.MatchString(write), "read route %q also admits %q", p, write)
			}
		}
	}
}

// The test that would have caught it.
//
// Every route a reader is given has to carry the placeholder vmauth
// substitutes its filter into, because that substitution is the ONLY
// thing that applies the claim: a route without one forwards the query
// with no filter at all while `default_vm_access_claim` beside it still
// states the grant, and the two are indistinguishable in any render, any
// health check, and any query that asks about a single tenant.
//
// It walks what was rendered rather than checking the declarations,
// because a route is only enforced where it is emitted.
func TestEveryRenderedReadRouteCarriesItsFilter(t *testing.T) {
	c := base()
	c.AllowUnfilteredTraceReads = true

	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Users)

	// The routes, in the order RenderVMAuth emits them, and what each one
	// must carry. The trace route is the exception and is named as one
	// here so that a fourth route added without a filter fails this test
	// rather than joining it.
	want := []struct {
		arg         string
		placeholder string
	}{
		{tenancy.MetricsFilterArg, tenancy.MetricsFilterPlaceholder},
		{tenancy.LogsFilterArg, tenancy.LogsFilterPlaceholder},
		{"", ""}, // traces: nothing to carry it in, admitted by name above
	}

	for _, user := range cfg.Users {
		require.Len(t, user.URLMap, len(want),
			"a route was added or removed without this test being told which of the two it is")
		for i, row := range user.URLMap {
			require.Len(t, row.URLPrefix, 1)
			prefix := row.URLPrefix[0]

			if want[i].arg == "" {
				assert.NotContains(t, prefix, "{{",
					"route %d is the unscoped one and must not pretend otherwise: %s", i, prefix)
				continue
			}

			assert.True(t, tenancy.CarriesFilter(prefix, want[i].arg, want[i].placeholder),
				"route %d of user %q reaches %s with no %s=%s, so every query it forwards is unfiltered while the claim beside it says otherwise",
				i, user.Name, prefix, want[i].arg, want[i].placeholder)
		}
	}
}

// And the same property, asserted against the route declarations rather
// than a render: a route that lost its placeholder must not survive
// Validate either, because Validate is what a caller runs before
// rendering anything.
func TestValidateRefusesARouteWithoutItsFilter(t *testing.T) {
	for _, r := range []tenancy.ReadRoute{tenancy.MetricsRead, tenancy.LogsRead} {
		assert.NotEmpty(t, r.FilterArg(), "read route %q has no filter argument", r.Name())
		assert.NotEmpty(t, r.Placeholder(), "read route %q has no filter placeholder", r.Name())
		assert.Empty(t, r.Unenforceable(), "read route %q claims it cannot be scoped, but it can", r.Name())
	}

	// The trace route is the only one that may say it cannot be scoped,
	// and it must say WHY, because that sentence is quoted back to
	// whoever is being asked to accept it.
	assert.NotEmpty(t, tenancy.TracesRead.Unenforceable())
	assert.Empty(t, tenancy.TracesRead.FilterArg(),
		"a route cannot both be unenforceable and carry a filter")
}

// A placeholder vmauth does not know is forwarded to the store as the
// literal string `{{.Whatever}}`, so the set of names is closed.
func TestFilterPlaceholdersAreTheOnesVMAuthSubstitutes(t *testing.T) {
	assert.Equal(t, "{{.MetricsExtraFilters}}", tenancy.MetricsFilterPlaceholder)
	assert.Equal(t, "{{.LogsExtraStreamFilters}}", tenancy.LogsFilterPlaceholder)
	assert.Equal(t, "extra_filters", tenancy.MetricsFilterArg)
	assert.Equal(t, "extra_stream_filters", tenancy.LogsFilterArg)
}

// vmauth substitutes a query argument only when the placeholder is the
// WHOLE value: the lookup is `data[value]`, a map read on the complete
// string, not a substring replacement. A value that merely contains a
// placeholder is forwarded as written — which is an unparseable filter
// at best and an ignored one at worst.
func TestAFilterMustBeTheWholeQueryArgValue(t *testing.T) {
	const arg, ph = tenancy.MetricsFilterArg, tenancy.MetricsFilterPlaceholder

	assert.True(t, tenancy.CarriesFilter("http://x:8428?"+arg+"="+ph, arg, ph))
	assert.False(t, tenancy.CarriesFilter("http://x:8428?"+arg+"={env=\"prod\"}"+ph, arg, ph),
		"a value that merely contains the placeholder is never substituted")
	assert.False(t, tenancy.CarriesFilter("http://x:8428?other="+ph, arg, ph),
		"the right placeholder in the wrong argument filters nothing")
	assert.False(t, tenancy.CarriesFilter("http://x:8428", arg, ph))
}

// The trace route is admitted only by name.
//
// Nothing can scope it — VictoriaTraces' Jaeger and Tempo select APIs
// accept no query argument a proxy could put a filter in — so rendering
// it beside two scoped routes, looking identical to them, is the defect
// one level down. It is a refusal until somebody writes down that they
// accept it.
func TestTracesRouteIsRefusedUntilItIsAskedFor(t *testing.T) {
	c := base()
	require.NotEmpty(t, c.TracesBackend)

	_, err := c.RenderVMAuth("https://issuer.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AllowUnfilteredTraceReads")
	assert.Contains(t, err.Error(), "cannot be scoped",
		"the refusal has to say what is wrong, not just that something is")

	c.AllowUnfilteredTraceReads = true
	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	require.Len(t, cfg.Users[0].URLMap, 3)

	// And it admits the trace route ONLY. A flag that quietly relaxed the
	// other two would be the same defect with a consent form attached.
	assert.True(t, tenancy.CarriesFilter(cfg.Users[0].URLMap[0].URLPrefix[0],
		tenancy.MetricsFilterArg, tenancy.MetricsFilterPlaceholder))
	assert.True(t, tenancy.CarriesFilter(cfg.Users[0].URLMap[1].URLPrefix[0],
		tenancy.LogsFilterArg, tenancy.LogsFilterPlaceholder))
}

// An estate without a trace store never has to answer the question.
func TestNoTraceBackendNeedsNoOptIn(t *testing.T) {
	c := base()
	c.TracesBackend = ""
	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	assert.Len(t, cfg.Users[0].URLMap, 2)
}

// A logs filter has to be one VictoriaLogs will parse, and the shape
// that looks right does not parse.
//
// VictoriaLogs reads an `extra_stream_filters` value beginning with `{"`
// as its JSON object form, `{"field":"value"}`. Every filter this
// package renders begins with `{"`, because a log field name carries
// dots and a slash and has to be quoted — so without the `_stream:`
// prefix the value reaches a JSON parser and comes back as `cannot parse
// JSON: missing ':' after object key`. That is a 400 on every log query
// the principal makes.
func TestLogsFilterIsParseableLogsQLAndNotJSON(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(twoGrants())
	require.NoError(t, err)

	for _, f := range claim.LogsExtraStreamFilters {
		assert.True(t, strings.HasPrefix(f, "_stream:{"),
			"a stream filter has to say so: %s", f)
		assert.False(t, strings.HasPrefix(f, `{"`),
			`a value beginning with {" is read as the JSON object form and fails to parse: %s`, f)
	}
}

// An empty filter list is not a narrow grant. It is no grant at all, and
// it also hands the caller the argument.
//
// vmauth expands a placeholder to the claim's VALUES, so an empty list
// expands to nothing and the query argument vanishes from the forwarded
// request. The only thing stopping a caller sending its own
// `extra_filters` is that such an argument CLASHES with one the route
// already set — and with the route's argument gone there is no clash, so
// the caller's filter is used instead. Refused here, because at the far
// end it looks like a successful query.
func TestClaimNeverRendersAnEmptyFilterList(t *testing.T) {
	c := base()
	for _, p := range []tenancy.Principal{c.Principals[0], twoGrants()} {
		claim, err := c.RenderClaim(p)
		require.NoError(t, err)
		assert.NotEmpty(t, claim.MetricsExtraFilters,
			"an empty list removes the argument rather than denying anything")
		assert.NotEmpty(t, claim.LogsExtraStreamFilters)
		for _, f := range append(append([]string{}, claim.MetricsExtraFilters...), claim.LogsExtraStreamFilters...) {
			assert.NotEmpty(t, f, "an empty filter string is the same hole spelled differently")
		}
	}
}
