package tenancy_test

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/truvity/observability/pkg/tenancy"
)

func base() tenancy.Config {
	return tenancy.Config{
		ClaimName:      "groups",
		Audience:       "example-observability-client",
		MetricsBackend: "http://metrics.example:8428",
		LogsBackend:    "http://logs.example:9428",
		TracesBackend:  "http://traces.example:10428",
		Principals: []tenancy.Principal{{
			Group:  "example:k8s:viewer",
			Grants: []tenancy.Grant{{Cluster: "example-cluster", AllNamespaces: true}},
		}},
	}
}

func twoGrants() tenancy.Principal {
	return tenancy.Principal{
		Group: "example:app:deployer",
		Grants: []tenancy.Grant{
			// Deliberately unsorted, and the namespaces too: the render
			// must be stable, or every unrelated change produces a diff and
			// people stop reading them.
			{Cluster: "other-cluster", Namespaces: []string{"other-app", "example-app"}},
			{Cluster: "example-cluster", Namespaces: []string{"example-app"}},
		},
	}
}

// The vocabulary is OpenTelemetry's, spelled the way each store can carry
// it, and it is pinned here as literal strings on purpose: these are the
// names charts/observability-emitters stamps, so a change to a constant
// is a change to what every filter selects on and has to be made in both
// places at once.
func TestTheVocabularyIsOpenTelemetrys(t *testing.T) {
	assert.Equal(t, "k8s_cluster_name", tenancy.DefaultClusterLabel)
	assert.Equal(t, "k8s_namespace_name", tenancy.DefaultNamespaceLabel)
	assert.Equal(t, "deployment_environment_name", tenancy.EnvironmentLabel)

	assert.Equal(t, "k8s.cluster.name", tenancy.DefaultLogsClusterField)
	assert.Equal(t, "kubernetes.pod_namespace", tenancy.DefaultLogsNamespaceField,
		"the log-path namespace key is the container-log agent's native spelling, because it cannot rename a field and the gateway can")
	assert.Equal(t, "deployment.environment.name", tenancy.EnvironmentAttribute)

	assert.Equal(t, "k8s.cluster.name", tenancy.TracesClusterAttribute)
	assert.Equal(t, "k8s.namespace.name", tenancy.TracesNamespaceAttribute)
}

func TestClaimRendersOneMetricsFilterPerGrant(t *testing.T) {
	claim, err := base().RenderClaim(twoGrants())
	require.NoError(t, err)

	assert.Equal(t, []string{
		`{k8s_cluster_name="example-cluster",k8s_namespace_name=~"^(example-app)$"}`,
		`{k8s_cluster_name="other-cluster",k8s_namespace_name=~"^(example-app|other-app)$"}`,
	}, claim.MetricsExtraFilters)
}

// One stream filter for the whole principal, with the grants as
// alternatives inside it.
//
// Not one per grant: VictoriaLogs AND-s every `extra_stream_filters`
// argument into the query as its own global constraint, so two entries
// naming two clusters intersect in nothing and the principal with the
// most access is the one who gets an empty screen.
func TestClaimRendersOneLogsFilterForEveryGrant(t *testing.T) {
	claim, err := base().RenderClaim(twoGrants())
	require.NoError(t, err)

	require.Len(t, claim.LogsExtraStreamFilters, 1,
		"a second entry would be AND-ed with the first, not OR-ed: %v", claim.LogsExtraStreamFilters)
	assert.Equal(t,
		`_stream:{"k8s.cluster.name"="example-cluster","kubernetes.pod_namespace"=~"^(example-app)$"`+
			` or "k8s.cluster.name"="other-cluster","kubernetes.pod_namespace"=~"^(example-app|other-app)$"}`,
		claim.LogsExtraStreamFilters[0])
}

