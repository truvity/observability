package status

import (
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-tailscale/sdk/go/tailscale"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/truvity/observability/pkg/statusbox"
)

func groups() []HostGroup {
	return []HostGroup{
		{Cluster: "dev", Hosts: []string{"b.example.com", "a.example.com", "*.dev.example.com"}},
		{Cluster: "dev", Hosts: []string{"a.example.com"}},
		{Cluster: "dev", Hosts: []string{"shop.dev.example.com", "*.shop.example.com"}, Company: "acme", Component: "shop"},
		{Cluster: "prod", Hosts: []string{"shop.example.com"}, Company: "acme", Component: "shop", StatusPath: "/healthz"},
	}
}

func TestPlatformHostsAreSortedDedupedAndWithoutWildcardsOrCompanyHosts(t *testing.T) {
	assert.Equal(t, []string{"a.example.com", "b.example.com"}, PlatformHosts(groups()))
}

func TestHostsByCompanyCarriesComponentClusterAndStatusPath(t *testing.T) {
	assert.Equal(t, map[string][]statusbox.CompanyHost{"acme": {
		{Host: "shop.dev.example.com", Component: "shop", Env: "dev"},
		{Host: "shop.example.com", Component: "shop", Env: "prod", StatusPath: "/healthz"},
	}}, HostsByCompany(groups()))
}

func TestPlatformAndCompanyHostsAreDisjoint(t *testing.T) {
	platform := PlatformHosts(groups())

	for _, hosts := range HostsByCompany(groups()) {
		for _, h := range hosts {
			assert.NotContains(t, platform, h.Host)
		}
	}
}

func TestIsWildcardHostname(t *testing.T) {
	assert.True(t, IsWildcardHostname("*.example.com"))
	assert.False(t, IsWildcardHostname("a.example.com"))
}

func catalogueInputs(public bool) CatalogueInputs {
	return CatalogueInputs{
		PlatformHosts:  PlatformHosts(groups()),
		ByCompany:      HostsByCompany(groups()),
		Entities:       []Entity{{Code: "acme", DisplayName: "Acme"}, {Code: "none", DisplayName: "None"}},
		AlertsReadHost: "alerts.example.test",
		DeadmanChannel: "#deadman",
		Public:         public,
		PublicHostname: "status.example.com",
		OIDC:           OIDC{IssuerURL: "https://issuer.example.com", ClientID: "status"},
	}
}

func TestOpsCatalogueMapsTheInputs(t *testing.T) {
	c := OpsCatalogue(catalogueInputs(false))

	assert.Equal(t, []string{"a.example.com", "b.example.com"}, c.PlatformHosts)
	require.Len(t, c.Companies, 2)
	assert.Equal(t, "acme", c.Companies[0].Code)
	assert.Len(t, c.Companies[0].Hosts, 2)
	assert.Equal(t, statusbox.AlertsRead{Host: "alerts.example.test", TokenEnvKey: AlertsReadTokenKey}, c.AlertsRead)
	assert.Equal(t, statusbox.DeadmanProviders{}, c.Providers, "the company signals page nobody")
	require.NotNil(t, c.Deadman.Post)
	assert.Equal(t, statusbox.ChatPost{URL: "https://slack.com/api/chat.postMessage", TokenEnvKey: DeadmanSlackTokenKey, Channel: "#deadman"}, *c.Deadman.Post)
	assert.True(t, c.Deadman.AlertmanagerWatchdog)
	assert.Equal(t, "/data/ops.db", c.StoragePath)
	assert.Nil(t, c.Security)
}

func TestOpsCatalogueSecurity(t *testing.T) {
	sec := OpsCatalogue(catalogueInputs(true)).Security
	require.NotNil(t, sec)
	assert.Equal(t, statusbox.OIDCSecurity{IssuerURL: "https://issuer.example.com", PublicHostname: "status.example.com", ClientID: "status"}, *sec)

	noHost := catalogueInputs(true)
	noHost.PublicHostname = ""
	assert.Nil(t, OpsCatalogue(noHost).Security, "no public hostname: even a public request has no OIDC block")
}

