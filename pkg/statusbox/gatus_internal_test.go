package statusbox

// White-box tests for gatus.go: this file needs the unexported helpers
// (endpointNames, probeURL/probeConditions, alertVar, alertsReadURL) as
// well as RenderGatus itself, the same reason statusbox_internal_test.go
// is `package statusbox` rather than `package statusbox_test`.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func testCatalogue() Catalogue {
	return Catalogue{
		PlatformHosts: []string{"example.xyz"},
		Companies: []Company{
			{Code: "acme", DisplayName: "Acme Corp"},
		},
		AlertsRead:  AlertsRead{Host: "alerts.kernel.example.private", TokenEnvKey: "tok"},
		StoragePath: "/data/ops.db",
	}
}

func TestRenderGatusRefusesAnEmptyPlatformHostList(t *testing.T) {
	c := testCatalogue()
	c.PlatformHosts = nil
	c.Providers = DeadmanProviders{SlackKey: "s", PagerDutyKey: "p"}

	_, err := RenderGatus(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PlatformHosts is empty")
}

// TestRenderGatusCarriesEveryJob is the combined-page contract: one
// platform probe list, one business-host probe per company (grouped
// under its display name), one customer-facing alerts-read check per
// company, and the deadman alerts-read check — all as ORDINARY
// endpoints.
func TestRenderGatusCarriesEveryJob(t *testing.T) {
	c := testCatalogue()
	c.PlatformHosts = []string{"example.xyz", "billing.devel.example.xyz"}
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts:       []CompanyHost{{Host: "billing.devel.example.xyz", Env: "devel"}},
		},
	}
	c.Providers = DeadmanProviders{SlackKey: "slack", PagerDutyKey: "pd"}

	out, err := RenderGatus(c)
	require.NoError(t, err)

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	// 2 platform probes + 1 business host + 1 customer-facing + 1 deadman.
	require.Len(t, parsed.Endpoints, 5)
	assert.Equal(t, "/data/ops.db", parsed.Storage.Path)

	byName := map[string]gatusEndpoint{}
	for _, e := range parsed.Endpoints {
		byName[e.Name] = e
	}

	// Named after the host's own first DNS label plus its cluster, never
	// the company/project name.
	billing, ok := byName["billing · devel"]
	require.True(t, ok, "business host must be named '<first label> · <env>'")
	assert.Equal(t, "https://billing.devel.example.xyz", billing.URL)
	assert.Equal(t, "Acme Corp", billing.Group)
	assert.Contains(t, billing.Conditions, "[CERTIFICATE_EXPIRATION] > 240h")
	// No StatusPath, so the lenient fallback condition — never the
	// strict one — and the bare host, no path appended.
	assert.Contains(t, billing.Conditions, lenientStatusCondition)
	assert.NotContains(t, billing.Conditions, statusCondition)
	assert.Empty(t, billing.Headers, "an ordinary hostname probe carries no bearer header")

	signal, ok := byName["acme-customer-facing"]
	require.True(t, ok)
	assert.Equal(t, "Acme Corp", signal.Group)
	assert.Equal(t,
		"https://alerts.kernel.example.private/api/v1/alerts?match%5B%5D=%7Bcustomer_facing%3D%22true%22%2Ccompany%3D%22acme%22%7D",
		signal.URL)
	assert.Equal(t, "Bearer ${ALERT_URL_TOK}", signal.Headers["Authorization"])
	assert.Contains(t, signal.Conditions, alertsAbsentCondition)
	require.Len(t, signal.Alerts, 2)

	deadman, ok := byName["deadman"]
	require.True(t, ok)
	assert.Equal(t, "platform", deadman.Group)
	assert.Equal(t,
		"https://alerts.kernel.example.private/api/v1/alerts?match%5B%5D=%7Balertname%3D%22Watchdog%22%7D",
		deadman.URL)
	assert.Contains(t, deadman.Conditions, alertsPresentCondition)

	require.NotNil(t, parsed.Alerting)
	require.NotNil(t, parsed.Alerting.Slack)
	require.NotNil(t, parsed.Alerting.PagerDuty)
	assert.NotEqual(t, parsed.Alerting.Slack.WebhookURL, parsed.Alerting.PagerDuty.IntegrationKey,
		"the two channels must be two distinct secrets, not the same one referenced twice")
	assert.False(t, strings.HasPrefix(out, "#"), "a configured render carries no 'nobody is notified' comment")
}

