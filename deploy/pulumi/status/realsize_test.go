package status

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
	"github.com/truvity/tailscale/pkg/hostaccess"

	"github.com/truvity/observability/pkg/statusbox"
	"github.com/truvity/observability/pkg/statusbox/ec2"
)

// userDataBudget is the most the EC2 backend's user-data may weigh on an
// estate-sized catalogue. EC2's hard limit is 16 KiB; the rest is headroom for
// the box to grow (more options, longer parameter names) without a surprise.
const userDataBudget = 12 * 1024

// estateGroups is a catalogue as large as a real estate's: platform hosts on
// three clusters and five companies with several components each, about 40
// endpoints in all.
func estateGroups() []HostGroup {
	var groups []HostGroup

	for _, cluster := range []string{"dev", "stage", "prod"} {
		var hosts []string

		for _, svc := range []string{"console", "grafana", "argocd", "vault", "keycloak"} {
			hosts = append(hosts, fmt.Sprintf("%s.%s.platform.example.test", svc, cluster))
		}

		groups = append(groups, HostGroup{Cluster: cluster, Hosts: hosts})
	}

	for _, company := range []string{"acme", "globex", "initech", "umbrella", "hooli"} {
		for _, cluster := range []string{"dev", "prod"} {
			for _, component := range []string{"portal", "api"} {
				groups = append(groups, HostGroup{
					Cluster: cluster, Company: company, Component: component,
					Hosts: []string{fmt.Sprintf("%s.%s.%s.example.test", component, company, cluster)},
				})
			}
		}
	}

	return groups
}

func estateEC2Inputs(t *testing.T) Inputs {
	t.Helper()

	var entities []Entity

	for _, c := range []string{"acme", "globex", "initech", "umbrella", "hooli"} {
		entities = append(entities, Entity{Code: c, DisplayName: c + " Corporation"})
	}

	return Inputs{
		Version:        "v0.7.0",
		PlatformHosts:  PlatformHosts(estateGroups()),
		ByCompany:      HostsByCompany(estateGroups()),
		Entities:       entities,
		AlertsReadHost: "alerts-read.platform.example.test",
		DeadmanChannel: "#deadman",
		PublicHostname: "status.platform.example.test",
		OIDC:           OIDC{IssuerURL: "https://issuer.platform.example.test/realms/acme", ClientID: "status"},
		EC2: EC2Inputs{
			VPCID:                      pulumi.String("vpc-0example"),
			SubnetIDs:                  []pulumi.StringInput{pulumi.String("subnet-0a"), pulumi.String("subnet-0b")},
			PrivateIngressCIDRs:        []string{"10.20.0.0/16"},
			Bucket:                     "acme-status-replica",
			BucketPrefix:               "status/box",
			AlertsReadTokenParameter:   "/acme/status/alerts-read-token",
			DeadmanSlackTokenParameter: "/acme/status/deadman-slack-token",
			OIDCClientSecretParameter:  "/acme/status/oidc-client-secret",
			TunnelTokenParameter:       "/acme/status/tunnel-token",
			TelegramTokenParameter:     "/acme/status/telegram-token",
			TelegramChatIDParameter:    "/acme/status/telegram-chat-id",
			PingURLParameter:           "/acme/status/ping-url",
			SSH: &ec2.SSHArgs{
				OPKSSH:           hostaccess.OPKSSHPreset{Issuer: "https://issuer.platform.example.test/realms/acme", ClientID: "opkssh", User: "ec2-user", Group: "ops"},
				HostKeyParameter: "/acme/status/ssh-host-key",
				IngressCIDRs:     []string{"10.30.0.0/16"},
			},
			SelfRegister: &ec2.SelfRegisterArgs{
				RoleARN:      "arn:" + "aws" + ":iam::" + strings.Repeat("7", 12) + ":role/dns-writer",
				HostedZoneID: "Z0EXAMPLE",
				RecordName:   "status-breakglass.platform.example.test",
			},
		},
	}
}

// TestEstateSizedCatalogueFitsTheUserDataBudget is the regression test for the
// 17,826-byte user-data: the fixture is as big as the estate's catalogue with
// every option on (both instances, Telegram, ping, SSH, SelfRegister), which a
// small fixture never was. The Gatus configurations travel as S3 objects, so
// the user-data stays small however large the catalogue grows.
func TestEstateSizedCatalogueFitsTheUserDataBudget(t *testing.T) {
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

		return sum + "  gatus_v5.37.0_linux_arm64\n" + sum + "  gatus_v5.37.0_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	in := estateEC2Inputs(t)
	cfg, err := statusbox.RenderGatus(OpsCatalogue(in.catalogue(true)))
	require.NoError(t, err)
	require.GreaterOrEqual(t, strings.Count(cfg, "- name: "), 40, "the fixture must be estate-sized")

	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		return Deploy(c, slog.New(slog.DiscardHandler), in)
	}, pulumi.WithMocks("proj", "stack", m)))

	encoded := m.input("aws:ec2/launchTemplate:LaunchTemplate", "status", "userData")
	require.NotEmpty(t, encoded)

	userData, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	t.Logf("user-data: %d bytes (budget %d, EC2 limit %d); config: %d bytes", len(userData), userDataBudget, 16*1024, len(cfg))
	require.LessOrEqual(t, len(userData), userDataBudget)

	// Both configurations (and the setup script) are objects in the box's
	// bucket, not user-data.
	require.Equal(t, []string{"status-config-ops", "status-config-ops-breakglass", "status-setup-script"}, m.names("aws:s3/bucketObjectv2:BucketObjectv2"))
	require.NotContains(t, string(userData), "endpoints:")

	for _, name := range m.names("aws:s3/bucketObjectv2:BucketObjectv2") {
		require.Equal(t, "acme-status-replica", m.input("aws:s3/bucketObjectv2:BucketObjectv2", name, "bucket"))
		require.Equal(t, "AES256", m.input("aws:s3/bucketObjectv2:BucketObjectv2", name, "serverSideEncryption"))
		// A secret is only ever an ${ENV} reference, filled from SSM at boot.
		require.NotRegexp(t, `(?i)(secret|token): +[^$\s]`, m.input("aws:s3/bucketObjectv2:BucketObjectv2", name, "content"), "no secret in an object")
	}

}
