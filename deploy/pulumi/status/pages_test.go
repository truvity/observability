package status

import (
	"log/slog"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
)

func pageHosts() []statusbox.CompanyHost {
	return []statusbox.CompanyHost{
		{Host: "api.dev.example.com", Component: "api", Env: "dev"},
		{Host: "api.example.com", Component: "api", Env: "prod"},
		{Host: "console.example.com", Component: "console", Env: "prod"},
		{Host: "id.example.com", Component: "id", Env: "prod", StatusPath: "/healthz"},
		{Host: "id.stage.example.com", Component: "id", Env: "stage"},
	}
}

func TestPublicGroupsNarrowsToOneEnvironmentAndTheListedServices(t *testing.T) {
	services := []Service{{Component: "id", DisplayName: "Sign-in"}, {Component: "api", DisplayName: "API"}, {Component: "absent", DisplayName: "Nowhere"}}

	assert.Equal(t, []statusbox.PublicGroup{
		{DisplayName: "Sign-in", Hosts: []statusbox.CompanyHost{{Host: "id.example.com", Component: "id", Env: "prod", StatusPath: "/healthz"}}},
		{DisplayName: "API", Hosts: []statusbox.CompanyHost{{Host: "api.example.com", Component: "api", Env: "prod"}}},
	}, PublicGroups(pageHosts(), "prod", services), "console is not listed; dev and stage hosts are another page's; an absent service renders no group")

	assert.Equal(t, []statusbox.PublicGroup{
		{DisplayName: "Sign-in", Hosts: []statusbox.CompanyHost{{Host: "id.stage.example.com", Component: "id", Env: "stage"}}},
	}, PublicGroups(pageHosts(), "stage", services))

	assert.Empty(t, PublicGroups(pageHosts(), "qa", services))
}

func withPages(in *Inputs) {
	in.EC2.TunnelTokenParameter = "/acme/status/tunnel-token"
	in.Pages = []Page{
		{
			Name: statusbox.PageInstanceName("acme", "prod"), Hostname: "status.example.com", Port: PagePort(0), Title: "Acme status",
			Groups: PublicGroups(pageHosts(), "prod", []Service{{Component: "api", DisplayName: "API"}}),
		},
		{
			Name: statusbox.PageInstanceName("acme", "stage"), Hostname: "status.example.net", Port: PagePort(1),
			Groups: PublicGroups(pageHosts(), "stage", []Service{{Component: "id", DisplayName: "Sign-in"}}),
		},
	}
}

// Each page is one more Public instance with its own config object, next to
// the ops instances, and needs the tunnel but not the OIDC secret.
func TestDeployRunsOneInstancePerPage(t *testing.T) {
	m := deployWithInputs(t, withPages)

	assert.Equal(t, []string{"status-config-acme-prod", "status-config-acme-stage", "status-config-ops-breakglass", "status-setup-script"},
		m.names("aws:s3/bucketObjectv2:BucketObjectv2"))

	cfg := m.input("aws:s3/bucketObjectv2:BucketObjectv2", "status-config-acme-prod", "content")
	assert.Contains(t, cfg, "url: https://api.example.com\n")
	assert.Contains(t, cfg, "path: /data/acme-prod.db")
	assert.Contains(t, cfg, "title: Acme status")
	assert.NotContains(t, cfg, "security:")
	assert.NotContains(t, cfg, "alerts.example.test", "no internal host on a public page")
	assert.NotContains(t, cfg, "api.dev.example.com")

	userData := m.input("aws:ec2/launchTemplate:LaunchTemplate", "status", "userData")
	require.NotEmpty(t, userData)
}

// A page without a public ops page still needs the tunnel token; the OIDC
// secret stays the combined page's alone.
func TestDeployPagesNeedTheTunnel(t *testing.T) {
	err := pulumi.RunErr(func(c *pulumi.Context) error {
		return Deploy(c, slog.New(slog.DiscardHandler), Inputs{Pages: []Page{{Name: "acme-prod"}}})
	}, pulumi.WithMocks("proj", "stack", &mocks{}))
	require.ErrorContains(t, err, "Pages are served through the tunnel")
}

func TestDeployRefusesAPageWithNothingToShow(t *testing.T) {
	_, err := pageInstances([]Page{{Name: "acme-prod", Hostname: "status.example.com", Port: PagePort(0)}})
	require.ErrorContains(t, err, "render page acme-prod (status.example.com)")
	require.ErrorContains(t, err, "no hosts at all")
}

func TestPagePortsStartAfterTheOpsPorts(t *testing.T) {
	assert.Greater(t, FirstPagePort, PublicPort)
	assert.Greater(t, FirstPagePort, PrivatePort)
	assert.Equal(t, FirstPagePort+2, PagePort(2))
	assert.Equal(t, "/data/acme-prod.db", PageStoragePath("acme-prod"))
}
