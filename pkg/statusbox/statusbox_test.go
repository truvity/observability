package statusbox_test

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
)

// mocks is the smallest MockResourceMonitor CloudInit's own tests need:
// this package registers no resource, so nothing here has to do more
// than echo a resource's inputs back as its state.
type mocks int

func (mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "_id", args.Inputs, nil
}

func (mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

// runCloudInit is the one place every refusal test goes through
// pulumi.RunErr: CloudInit takes a *pulumi.Context, and every case
// below is refused by Args.validate() before CloudInit ever reaches the
// network, so no test here needs a stubbed checksums.txt.
func runCloudInit(a statusbox.Args) error {
	return pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := statusbox.CloudInit(ctx, a)
		return err
	}, pulumi.WithMocks("statusbox-test", "test", mocks(0)))
}

func validArgs() statusbox.Args {
	return statusbox.Args{
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
	}
}

// TestRefusals mutates exactly one field of validArgs() per case — see
// TestValidArgsPassesValidate in statusbox_internal_test.go, which
// proves that unmodified fixture passes validation, so a refusal that
// fired on it unmodified would be a false positive in every case below.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*statusbox.Args)
		wantErr string
	}{
		{
			name: "instance with no config",
			mutate: func(a *statusbox.Args) {
				a.Instances[0].Config = ""
			},
			wantErr: `instance "example-co": Config is empty`,
		},
		{
			name: "two instances on one port",
			mutate: func(a *statusbox.Args) {
				a.Instances[1].Port = a.Instances[0].Port
			},
			wantErr: "is also used by instance",
		},
		{
			name: "public instance with no hostname",
			mutate: func(a *statusbox.Args) {
				a.Hostnames = map[string]string{}
			},
			wantErr: `Public is true but Hostnames["example-co"] is empty`,
		},
		{
			name: "no version",
			mutate: func(a *statusbox.Args) {
				a.Version = ""
			},
			wantErr: "Version is empty",
		},
		{
			name: "no instances",
			mutate: func(a *statusbox.Args) {
				a.Instances = nil
			},
			wantErr: "no instances",
		},
		{
			name: "instance name is not a valid shape",
			mutate: func(a *statusbox.Args) {
				a.Instances[0].Name = "Example.Co"
			},
			wantErr: "is not a valid name",
		},
		{
			name: "duplicate instance name",
			mutate: func(a *statusbox.Args) {
				a.Instances[1].Name = a.Instances[0].Name
			},
			wantErr: "appears twice",
		},
		{
			name: "hostname names no instance",
			mutate: func(a *statusbox.Args) {
				a.Hostnames["typo-co"] = "status.example.test"
			},
			wantErr: `Hostnames["typo-co"] names no instance`,
		},
		{
			name: "no tailnet key",
			mutate: func(a *statusbox.Args) {
				a.Secrets.TailscaleAuthKey = nil
			},
			wantErr: "TailscaleAuthKey is nil",
		},
		{
			name: "public instance but no tunnel token",
			mutate: func(a *statusbox.Args) {
				a.Secrets.TunnelToken = nil
			},
			wantErr: "TunnelToken is nil",
		},
		{
			name: "alert key is not a valid environment-variable suffix",
			mutate: func(a *statusbox.Args) {
				a.Secrets.AlertURLs = map[string]pulumi.StringInput{
					"push-service": pulumi.String("test-alert-url"),
				}
			},
			wantErr: "is not a valid name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := validArgs()
			c.mutate(&a)
			err := runCloudInit(a)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.wantErr)
		})
	}
}
