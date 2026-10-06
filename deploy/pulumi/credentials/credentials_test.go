package credentials

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	password = "random:index/randomPassword:RandomPassword"
	ssmParam = "aws:ssm/parameter:Parameter"
)

type (
	put struct{ namespace, key string }

	fakeWriter struct {
		mu   sync.Mutex
		puts []put
	}
)

func (w *fakeWriter) Put(_ *pulumi.Context, namespace, key string, _ map[string]pulumi.StringInput, _ ...pulumi.ResourceOption) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.puts = append(w.puts, put{namespace, key})

	return nil
}

func kernelInputs(w KVWriter, p *aws.Provider) Inputs {
	return Inputs{
		Cluster: "hub", Writer: w, KVKind: "obs", StoreInstall: true, Rotated: "2000-01-01",
		Kernel: &Kernel{
			Provider:                p,
			StatusboxRotated:        "2000-02-01",
			StatusboxReadTokenSSM:   "/x/read",
			EvaluatorRotated:        "2000-03-01",
			EvaluatorNamespaces:     []string{"hub", "other"},
			OIDCClientSecretKV:      "proxy/client",
			OIDCClientSecretRotated: "2000-04-01",
			OIDCClientSecretSSM:     "/x/oidc",
		},
	}
}

func run(t *testing.T, build func(p *aws.Provider) Inputs) (*mocks, *fakeWriter) {
	t.Helper()

	m, w := &mocks{}, &fakeWriter{}

	require.NoError(t, pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := aws.NewProvider(ctx, "p", &aws.ProviderArgs{})
		if err != nil {
			return err
		}

		in := build(p)
		in.Writer = w

		return Deploy(ctx, slog.New(slog.DiscardHandler), in)
	}, pulumi.WithMocks("proj", "stack", m)))

	return m, w
}

func TestDeployMintsBothCredentials(t *testing.T) {
	m, w := run(t, func(*aws.Provider) Inputs {
		return Inputs{Cluster: "c", KVKind: "obs", StoreInstall: true, Rotated: "2000-01-01", StorePasswordRotated: "2000-06-01"}
	})

	assert.Equal(t, []string{"observability-store-password", "observability-write-token"}, m.names(password))
	assert.EqualValues(t, storePasswordLength, m.input(password, "observability-store-password", "length").NumberValue())
	assert.EqualValues(t, writeTokenLength, m.input(password, "observability-write-token", "length").NumberValue())
	assert.False(t, m.input(password, "observability-store-password", "special").BoolValue())

	// The store password has its own lever; the write token rides the shared one.
	assert.Equal(t, map[string]string{"rotated": "2000-06-01"}, m.keepers(password, "observability-store-password"))
	assert.Equal(t, map[string]string{"rotated": "2000-01-01"}, m.keepers(password, "observability-write-token"))
	assert.Equal(t, []put{{"c", "obs/store-credentials"}, {"c", "obs/write-token"}}, w.puts)
}

func TestStorePasswordFallsBackToSharedLever(t *testing.T) {
	m, _ := run(t, func(*aws.Provider) Inputs {
		return Inputs{Cluster: "c", KVKind: "obs", StoreInstall: true, Rotated: "2000-01-01"}
	})

	assert.Equal(t, map[string]string{"rotated": "2000-01-01"}, m.keepers(password, "observability-store-password"))
}

func TestOperatorOnlyClusterMintsNoStorePassword(t *testing.T) {
	m, w := run(t, func(*aws.Provider) Inputs {
		return Inputs{Cluster: "c", KVKind: "obs", Rotated: "2000-01-01"}
	})

	assert.Equal(t, []string{"observability-write-token"}, m.names(password))
	assert.Equal(t, []put{{"c", "obs/write-token"}}, w.puts)
}