// Two instances that both carried the deadman provider would page twice, and
// the public one dies with the OIDC issuer.
func TestOnlyTheBreakglassInstancePages(t *testing.T) {
	private, err := statusbox.RenderGatus(OpsCatalogue(catalogueInputs(false)))
	require.NoError(t, err)
	public, err := statusbox.RenderGatus(OpsCatalogue(catalogueInputs(true)))
	require.NoError(t, err)

	for _, want := range []string{"name: deadman", "name: alertmanager-watchdog", "type: custom", "Bearer ${ALERT_URL_DEADMAN_SLACK_TOKEN}", "#deadman"} {
		assert.Contains(t, private, want)
	}

	assert.NotContains(t, public, "alerting:")
	assert.NotContains(t, public, "type: custom")
	assert.Contains(t, public, "name: alertmanager-watchdog", "but it still shows the checks")
}

func TestRenderedConfigsAreDeterministic(t *testing.T) {
	a, err := statusbox.RenderGatus(OpsCatalogue(catalogueInputs(false)))
	require.NoError(t, err)
	b, err := statusbox.RenderGatus(OpsCatalogue(catalogueInputs(false)))
	require.NoError(t, err)

	assert.Equal(t, a, b)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(a), &parsed))
}

func TestRenderRefusesAnEmptyHostList(t *testing.T) {
	in := catalogueInputs(false)
	in.PlatformHosts = nil
	in.ByCompany = nil

	_, err := statusbox.RenderGatus(OpsCatalogue(in))
	require.Error(t, err)
}

func deployWith(t *testing.T, public bool) *mocks {
	t.Helper()

	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		box, err := aws.NewProvider(c, "box", &aws.ProviderArgs{})
		if err != nil {
			return err
		}

		ts, err := tailscale.NewProvider(c, "ts", &tailscale.ProviderArgs{})
		if err != nil {
			return err
		}

		in := Inputs{
			BoxProvider: box, TailscaleProvider: ts,
			Version: "v0.7.0", AvailabilityZone: "eu-west-3a", Hostname: "statusbox", TailscaleTag: "tag:statusbox",
			PlatformHosts: PlatformHosts(groups()), ByCompany: HostsByCompany(groups()),
			Entities: []Entity{{Code: "acme", DisplayName: "Acme"}}, AlertsReadHost: "alerts.example.test", DeadmanChannel: "#deadman",
			AlertsReadToken: pulumi.String("read"), DeadmanSlackToken: pulumi.String("slack"),
		}
		if public {
			in.PublicHostname = "status.example.com"
			in.OIDC = OIDC{IssuerURL: "https://issuer.example.com", ClientID: "status"}
			in.OIDCClientSecret = pulumi.String("secret")
			in.TunnelToken = pulumi.String("tunnel")
		}

		return Deploy(c, slog.New(slog.DiscardHandler), in)
	}, pulumi.WithMocks("proj", "stack", m)))

	return m
}

func TestDeployCreatesTheKeyAndTheBox(t *testing.T) {
	for _, public := range []bool{false, true} {
		m := deployWith(t, public)

		assert.Equal(t, []string{"status-box"}, m.names("tailscale:index/tailnetKey:TailnetKey"))
		assert.Equal(t, "tag:statusbox", m.tags("status-box"))
		assert.NotEmpty(t, m.names("aws:lightsail/instance:Instance"))
	}
}

func TestPublicPageNeedsItsSecrets(t *testing.T) {
	err := pulumi.RunErr(func(c *pulumi.Context) error {
		return Deploy(c, slog.New(slog.DiscardHandler), Inputs{PublicHostname: "status.example.com"})
	}, pulumi.WithMocks("proj", "stack", &mocks{}))
	require.ErrorContains(t, err, "needs OIDCClientSecret and TunnelToken")
}

