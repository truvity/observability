package statusbox_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
)

// testLeafCertPEM returns a freshly generated, self-signed certificate
// with BasicConstraints.IsCA left false — the one shape
// Args.TrustedCAs's own validation has to refuse: a certificate that
// could never act as a trust anchor no matter how it got there.
func testLeafCertPEM(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leaf.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

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
		Hostname:  "statusbox",
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
			name: "two private instances",
			mutate: func(a *statusbox.Args) {
				// example-co was the Public one; making it private too
				// means both instances are now Public: false, and the
				// box can forward only one of them to the tailnet on
				// port 80.
				a.Instances[0].Public = false
			},
			wantErr: "this box serves at most one private instance",
		},
		{
			name: "no version",
			mutate: func(a *statusbox.Args) {
				a.Version = ""
			},
			wantErr: "Version is empty",
		},
		{
			name: "no hostname",
			mutate: func(a *statusbox.Args) {
				a.Hostname = ""
			},
			wantErr: "Hostname is empty",
		},
		{
			name: "hostname is not a valid shape",
			mutate: func(a *statusbox.Args) {
				a.Hostname = "Status.Box"
			},
			wantErr: "is not a valid name",
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
		{
			name: "env key is not a valid environment-variable name",
			mutate: func(a *statusbox.Args) {
				a.Secrets.Env = map[string]pulumi.StringInput{
					"push-service": pulumi.String("test-env-value"),
				}
			},
			wantErr: "is not a valid environment-variable name",
		},
		{
			name: "env key collides with a reserved name",
			mutate: func(a *statusbox.Args) {
				a.Secrets.Env = map[string]pulumi.StringInput{
					"TUNNEL_TOKEN": pulumi.String("test-env-value"),
				}
			},
			wantErr: "is reserved by statusbox itself",
		},
		{
			name: "env key collides with an AlertURLs entry",
			mutate: func(a *statusbox.Args) {
				a.Secrets.AlertURLs = map[string]pulumi.StringInput{
					"slack": pulumi.String("test-alert-url"),
				}
				a.Secrets.Env = map[string]pulumi.StringInput{
					"ALERT_URL_SLACK": pulumi.String("test-env-value"),
				}
			},
			wantErr: "collides with Secrets.AlertURLs",
		},
		{
			name: "TrustedCAs is not PEM at all",
			mutate: func(a *statusbox.Args) {
				a.TrustedCAs = "not a pem bundle at all\n"
			},
			wantErr: "no PEM CERTIFICATE block",
		},
		{
			name: "TrustedCAs is a leaf certificate, not a CA",
			mutate: func(a *statusbox.Args) {
				a.TrustedCAs = testLeafCertPEM(t)
			},
			wantErr: "is not a CA certificate",
		},
		{
			name: "TrustedCAs has trailing data after its last PEM block",
			mutate: func(a *statusbox.Args) {
				// the trailing-garbage refusal alone (with an otherwise
				// VALID CA) is proven directly, at the unit level, by
				// TestParseTrustedCAs in statusbox_internal_test.go — this
				// case only has to prove Args.validate reaches
				// parseTrustedCAs at all.
				a.TrustedCAs = testLeafCertPEM(t) + "garbage-after-the-cert\n"
			},
			wantErr: "is not a CA certificate",
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