func TestKernelMintsTheExtraCredentials(t *testing.T) {
	m, w := run(t, func(p *aws.Provider) Inputs { return kernelInputs(nil, p) })

	assert.Equal(t, []string{
		"observability-kernel-evaluator-read-token", "observability-store-password", "observability-write-token",
		"statusbox-alerts-read-token", "statusbox-oidc-client-secret",
	}, m.names(password))
	assert.Equal(t, []string{"statusbox-ssm-oidc-client-secret", "statusbox-ssm-read-token"}, m.names(ssmParam))
	assert.Equal(t, "/x/read", m.input(ssmParam, "statusbox-ssm-read-token", "name").StringValue())
	assert.Equal(t, "/x/oidc", m.input(ssmParam, "statusbox-ssm-oidc-client-secret", "name").StringValue())
	assert.Equal(t, map[string]string{"rotated": "2000-03-01"}, m.keepers(password, "observability-kernel-evaluator-read-token"))

	// One mint, written to every evaluator namespace.
	assert.Contains(t, w.puts, put{"hub", "obs/kernel-evaluator"})
	assert.Contains(t, w.puts, put{"other", "obs/kernel-evaluator"})
	assert.Contains(t, w.puts, put{"hub", "proxy/client"})
	assert.Contains(t, w.puts, put{"hub", "obs/statusbox"})
}

func TestDeadmanSlackTokenCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		token, skip string
		want        []string
	}{
		"installed":     {token: "xoxb-token", want: []string{"statusbox-ssm-deadman-slack-token"}},
		"not installed": {},
		"skipped":       {token: "xoxb-token", skip: "unit test"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := run(t, func(p *aws.Provider) Inputs {
				in := kernelInputs(nil, p)
				in.Kernel.DeadmanSlack = &DeadmanSlack{
					Skip: tc.skip, SSMPath: "/x/slack", Source: "kv/slack",
					Read: func(context.Context) (string, error) { return tc.token, nil },
				}

				return in
			})

			var got []string

			for _, n := range m.names(ssmParam) {
				if n == "statusbox-ssm-deadman-slack-token" {
					got = append(got, n)
				}
			}

			assert.Equal(t, tc.want, got)

			if tc.want != nil {
				assert.Equal(t, "/x/slack", m.input(ssmParam, tc.want[0], "name").StringValue())
				assert.Equal(t, "SecureString", m.input(ssmParam, tc.want[0], "type").StringValue())
			}
		})
	}
}

func TestMetricsBackupRole(t *testing.T) {
	m, _ := run(t, func(p *aws.Provider) Inputs {
		in := kernelInputs(nil, p)
		in.Kernel.MetricsBackup = &MetricsBackup{
			ClusterName: "hub", Region: "eu-west-1", AccountID: "123456789012",
			PermissionsBoundary: "arn:aws:iam::123456789012:policy/boundary",
			Namespace:           "observability", ServiceAccount: "backup", RoleName: "hub-metrics-backup",
			Prefix: "metrics", BucketARN: "arn:aws:s3:::bucket", KMSKeyARN: "arn:aws:kms:eu-west-1:123456789012:key/k",
			BucketName: "bucket",
		}

		return in
	})

	assert.Equal(t, []string{"hub-metrics-backup"}, m.names("aws:iam/role:Role"))
	assert.Equal(t, []string{"hub-metrics-backup-policy"}, m.names("aws:iam/rolePolicy:RolePolicy"))
	assert.Equal(t, []string{"hub-metrics-backup-pia"}, m.names("aws:eks/podIdentityAssociation:PodIdentityAssociation"))
	assert.Len(t, m.names("pulumi:providers:aws"), 1)
}

func TestMetricsBackupPolicyScopedToPrefix(t *testing.T) {
	doc, err := metricsBackupPolicy("pfx", "arn:aws:s3:::b", "arn:aws:kms:r:1:key/k")
	require.NoError(t, err)

	for _, want := range []string{
		`"arn:aws:s3:::b/pfx/*"`, `"s3:PutObject"`, `"s3:GetObject"`, `"s3:DeleteObject"`, `"s3:ListBucket"`,
		`"kms:Decrypt"`, `"arn:aws:kms:r:1:key/k"`, `"s3:prefix"`, `"pfx"`,
	} {
		assert.Contains(t, doc, want)
	}
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

func (m *mocks) input(typ, name, key string) resource.PropertyValue {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.resources {
		if r.typ == typ && r.name == name {
			return r.inputs[resource.PropertyKey(key)]
		}
	}

	return resource.NewNullProperty()
}

func (m *mocks) keepers(typ, name string) map[string]string {
	out := map[string]string{}

	for key, value := range m.input(typ, name, "keepers").ObjectValue() {
		out[string(key)] = value.StringValue()
	}

	return out
}