// TestRenderGatusNamesEndpointsByHostLabelAndEnv: two hosts owned by the
// SAME company on two different clusters get two distinct names for
// free, because the label and the env both differ, with no counter
// involved at all.
func TestRenderGatusNamesEndpointsByHostLabelAndEnv(t *testing.T) {
	c := testCatalogue()
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts: []CompanyHost{
				{Host: "dms.devel.example.xyz", Env: "devel"},
				{Host: "dms.prod.example.xyz", Env: "prod"},
			},
		},
	}

	out, err := RenderGatus(c)
	require.NoError(t, err)

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	var names []string
	for _, e := range parsed.Endpoints {
		names = append(names, e.Name)
	}

	assert.Contains(t, names, "dms · devel")
	assert.Contains(t, names, "dms · prod")
	assert.NotContains(t, names, "dms")
	assert.NotContains(t, names, "dms (2)")
}

// TestRenderGatusNamesAreUniqueWithNoNumbering is the acceptance test
// against a realistic multi-host, multi-cluster company (dms's primary
// AND its ssi surface, each on two clusters, plus keycloak): every
// rendered endpoint name is unique and NONE carries a "(n)" suffix.
func TestRenderGatusNamesAreUniqueWithNoNumbering(t *testing.T) {
	c := testCatalogue()
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts: []CompanyHost{
				{Host: "dms.devel.example.xyz", Env: "devel"},
				{Host: "ssi.devel.example.xyz", Env: "devel"},
				{Host: "dms.prod.example.xyz", Env: "prod"},
				{Host: "ssi.prod.example.xyz", Env: "prod"},
				{Host: "keycloak.prod.example.xyz", Env: "prod"},
			},
		},
	}

	out, err := RenderGatus(c)
	require.NoError(t, err)

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	seen := map[string]bool{}
	for _, e := range parsed.Endpoints {
		require.False(t, seen[e.Name], "endpoint name %q rendered twice", e.Name)
		seen[e.Name] = true
		assert.NotContains(t, e.Name, "(", "endpoint name %q still carries the retired numbering scheme", e.Name)
	}

	assert.True(t, seen["dms · devel"])
	assert.True(t, seen["ssi · devel"])
	assert.True(t, seen["dms · prod"])
	assert.True(t, seen["ssi · prod"])
	assert.True(t, seen["keycloak · prod"])
}

// TestRenderGatusFallsBackToFullHostnameOnCollision is the escape hatch:
// Gatus refuses two endpoints sharing a (name, group) pair, so the rare
// case where two hosts share BOTH their first DNS label AND their env —
// distinguished only by a deeper subdomain — must fall back to the full
// hostname, for EVERY host in the collision, not just the second one
// seen.
func TestRenderGatusFallsBackToFullHostnameOnCollision(t *testing.T) {
	c := testCatalogue()
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts: []CompanyHost{
				{Host: "a.one.devel.example.xyz", Env: "devel"},
				{Host: "a.two.devel.example.xyz", Env: "devel"},
			},
		},
	}

	out, err := RenderGatus(c)
	require.NoError(t, err)

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	var names []string
	for _, e := range parsed.Endpoints {
		names = append(names, e.Name)
	}

	assert.Contains(t, names, "a.one.devel.example.xyz")
	assert.Contains(t, names, "a.two.devel.example.xyz")
	assert.NotContains(t, names, "a · devel")
}