// The two paths name different fields and must. The metrics store carries
// the namespace in a label that cannot hold a dot; the log store carries
// it in the field the container-log agent natively writes, which is
// neither the label nor the OpenTelemetry attribute. A logs filter naming
// the metrics label selects a field the streams do not have, which
// returns an empty result rather than an error — the one failure this
// repository exists to prevent.
func TestLogsFiltersNeverNameTheMetricsFields(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(twoGrants())
	require.NoError(t, err)

	require.NotEmpty(t, claim.LogsExtraStreamFilters)
	for _, f := range claim.LogsExtraStreamFilters {
		assert.NotContains(t, f, tenancy.DefaultNamespaceLabel,
			"the logs filter names the METRICS namespace label; on the log path no such field exists, and the query would return nothing with no error at all: %s", f)
		assert.NotContains(t, f, tenancy.DefaultClusterLabel, "same, for the cluster: %s", f)
		assert.Contains(t, f, strconv.Quote(tenancy.DefaultLogsNamespaceField),
			"the logs filter does not name the field the log agent writes: %s", f)
		assert.Contains(t, f, strconv.Quote(tenancy.DefaultLogsClusterField))
	}

	assert.NotEqual(t, claim.MetricsExtraFilters, claim.LogsExtraStreamFilters,
		"the two paths name different fields and combine their entries differently; identical lists mean one of them is not being rendered")
}

// The tier is descriptive and never a key: two clusters can share it, so
// a filter on it would select both. No filter this package renders may
// mention it.
func TestTheEnvironmentTierIsNeverAKey(t *testing.T) {
	claim, err := base().RenderClaim(twoGrants())
	require.NoError(t, err)
	for _, f := range append(append([]string{}, claim.MetricsExtraFilters...), claim.LogsExtraStreamFilters...) {
		assert.NotContains(t, f, tenancy.EnvironmentLabel, "a filter on the tier selects every cluster that shares it: %s", f)
		assert.NotContains(t, f, tenancy.EnvironmentAttribute, "a filter on the tier selects every cluster that shares it: %s", f)
	}
}

func TestAllNamespacesOmitsTheNamespaceMatcher(t *testing.T) {
	c := base()
	claim, err := c.RenderClaim(c.Principals[0])
	require.NoError(t, err)

	require.Len(t, claim.MetricsExtraFilters, 1)
	assert.Equal(t, `{k8s_cluster_name="example-cluster"}`, claim.MetricsExtraFilters[0])
	assert.NotContains(t, claim.MetricsExtraFilters[0], tenancy.DefaultNamespaceLabel,
		"a match-all namespace matcher would drop series that carry no namespace label at all — the node-level series — which for a principal allowed the whole cluster is exactly the data they came for")

	require.Len(t, claim.LogsExtraStreamFilters, 1)
	assert.Equal(t, `_stream:{"k8s.cluster.name"="example-cluster"}`, claim.LogsExtraStreamFilters[0])
}

