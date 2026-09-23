package tenancy_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/tenancy"
)

func base() tenancy.Config {
	return tenancy.Config{
		ClaimName:      "groups",
		MetricsBackend: "http://metrics.example:8428",
		LogsBackend:    "http://logs.example:9428",
		TracesBackend:  "http://traces.example:10428",
		Principals: []tenancy.Principal{{
			Group:  "example:k8s:viewer",
			Grants: []tenancy.Grant{{Env: "devel", AllTenants: true}},
		}},
	}
}

func TestClaimRendersOneFilterPerGrant(t *testing.T) {
	c := base()
	p := tenancy.Principal{
		Group: "example:dms:deployer",
		Grants: []tenancy.Grant{
			// Deliberately unsorted, and the tenants too: the render must
			// be stable, or every unrelated change produces a diff and
			// people stop reading them.
			{Env: "prod", Tenants: []string{"url-shortener", "dms"}},
			{Env: "devel", Tenants: []string{"dms"}},
		},
	}

	claim, err := c.RenderClaim(p)
	require.NoError(t, err)

	assert.Equal(t, []string{
		`{env="devel",tenant=~"^(dms)$"}`,
		`{env="prod",tenant=~"^(dms|url-shortener)$"}`,
	}, claim.MetricsExtraFilters)

	assert.Equal(t, claim.MetricsExtraFilters, claim.LogsExtraStreamFilters,
		"the two filter sets must stay identical; a difference between them is a difference between what a token says and what the proxy enforces")
}

func TestAllTenantsOmitsTheTenantMatcher(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(c.Principals[0])
	require.NoError(t, err)

	require.Len(t, claim.MetricsExtraFilters, 1)
	assert.Equal(t, `{env="devel"}`, claim.MetricsExtraFilters[0])
	assert.NotContains(t, claim.MetricsExtraFilters[0], "tenant",
		"a match-all tenant matcher would drop series that carry no tenant label at all, which mid-rollout is exactly the data someone is looking for")
}

func TestLabelKeysAreInputs(t *testing.T) {
	c := base()
	c.TenantLabel = "owner"
	c.EnvLabel = "stage"
	claim, err := c.RenderClaim(tenancy.Principal{
		Group:  "g",
		Grants: []tenancy.Grant{{Env: "devel", Tenants: []string{"dms"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, `{stage="devel",owner=~"^(dms)$"}`, claim.MetricsExtraFilters[0])
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