// TestEndpointNames is endpointNames's own unit test, isolated from a
// full render.
func TestEndpointNames(t *testing.T) {
	t.Run("no collision", func(t *testing.T) {
		got := endpointNames([]CompanyHost{
			{Host: "dms.devel.example.xyz", Env: "devel"},
			{Host: "dms.prod.example.xyz", Env: "prod"},
		})
		assert.Equal(t, []string{"dms · devel", "dms · prod"}, got)
	})

	t.Run("collision falls back to the full hostname for both", func(t *testing.T) {
		got := endpointNames([]CompanyHost{
			{Host: "a.one.devel.example.xyz", Env: "devel"},
			{Host: "a.two.devel.example.xyz", Env: "devel"},
		})
		assert.Equal(t, []string{"a.one.devel.example.xyz", "a.two.devel.example.xyz"}, got)
	})
}

func TestFirstDNSLabel(t *testing.T) {
	assert.Equal(t, "dms", firstDNSLabel("dms.devel.example.xyz"))
	assert.Equal(t, "example", firstDNSLabel("example.xyz"))
	assert.Equal(t, "bare", firstDNSLabel("bare"))
}

// TestProbeURLAndConditions: a host with no StatusPath gets the lenient
// condition against the bare host; a host that set one gets the strict
// condition against host+path. Either way certExpiryCondition rides
// along.
func TestProbeURLAndConditions(t *testing.T) {
	t.Run("no StatusPath: lenient status, bare host", func(t *testing.T) {
		host := CompanyHost{Host: "dms.devel.example.xyz"}
		assert.Equal(t, "https://dms.devel.example.xyz", probeURL(host))
		assert.Equal(t, []string{lenientStatusCondition, certExpiryCondition}, probeConditions(host))
	})

	t.Run("StatusPath set: strict status, path appended", func(t *testing.T) {
		host := CompanyHost{
			Host:       "keycloak.prod.example.xyz",
			StatusPath: "/realms/customer/.well-known/openid-configuration",
		}
		assert.Equal(t, "https://keycloak.prod.example.xyz/realms/customer/.well-known/openid-configuration", probeURL(host))
		assert.Equal(t, []string{statusCondition, certExpiryCondition}, probeConditions(host))
	})
}

// TestRenderGatusWithNoProvidersConfigured: the render must still be a
// Config Gatus boots, with no alerts: refs and no alerting: stanza at
// all, plus a visible comment that nobody is notified yet.
func TestRenderGatusWithNoProvidersConfigured(t *testing.T) {
	c := testCatalogue()

	out, err := RenderGatus(c)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(out, "#"), "must carry a visible comment that nobody is notified:\n%s", out)
	assert.Contains(t, out, "no alert providers configured")

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))

	for _, e := range parsed.Endpoints {
		assert.Empty(t, e.Alerts, "endpoint %q: no alerts: refs without a configured provider", e.Name)
	}
	assert.Nil(t, parsed.Alerting, "no alerting: stanza at all when neither provider is configured")
}

func TestAlertVarUppercasesTheKey(t *testing.T) {
	assert.Equal(t, "${ALERT_URL_ACME_BRIDGE_TOKEN}", alertVar("acme_bridge_token"))
}

// testPublicCatalogue is testCatalogue with the Security a Public
// instance needs.
func testPublicCatalogue() Catalogue {
	c := testCatalogue()
	c.Security = &OIDCSecurity{
		IssuerURL:      "https://access.example.xyz",
		PublicHostname: "status.example.xyz",
		ClientID:       "status",
	}

	return c
}

// TestOpsAndOpsBreakglassShareEveryEndpoint is the invariant a
// deployment's two RenderGatus calls depend on: the public and
// breakglass instances render the SAME endpoints and alerting, differing
// ONLY in their security block.
func TestOpsAndOpsBreakglassShareEveryEndpoint(t *testing.T) {
	c := testPublicCatalogue()
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts:       []CompanyHost{{Host: "billing.devel.example.xyz", Env: "devel"}},
		},
	}

	public, err := RenderGatus(c)
	require.NoError(t, err)

	private := c
	private.Security = nil
	breakglass, err := RenderGatus(private)
	require.NoError(t, err)

	var parsedPrivate, parsedPublic gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(breakglass), &parsedPrivate))
	require.NoError(t, yaml.Unmarshal([]byte(public), &parsedPublic))

	assert.Nil(t, parsedPrivate.Security, "the breakglass instance carries no security block")
	require.NotNil(t, parsedPublic.Security, "the public instance carries the OIDC security block")
	assert.Equal(t, parsedPrivate.Endpoints, parsedPublic.Endpoints, "the two instances must probe the same things")
	assert.Equal(t, parsedPrivate.Storage, parsedPublic.Storage)
}