// The keys are inputs with defaults, for an estate whose collectors were
// not built here. Setting them has to reach the filter.
func TestKeysAreInputs(t *testing.T) {
	c := base()
	c.ClusterLabel = "cell"
	c.NamespaceLabel = "ns"
	c.LogsClusterField = "k8s.cell"
	c.LogsNamespaceField = "kubernetes.namespace_name"
	claim, err := c.RenderClaim(tenancy.Principal{
		Group:  "g",
		Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, `{cell="example-cluster",ns=~"^(example-app)$"}`, claim.MetricsExtraFilters[0])
	assert.Equal(t, `_stream:{"k8s.cell"="example-cluster","kubernetes.namespace_name"=~"^(example-app)$"}`,
		claim.LogsExtraStreamFilters[0])
}

// And unset, they are the OpenTelemetry-derived defaults — the names the
// emitters chart stamps — rather than a refusal. The refusal that used to
// live here guarded a field derived from an estate's own namespace label,
// which every estate spelled differently; the key is the namespace's
// name now, which every estate spells the same way.
func TestUnsetKeysAreTheDefaults(t *testing.T) {
	c := base()
	require.Empty(t, c.ClusterLabel+c.NamespaceLabel+c.LogsClusterField+c.LogsNamespaceField)
	require.NoError(t, c.Validate())

	claim, err := c.RenderClaim(twoGrants())
	require.NoError(t, err)
	assert.Contains(t, claim.MetricsExtraFilters[0], tenancy.DefaultClusterLabel+"=")
	assert.Contains(t, claim.MetricsExtraFilters[0], tenancy.DefaultNamespaceLabel+"=~")
	assert.Contains(t, claim.LogsExtraStreamFilters[0], strconv.Quote(tenancy.DefaultLogsClusterField)+"=")
	assert.Contains(t, claim.LogsExtraStreamFilters[0], strconv.Quote(tenancy.DefaultLogsNamespaceField)+"=~")
}

// A metrics label key is held to the Prometheus label-name shape, which
// is not the namespace-name shape: the defaults carry underscores.
func TestLabelKeysAreHeldToThePrometheusShape(t *testing.T) {
	for _, key := range []string{"k8s_namespace_name", "namespace", "_ns", "Ns2"} {
		c := base()
		c.NamespaceLabel = key
		assert.NoError(t, c.Validate(), "%q is a Prometheus label name", key)
	}
	for what, key := range map[string]string{
		"a dot":       "k8s.namespace.name",
		"a dash":      "k8s-namespace",
		"a brace":     "ns}",
		"a leading 0": "0ns",
		"a space":     "ns x",
	} {
		t.Run(what, func(t *testing.T) {
			c := base()
			c.NamespaceLabel = key
			err := c.Validate()
			require.Error(t, err, "%q is not a label a series can carry", key)
			assert.Contains(t, err.Error(), "not a Prometheus label name")
		})
	}
}

// The log field names admit the shape a real one has — dots — and refuse
// everything that is stream-filter syntax.
func TestRealLogFieldNamesAreAccepted(t *testing.T) {
	for _, field := range []string{
		"kubernetes.pod_namespace",
		"k8s.namespace.name",
		"kubernetes.namespace_name",
		"resource_attr_service.namespace",
		"a-b_c.d/e",
	} {
		t.Run(field, func(t *testing.T) {
			c := base()
			c.LogsNamespaceField = field
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app"}}},
			}}
			require.NoError(t, c.Validate())

			claim, err := c.RenderClaim(c.Principals[0])
			require.NoError(t, err)
			assert.Contains(t, claim.LogsExtraStreamFilters[0], strconv.Quote(field))
		})
	}
}

// The same security property as TestHostileNamesAreRefused, on the field
// surface: the name is interpolated into the same filter, so a name that
// is not a plain field name must never reach the renderer.
func TestHostileLogFieldNamesAreRefused(t *testing.T) {
	hostile := map[string]string{
		`quote closes the name`:     `ns"=~"^(.*)$"} or {"k8s.cluster.name`,
		`brace ends the filter`:     `ns"} or {"x`,
		`comma opens a second term`: `ns,x`,
		`equals ends the name`:      `ns=x`,
		`alternation`:               `ns|other`,
		`backslash`:                 `ns\x`,
		`space`:                     `ns field`,
		`colon is logsql syntax`:    `ns:x`,
		`leading dot`:               `.ns`,
		`leading slash`:             `/ns`,
		`leading dash`:              `-ns`,
		`newline`:                   "ns\nx",
		`a lone glue character`:     `.`,
		`regexp that matches all`:   `.*`,
		`backtick`:                  "ns`x",
		`single quote`:              `ns'x`,
		`parenthesis`:               `ns(x)`,
		`tilde`:                     `ns~x`,
	}

	for what, field := range hostile {
		t.Run("namespaceField/"+what, func(t *testing.T) {
			c := base()
			c.LogsNamespaceField = field
			err := c.Validate()
			require.Error(t, err, "a log field named %q must be refused, not escaped", field)
			assert.Contains(t, err.Error(), "not a valid log field name")

			_, renderErr := c.RenderClaim(c.Principals[0])
			require.Error(t, renderErr, "and it must not survive as far as a rendered filter")
		})

		t.Run("clusterField/"+what, func(t *testing.T) {
			c := base()
			c.LogsClusterField = field
			require.Error(t, c.Validate(), "a log field named %q must be refused", field)
		})
	}
}

// The namespace-name rule is NOT loosened to make a field name fit. A
// field name may carry a dot and a slash; a namespace may not, because a
// namespace name goes inside the alternation where a `.` is a
// regular-expression metacharacter.
func TestTheFieldShapeDoesNotLoosenTheNamespaceShape(t *testing.T) {
	c := base()
	c.Principals = []tenancy.Principal{{
		Group:  "g",
		Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example.com/app"}}},
	}}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid name")
}