type (
	recorded struct {
		typ, name string
		inputs    resource.PropertyMap
	}

	mocks struct {
		mu        sync.Mutex
		resources []recorded
	}
)

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources = append(m.resources, recorded{args.TypeToken, args.Name, args.Inputs})

	return args.Name + "-id", args.Inputs.Copy(), nil
}

func (m *mocks) Call(_ pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *mocks) names(typ string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string

	for _, r := range m.resources {
		if r.typ == typ {
			out = append(out, r.name)
		}
	}

	slices.Sort(out)

	return out
}

func (m *mocks) tags(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.resources {
		if r.name == name && r.typ == "tailscale:index/tailnetKey:TailnetKey" {
			return r.inputs["tags"].ArrayValue()[0].StringValue()
		}
	}

	return ""
}

func deployEC2With(t *testing.T, public bool) *mocks {
	t.Helper()

	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		return sum + "  gatus_v5.37.0_linux_arm64\n" + sum + "  gatus_v5.37.0_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		in := Inputs{
			Backend:       BackendEC2,
			Version:       "v0.7.0",
			PlatformHosts: PlatformHosts(groups()), ByCompany: HostsByCompany(groups()),
			Entities: []Entity{{Code: "acme", DisplayName: "Acme"}}, AlertsReadHost: "alerts.example.test", DeadmanChannel: "#deadman",
			EC2: EC2Inputs{
				VPCID:                      pulumi.String("vpc-0example"),
				SubnetIDs:                  []pulumi.StringInput{pulumi.String("subnet-0a"), pulumi.String("subnet-0b")},
				Bucket:                     "acme-status-replica",
				BucketPrefix:               "box",
				AlertsReadTokenParameter:   "/acme/status/alerts-read-token",
				DeadmanSlackTokenParameter: "/acme/status/deadman-token",
			},
		}
		if public {
			in.PublicHostname = "status.example.com"
			in.OIDC = OIDC{IssuerURL: "https://issuer.example.com", ClientID: "status"}
			in.EC2.OIDCClientSecretParameter = "/acme/status/oidc-client-secret"
			in.EC2.TunnelTokenParameter = "/acme/status/tunnel-token"
		}

		return Deploy(c, slog.New(slog.DiscardHandler), in)
	}, pulumi.WithMocks("proj", "stack", m)))

	return m
}

// The EC2 backend mints no tailnet key and creates no Lightsail instance: it is
// an Auto Scaling group, and no secret value is an input at all.
func TestDeployEC2CreatesAGroupAndNoKey(t *testing.T) {
	for _, public := range []bool{false, true} {
		m := deployEC2With(t, public)

		assert.Equal(t, []string{"status"}, m.names("aws:autoscaling/group:Group"))
		assert.Empty(t, m.names("tailscale:index/tailnetKey:TailnetKey"))
		assert.Empty(t, m.names("aws:lightsail/instance:Instance"))
	}
}

func TestDeployEC2PublicPageNeedsItsParameters(t *testing.T) {
	err := pulumi.RunErr(func(c *pulumi.Context) error {
		return Deploy(c, slog.New(slog.DiscardHandler), Inputs{Backend: BackendEC2, PublicHostname: "status.example.com"})
	}, pulumi.WithMocks("proj", "stack", &mocks{}))
	require.ErrorContains(t, err, "EC2.OIDCClientSecretParameter and EC2.TunnelTokenParameter")
}

func TestDeployRefusesAnUnknownBackend(t *testing.T) {
	err := pulumi.RunErr(func(c *pulumi.Context) error {
		return Deploy(c, slog.New(slog.DiscardHandler), Inputs{Backend: "gce"})
	}, pulumi.WithMocks("proj", "stack", &mocks{}))
	require.ErrorContains(t, err, `unknown Backend "gce"`)
}