// TestOIDCSecurity pins the block Gatus's README documents: issuer-url,
// redirect-url (ending in /authorization-code/callback), client-id,
// client-secret as oidcClientSecretVar (never a literal), and the one
// scope Gatus needs. No allowed-subjects: OIDCSecurity has no field to
// set one with, which this test proves at the type level as much as the
// runtime one.
func TestOIDCSecurity(t *testing.T) {
	c := testPublicCatalogue()

	out, err := RenderGatus(c)
	require.NoError(t, err)

	var parsed gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(out), &parsed))
	require.NotNil(t, parsed.Security)
	require.NotNil(t, parsed.Security.OIDC)
	assert.Equal(t, "https://access.example.xyz", parsed.Security.OIDC.IssuerURL)
	assert.Equal(t, "https://status.example.xyz/authorization-code/callback", parsed.Security.OIDC.RedirectURL)
	assert.True(t, strings.HasSuffix(parsed.Security.OIDC.RedirectURL, "/authorization-code/callback"),
		"Gatus v5.37.0 requires redirect-url to end with /authorization-code/callback")
	assert.Equal(t, "status", parsed.Security.OIDC.ClientID)
	assert.Equal(t, oidcClientSecretVar, parsed.Security.OIDC.ClientSecret)
	assert.NotContains(t, parsed.Security.OIDC.ClientSecret, "test-", "the client secret must never be a literal value")
	assert.Equal(t, []string{"openid"}, parsed.Security.OIDC.Scopes)
}

func TestAlertsReadURLEscapesTheMatcher(t *testing.T) {
	got := alertsReadURL("alerts.kernel.example.private", `{alertname="Watchdog"}`)
	assert.Equal(t, "https://alerts.kernel.example.private/api/v1/alerts?match%5B%5D=%7Balertname%3D%22Watchdog%22%7D", got)
}

// TestRenderGatusIsDeterministic guards the golden-render property this
// package's own docs/statusbox.md expects: the same Catalogue produces
// byte-identical YAML, so a redeploy that changed nothing about the
// catalogue renders no diff.
func TestRenderGatusIsDeterministic(t *testing.T) {
	c := testCatalogue()
	c.PlatformHosts = []string{"example.xyz"}
	c.Companies = []Company{
		{
			Code:        "acme",
			DisplayName: "Acme Corp",
			Hosts: []CompanyHost{
				{Host: "b.example.xyz", Env: "devel"},
				{Host: "a.example.xyz", Env: "devel"},
			},
		},
	}

	a, err := RenderGatus(c)
	require.NoError(t, err)
	b, err := RenderGatus(c)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

// TestComponentIsNeverRendered: Component is a caller-side label; two
// Catalogues that differ only in it must render identical bytes.
func TestComponentIsNeverRendered(t *testing.T) {
	plain := testCatalogue()
	labelled := testCatalogue()

	labelled.Companies = append([]Company(nil), plain.Companies...)
	for i := range labelled.Companies {
		hosts := append([]CompanyHost(nil), labelled.Companies[i].Hosts...)
		for j := range hosts {
			hosts[j].Component = "component-" + hosts[j].Env
		}
		labelled.Companies[i].Hosts = hosts
	}

	a, err := RenderGatus(plain)
	require.NoError(t, err)
	b, err := RenderGatus(labelled)
	require.NoError(t, err)

	assert.Equal(t, a, b)
	assert.NotContains(t, b, "component-")
}