// One name for both dimensions is one dimension. Refused on both paths.
func TestTheTwoKeysMustDiffer(t *testing.T) {
	c := base()
	c.ClusterLabel = "k8s_namespace_name"
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clusterLabel and namespaceLabel are both")

	c = base()
	c.LogsClusterField = "kubernetes.pod_namespace"
	err = c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logsClusterField and logsNamespaceField are both")
}

// The refusal has to teach, because the failure it prevents teaches
// nothing: an empty result carries no message at all.
func TestTheRefusalExplainsTheModel(t *testing.T) {
	c := base()
	c.Principals[0].Grants = []tenancy.Grant{{Cluster: "example-cluster"}}
	err := c.Validate()
	require.Error(t, err)
	for _, phrase := range []string{
		"refused rather than read as",
		"project that expands to no namespaces",
	} {
		assert.Contains(t, err.Error(), phrase,
			"the refusal should leave the reader knowing that a grant is namespaces on a cluster and that a project is a derivation")
	}

	c = base()
	c.Principals[0].Grants = []tenancy.Grant{{Cluster: "", AllNamespaces: true}}
	err = c.Validate()
	require.Error(t, err)
	for _, phrase := range []string{
		"cluster is empty",
		"half of the scoping key",
		"empty result rather than an error",
	} {
		assert.Contains(t, err.Error(), phrase)
	}

	c = base()
	c.LogsNamespaceField = "ns|x"
	err = c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container-log agent natively writes",
		"the refusal should say where the default comes from, because that is the field the reader has to match")
}

// The security property this package exists for: a name is interpolated
// into a regular expression, so a name that is not a plain name must never
// reach the renderer.
func TestHostileNamesAreRefused(t *testing.T) {
	hostile := []string{
		"example-app|other-app", // alternation: would grant a second namespace
		".*",                    // would grant every namespace
		"example-app)|(.*",      // would close the group and open a new one
		"example-app$|^",        // would defeat the anchors
		"DMS",                   // uppercase is not the DNS-label shape
		"example-app ",          // trailing space
		"-example-app",          // must start alphanumeric
		"",                      // empty
	}

	for _, name := range hostile {
		t.Run("namespace/"+name, func(t *testing.T) {
			c := base()
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{name}}},
			}}
			err := c.Validate()
			require.Error(t, err, "a namespace named %q must be refused, not escaped", name)
			assert.Contains(t, err.Error(), "not a valid name")
		})

		t.Run("cluster/"+name, func(t *testing.T) {
			c := base()
			c.Principals = []tenancy.Principal{{
				Group:  "g",
				Grants: []tenancy.Grant{{Cluster: name, Namespaces: []string{"example-app"}}},
			}}
			require.Error(t, c.Validate(), "a cluster named %q must be refused", name)
		})
	}
}

// Whatever a hostile name would have tried to do, it must not survive as
// far as a rendered filter.
func TestHostileNameNeverReachesAFilter(t *testing.T) {
	c := base()
	_, err := c.RenderClaim(tenancy.Principal{
		Group:  "g",
		Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app)|(.*"}}},
	})
	require.Error(t, err)
}

func TestRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*tenancy.Config)
		want   string
	}{
		"empty namespace list is not everything": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Cluster: "example-cluster"}}
			},
			want: "refused rather than read as",
		},
		"allNamespaces and a list together": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Cluster: "example-cluster", AllNamespaces: true, Namespaces: []string{"example-app"}}}
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
		"the same cluster granted twice to one principal": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{
					{Cluster: "example-cluster", Namespaces: []string{"example-app"}},
					{Cluster: "example-cluster", Namespaces: []string{"other-app"}},
				}
			},
			want: "granted twice",
		},
		"a namespace listed twice": {
			mutate: func(c *tenancy.Config) {
				c.Principals[0].Grants = []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app", "example-app"}}}
			},
			want: "listed twice",
		},
		"no claim name": {
			mutate: func(c *tenancy.Config) { c.ClaimName = "" },
			want:   "claimName is empty",
		},
		"an audience that is not an identifier at all": {
			mutate: func(c *tenancy.Config) { c.Audience = "example-observability-client\n" },
			want:   "not an identifier",
		},
		"the groups claim named `aud`": {
			mutate: func(c *tenancy.Config) { c.ClaimName = "aud" },
			want:   "would overwrite the other",
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
			{Cluster: "example-cluster", Namespaces: []string{"BAD"}},
			{Cluster: "PROD", Namespaces: []string{"example-app"}},
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
		Group:  "example:example-app:deployer",
		Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app"}}},
	})

	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)
	require.Len(t, cfg.Users, 2)

	viewer, deployer := cfg.Users[0], cfg.Users[1]

	// Two claims, answering two different questions. The group says which
	// principal a token is; the audience says the token was minted for
	// this proxy at all — which vmauth checks nowhere else, because it
	// validates expiry and issuer and stops.
	assert.Equal(t, map[string]string{
		"groups": "^(example:k8s:viewer)$",
		"aud":    "^(example-observability-client)$",
	}, viewer.JWT.MatchClaims)
	assert.Equal(t, viewer.JWT.MatchClaims["aud"], deployer.JWT.MatchClaims["aud"],
		"the audience is the proxy's, not a principal's: every user entry carries the same pin")
	assert.Equal(t, "https://issuer.example", viewer.JWT.OIDC)

	// The whole point, asserted: two principals, two different reaches.
	assert.Equal(t, []string{`{k8s_cluster_name="example-cluster"}`}, viewer.JWT.DefaultVMAccess.MetricsExtraFilters)
	assert.Equal(t, []string{`{k8s_cluster_name="example-cluster",k8s_namespace_name=~"^(example-app)$"}`}, deployer.JWT.DefaultVMAccess.MetricsExtraFilters)

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

	t.Run("audience", func(t *testing.T) {
		c := base()
		c.Audience = ""
		_, err := c.RenderVMAuth("https://issuer.example")
		require.Error(t, err)
		// The message is the whole of the defence for somebody reading
		// it at two in the morning: what vmauth checks, what it does
		// not, and what follows from that.
		assert.Contains(t, err.Error(), "audience is empty")
		assert.Contains(t, err.Error(), "ISSUER")
		assert.Contains(t, err.Error(), "whatever client it was minted for")
	})

	t.Run("an invalid config never renders", func(t *testing.T) {
		c := base()
		c.Principals[0].Grants = []tenancy.Grant{{Cluster: "example-cluster"}}
		_, err := c.RenderVMAuth("https://issuer.example")
		require.Error(t, err)
	})

	t.Run("a hostile key never renders", func(t *testing.T) {
		c := base()
		c.LogsNamespaceField = `ns"} or {"x`
		_, err := c.RenderVMAuth("https://issuer.example")
		require.Error(t, err)
	})
}

// Every value this package puts in `match_claims`, read back the way the
// proxy will read it.
//
// vmauth compiles a `match_claims` value as a REGULAR EXPRESSION —
// `^(?:` + value + `)$` since v1.152.0, the release that fixed
// GHSA-f99m-22fh-qw96, and bare before it. Neither value in that map is
// this package's own: a group is whatever a derivation produced, an
// audience is whatever an issuer assigned. So both are escaped and
// anchored, and both are asserted here under both compilations: the one
// the version floor promises, and the one an estate on an older proxy
// would get.
func TestEveryMatchClaimValueMeansOnlyItself(t *testing.T) {
	cases := []struct {
		name   string
		group  string
		aud    string
		admits []string // strings the two values must NOT admit
	}{
		{
			name:   "ordinary names",
			group:  "example:k8s:viewer",
			aud:    "example-observability-client",
			admits: []string{"example:k8s:viewer-admin", "xexample:k8s:viewer", "example-observability-client-for-something-else"},
		},
		{
			name:   "a client id and a group with dots in them",
			group:  "example.k8s.viewers",
			aud:    "123.apps.example-issuer",
			admits: []string{"examplexk8sxviewers", "123xappsxexample-issuer"},
		},
		{
			// The shape a derivation bug produces. `.*` pins nothing at
			// all unless it is escaped: `^(?:.*)$` matches every token
			// there is.
			name:   "a group and an audience written as patterns",
			group:  ".*",
			aud:    "example-.*",
			admits: []string{"any-group-at-all", "example-anything", "example-"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			c.TracesBackend = "" // the trace route has its own refusal; not what this test is about
			c.Audience = tc.aud
			c.Principals = []tenancy.Principal{{
				Group:  tc.group,
				Grants: []tenancy.Grant{{Cluster: "example-cluster", AllNamespaces: true}},
			}}

			cfg, err := c.RenderVMAuth("https://issuer.example")
			require.NoError(t, err)

			rendered := cfg.Users[0].JWT.MatchClaims
			require.Len(t, rendered, 2, "the group and the audience, and nothing else")

			for claim, want := range map[string]string{c.ClaimName: tc.group, tenancy.AudienceClaim: tc.aud} {
				value := rendered[claim]
				require.NotEmpty(t, value, "nothing was rendered for %q", claim)

				for name, compiled := range map[string]*regexp.Regexp{
					"as vmauth at the version floor compiles it": regexp.MustCompile("^(?:" + value + ")$"),
					"as a vmauth below the floor compiles it":    regexp.MustCompile(value),
				} {
					assert.True(t, compiled.MatchString(want),
						"%s, %s: the entry matches nothing, so the proxy admits nobody — fail-closed, and still wrong", claim, name)
					for _, other := range tc.admits {
						assert.False(t, compiled.MatchString(other),
							"%s, %s: %q is admitted by an entry that names %q", claim, name, other, want)
					}
				}
			}
		})
	}
}

