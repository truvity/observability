package statusbox

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var updateGolden = flag.Bool("update", false, "update the golden files this package's render tests compare against")

func testPublicPage() PublicPage {
	return PublicPage{
		Title: "Example status",
		Groups: []PublicGroup{
			{DisplayName: "API", Hosts: []CompanyHost{
				{Host: "api.example.com", Component: "api", Env: "prod"},
				{Host: "did.example.com", Component: "api", Env: "prod"},
			}},
			{DisplayName: "Sign-in", Hosts: []CompanyHost{
				{Host: "id.example.com", Component: "id", Env: "prod", StatusPath: "/realms/customer/.well-known/openid-configuration"},
			}},
		},
		StoragePath: "/data/example-prod.db",
	}
}

// The public page's whole render, byte for byte.
func TestRenderPublicPageGolden(t *testing.T) {
	got, err := RenderPublicPage(testPublicPage())
	require.NoError(t, err)

	path := filepath.Join("testdata", "public-page.golden.yaml")
	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "run 'go test ./pkg/statusbox -run TestRenderPublicPageGolden -update' and review the diff")
	require.Equal(t, string(want), got, "the public page render moved: run 'go test ./pkg/statusbox -run TestRenderPublicPageGolden -update' and review the diff")
}

// What a page customers read must never carry: a sign-in, an alert provider,
// a bearer, an alerts-read call or a platform group.
func TestRenderPublicPageIsNarrow(t *testing.T) {
	got, err := RenderPublicPage(testPublicPage())
	require.NoError(t, err)

	for _, absent := range []string{
		"security:", "alerting:", "alerts:", "headers:", "Authorization", "ALERT_URL_",
		"/api/v1/alerts", "deadman", "platform", "customer-facing", "· prod",
	} {
		assert.NotContains(t, got, absent)
	}

	var cfg gatusConfig
	require.NoError(t, yaml.Unmarshal([]byte(got), &cfg))
	require.Len(t, cfg.Endpoints, 3)

	byName := map[string]gatusEndpoint{}
	for _, e := range cfg.Endpoints {
		byName[e.Name] = e
	}

	assert.Equal(t, "API", byName["api.example.com"].Group)
	assert.Equal(t, "https://api.example.com", byName["api.example.com"].URL)
	assert.Equal(t, []string{lenientStatusCondition, certExpiryCondition}, byName["api.example.com"].Conditions)
	assert.Equal(t, "https://id.example.com/realms/customer/.well-known/openid-configuration", byName["id.example.com"].URL)
	assert.Equal(t, []string{statusCondition, certExpiryCondition}, byName["id.example.com"].Conditions)
	assert.Equal(t, companyProbeInterval, byName["id.example.com"].Interval)
	require.NotNil(t, cfg.UI)
	assert.Equal(t, gatusUI{Title: "Example status", Header: "Example status"}, *cfg.UI)
}

func TestRenderPublicPageWithoutATitleKeepsGatusDefaults(t *testing.T) {
	p := testPublicPage()
	p.Title = ""

	got, err := RenderPublicPage(p)
	require.NoError(t, err)
	assert.NotContains(t, got, "ui:")
}

// The combined page never grows a ui block: its render is unchanged.
func TestRenderGatusHasNoUIBlock(t *testing.T) {
	got, err := RenderGatus(testCatalogue())
	require.NoError(t, err)
	assert.NotContains(t, got, "ui:")
}

func TestRenderPublicPageRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*PublicPage)
		want string
	}{
		"no groups":       {func(p *PublicPage) { p.Groups = nil }, "no hosts at all"},
		"empty group":     {func(p *PublicPage) { p.Groups[0].Hosts = nil }, `group "API": no hosts`},
		"no display name": {func(p *PublicPage) { p.Groups[0].DisplayName = " " }, "Groups[0]: DisplayName is empty"},
		"empty host":      {func(p *PublicPage) { p.Groups[0].Hosts[0].Host = "" }, "a host is empty"},
		"wildcard":        {func(p *PublicPage) { p.Groups[0].Hosts[0].Host = "*.example.com" }, "is a wildcard"},
		"scheme":          {func(p *PublicPage) { p.Groups[0].Hosts[0].Host = "https://api.example.com" }, "not a bare hostname"},
		"port":            {func(p *PublicPage) { p.Groups[0].Hosts[0].Host = "api.example.com:8443" }, "not a bare hostname"},
		"duplicate":       {func(p *PublicPage) { p.Groups[1].Hosts[0].Host = "api.example.com" }, `is also listed under "API"`},
		"status path":     {func(p *PublicPage) { p.Groups[1].Hosts[0].StatusPath = "healthz" }, "does not start with /"},
		"storage":         {func(p *PublicPage) { p.StoragePath = "" }, "StoragePath is empty"},
		"title":           {func(p *PublicPage) { p.Title = "a\nb" }, "Title spans several lines"},
	} {
		p := testPublicPage()
		// Fresh slices: the cases mutate them.
		p.Groups = []PublicGroup{
			{DisplayName: p.Groups[0].DisplayName, Hosts: append([]CompanyHost(nil), p.Groups[0].Hosts...)},
			{DisplayName: p.Groups[1].DisplayName, Hosts: append([]CompanyHost(nil), p.Groups[1].Hosts...)},
		}
		tc.mut(&p)

		_, err := RenderPublicPage(p)
		if assert.Error(t, err, name) {
			assert.True(t, strings.Contains(err.Error(), tc.want), "%s: %v, want %q", name, err, tc.want)
		}
	}
}
