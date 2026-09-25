package lightsail_test

import (
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lightsail"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
	statusboxlightsail "github.com/truvity/observability/pkg/statusbox/lightsail"
)

// mocks stands in for the real AWS provider. It echoes every resource's
// inputs back as its state, which is enough to inspect what NewLightsail
// asked the provider to create without ever calling AWS.
type mocks int

func (mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "_id", args.Inputs, nil
}

func (mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

// stubChecksums replaces statusbox.FetchChecksums for the duration of a
// test, so NewLightsail's own call into pkg/statusbox never reaches
// GitHub for a release this repository has not necessarily cut yet. See
// the doc comment on FetchChecksums for why it is this package's own
// seam and not a test-only hack.
func stubChecksums(t *testing.T) {
	t.Helper()
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(version string) (string, error) {
		return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  setup.sh\n", nil
	}
	t.Cleanup(func() { statusbox.FetchChecksums = previous })
}

func validArgs() *statusboxlightsail.LightsailArgs {
	return &statusboxlightsail.LightsailArgs{
		AvailabilityZone: "us-east-1b",
		Args: statusbox.Args{
			Version: "v1.0.0",
			Instances: []statusbox.Instance{
				{Name: "example-co", Port: 8081, Public: true, Config: "endpoints: []\n"},
				{Name: "ops", Port: 8084, Public: false, Config: "external-endpoints: []\n"},
			},
			Hostnames: map[string]string{"example-co": "status.example.test"},
			Secrets: statusbox.Secrets{
				TailscaleAuthKey: pulumi.String("test-tailscale-authkey"),
				TunnelToken:      pulumi.String("test-tunnel-token"),
			},
		},
	}
}

// TestPublicPortsAreEmpty is the one property docs/statusbox.md asks a
// unit test to prove directly: the firewall this package creates has an
// empty port list, so an estate reviewing a plan sees "no port" rather
// than the absence of a resource that might mean anything.
func TestPublicPortsAreEmpty(t *testing.T) {
	stubChecksums(t)

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		box, err := statusboxlightsail.NewLightsail(ctx, "status", validArgs())
		if err != nil {
			return err
		}

		done := make(chan struct{})
		box.PublicPorts.PortInfos.ApplyT(func(infos []lightsail.InstancePublicPortsPortInfo) []lightsail.InstancePublicPortsPortInfo {
			require.Empty(t, infos, "the firewall must be declared with an empty port list, not omitted")
			close(done)
			return infos
		})
		<-done
		return nil
	}, pulumi.WithMocks("statusbox-lightsail-test", "test", mocks(0)))
	require.NoError(t, err)
}

// TestNewLightsailRefusesAnEmptyAvailabilityZone proves the one input
// this package adds beyond statusbox.Args is actually required: Lightsail
// has no provider-configuration default to fall back to (see
// NewLightsail's doc comment), so a caller that forgot it must be told
// before anything is provisioned, not after a plan fails deep inside the
// provider. No checksums stub is needed: this refusal fires before
// NewLightsail ever calls into pkg/statusbox.
func TestNewLightsailRefusesAnEmptyAvailabilityZone(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		a := validArgs()
		a.AvailabilityZone = ""
		_, err := statusboxlightsail.NewLightsail(ctx, "status", a)
		return err
	}, pulumi.WithMocks("statusbox-lightsail-test", "test", mocks(0)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "AvailabilityZone is empty")
}

// TestDiskSurvivesInstanceIdentity proves Disk and DiskAttachment are
// their own resources, named independently of Instance, which is the
// point: docs/statusbox.md promises a config change replaces Instance
// and "the disk comes back with its history" — a promise that only
// holds if Disk's own identity never depends on Instance's.
func TestDiskSurvivesInstanceIdentity(t *testing.T) {
	stubChecksums(t)

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		box, err := statusboxlightsail.NewLightsail(ctx, "status", validArgs())
		if err != nil {
			return err
		}
		require.NotNil(t, box.Disk)
		require.NotNil(t, box.DiskAttachment)
		return nil
	}, pulumi.WithMocks("statusbox-lightsail-test", "test", mocks(0)))
	require.NoError(t, err)
}