// A client id is escaped, not refused — which is the opposite of what
// this package does with a cluster or a namespace name.
//
// The distinction is where the value comes from. A namespace name is the
// estate's own and a narrow shape costs it nothing. A client id is
// assigned by somebody else's issuer, and issuers mint ids with dots in
// them, so refusing a shape we do not control would lock an operator out
// of a component published for them to install. What is still refused is
// a value no issuer mints at all.
func TestAnAudienceIsEscapedRatherThanRefused(t *testing.T) {
	render := func(audience string) (tenancy.VMAuthConfig, error) {
		c := base()
		c.TracesBackend = ""
		c.Audience = audience
		return c.RenderVMAuth("https://issuer.example")
	}

	for _, id := range []string{
		"example-observability-client",
		"0f8fad5b-d9cb-469f-a165-70867728950e",  // an issuer that mints UUIDs
		"123456@example-project",                // an issuer that qualifies the id with a project
		"123456789.apps.example-issuer.example", // an issuer that mints a hostname-shaped id
		"EXAMPLE_CLIENT",
		"example:observability",
		"example-.*", // even this: it pins the literal, and nothing else
		"a|b",
	} {
		cfg, err := render(id)
		require.NoError(t, err, "a client id an issuer could mint was refused: %q", id)
		assert.Equal(t, "^("+regexp.QuoteMeta(id)+")$", cfg.Users[0].JWT.MatchClaims[tenancy.AudienceClaim])
	}

	for _, notAnID := range []string{
		"example-observability-client\n", // a file read with its trailing newline
		"example-client another-client",  // two ids in one string
		"\texample-observability-client", // a heredoc, indented
		" ",                              // and the one that renders a pin of nothing
	} {
		_, err := render(notAnID)
		require.Error(t, err, "a value no issuer mints rendered: %q", notAnID)
		assert.Contains(t, err.Error(), "not an identifier")
	}
}

// Rendering must not mutate its input: a caller that renders twice, or
// renders and then writes the config back out, must see what it passed in.
func TestRenderDoesNotMutateItsInput(t *testing.T) {
	c := base()
	c.Principals[0].Grants = []tenancy.Grant{
		{Cluster: "other-cluster", Namespaces: []string{"other-app", "example-app"}},
		{Cluster: "example-cluster", Namespaces: []string{"example-app"}},
	}

	_, err := c.RenderClaim(c.Principals[0])
	require.NoError(t, err)

	assert.Equal(t, "other-cluster", c.Principals[0].Grants[0].Cluster, "grant order changed under the caller")
	assert.Equal(t, []string{"other-app", "example-app"}, c.Principals[0].Grants[0].Namespaces, "namespace order changed under the caller")
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
// health check, and any query that asks about a single namespace.
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
	assert.False(t, tenancy.CarriesFilter("http://x:8428?"+arg+"={k8s_cluster_name=\"prod\"}"+ph, arg, ph),
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
// dots and has to be quoted — so without the `_stream:` prefix the value
// reaches a JSON parser and comes back as `cannot parse JSON: missing
// ':' after object key`. That is a 400 on every log query the principal
// makes.
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

// vmauth's own struct, not ours, decides where the default claim lives.
//
// This package once rendered `default_vm_access_claim` beside `url_map`,
// which reads perfectly well and is not a field vmauth's `UserInfo` has.
// The result was not a missing default: vmauth refused the whole file and
// exited, so the proxy never started at all.
//
//	cannot unmarshal AuthConfig data: yaml: unmarshal errors:
//	  field default_vm_access_claim not found in type main.UserInfo
//
// Nothing in this repository could have caught that, because every test
// compared our output against our own idea of the shape. It was found by
// running the rendered file against the binary, and this test is here so
// the next person moving these structs around does not have to.
func TestTheDefaultClaimIsNestedInsideTheTokenBlock(t *testing.T) {
	c := base()
	c.TracesBackend = "" // the trace route has its own refusal; not what this test is about
	cfg, err := c.RenderVMAuth("https://issuer.example")
	require.NoError(t, err)

	out, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var parsed struct {
		Users []struct {
			JWT map[string]any `yaml:"jwt"`
			// Deliberately typed as a catch-all: the assertion below is
			// that vmauth would find NOTHING here, whatever it is called.
			Rest map[string]any `yaml:",inline"`
		} `yaml:"users"`
	}
	require.NoError(t, yaml.Unmarshal(out, &parsed))
	require.Len(t, parsed.Users, 1)

	u := parsed.Users[0]
	assert.Contains(t, u.JWT, "default_vm_access_claim",
		"the default claim must sit inside the jwt block, where vmauth reads it")
	assert.NotContains(t, u.Rest, "default_vm_access_claim",
		"a default claim beside url_map is not a field vmauth has: it refuses to parse the file and the proxy does not start")
}

// The metric-metadata switch, and the three endpoints no switch admits.
//
// `/api/v1/metadata` carries every metric name, type and help in the
// store and takes no filter, so it is the same answer for every principal
// whatever their grant. That makes it an estate's decision rather than
// this package's, and the default is off.
//
// Its siblings are not a decision: `/status/active_queries` and
// `/status/top_queries` return other principals' query TEXT, and
// `/status/metric_names_stats` returns names with per-tenant counts. No
// field admits them, and the reason this test names them is that a
// wildcard on the metrics route once admitted all four together.
func TestMetricMetadataOptIn(t *testing.T) {
	t.Parallel()

	never := []string{
		"/prometheus/api/v1/status/active_queries",
		"/prometheus/api/v1/status/top_queries",
		"/prometheus/api/v1/status/metric_names_stats",
	}

	for _, tc := range []struct {
		what  string
		allow bool
		want  bool
	}{
		{"unset", false, false},
		{"set", true, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			t.Parallel()

			c := base()
			c.AllowUnfilteredTraceReads = true
			c.AllowUnfilteredMetricMetadata = tc.allow

			cfg, err := c.RenderVMAuth("https://issuer.example")
			require.NoError(t, err)
			require.NotEmpty(t, cfg.Users)

			var metrics []string

			for _, row := range cfg.Users[0].URLMap {
				if slices.ContainsFunc(row.SrcPaths, func(p string) bool {
					return strings.HasPrefix(p, "/prometheus/")
				}) {
					metrics = append(metrics, row.SrcPaths...)
				}
			}

			require.NotEmpty(t, metrics, "no metrics row rendered; this check cannot see the paths")

			assert.Equalf(t, tc.want, slices.Contains(metrics, tenancy.MetricMetadataPath),
				"with AllowUnfilteredMetricMetadata=%v the metrics route should%s carry %q",
				tc.allow, map[bool]string{true: "", false: " not"}[tc.want], tenancy.MetricMetadataPath)

			for _, n := range never {
				assert.NotContainsf(t, metrics, n,
					"%q is admitted by no field on Config: it leaks per-principal material, "+
						"unlike the metadata endpoint, whose leak is a bounded list", n)
			}

			// The package-level value is the safe set whatever a caller
			// asked for: it is shared by every Config in the process, so
			// withPath must copy rather than append in place.
			assert.NotContains(t, tenancy.MetricsReadPaths, tenancy.MetricMetadataPath,
				"MetricsReadPaths is shared and must stay the safe set")
		})
	}
}
