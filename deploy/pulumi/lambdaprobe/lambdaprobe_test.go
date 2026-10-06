package lambdaprobe

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/deploy/pulumi/otlplayer"
	"github.com/truvity/observability/deploy/pulumi/releaseassets"
)

const (
	version = "0.47.0"
)

// fakeRelease serves a checksums file and the layer zips, each holding the
// extension at its path; corrupt makes the arm64 zip differ from what the
// checksums file lists.
func fakeRelease(t *testing.T, corrupt bool) releaseassets.Fetcher {
	t.Helper()

	zips := map[string][]byte{}

	var sums strings.Builder

	for _, arch := range []string{"arm64", "amd64"} {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		w, err := zw.Create(otlplayer.ExtensionPath)
		require.NoError(t, err)
		_, err = w.Write([]byte("extension-" + arch))
		require.NoError(t, err)
		require.NoError(t, zw.Close())

		name := otlplayer.AssetName(version, arch)
		zips[name] = buf.Bytes()

		h := sha256.Sum256(buf.Bytes())
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}

	if corrupt {
		zips[otlplayer.AssetName(version, "arm64")] = []byte("tampered")
	}

	return func(_ context.Context, url string) ([]byte, error) {
		base := "https://github.com/truvity/observability/releases/download/v" + version + "/"
		if !strings.HasPrefix(url, base) {
			return nil, fmt.Errorf("unexpected url %s", url)
		}

		name := strings.TrimPrefix(url, base)
		if name == releaseassets.ChecksumsFile {
			return []byte(sums.String()), nil
		}

		if b, ok := zips[name]; ok {
			return b, nil
		}

		return nil, fmt.Errorf("404 %s", name)
	}
}

func TestDeployRefusesATamperedLayer(t *testing.T) {
	cfg := sampleConfig()

	err := runErr(t, cfg, "example", &mocks{}, fakeRelease(t, true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the release checksums file")
}

func TestPolicyDocumentPinsAudience(t *testing.T) {
	doc, err := policyDocument("https://issuer.example.com")
	require.NoError(t, err)

	var p struct {
		Statement []struct {
			Action    string
			Condition map[string]map[string]string
		}
	}

	require.NoError(t, json.Unmarshal([]byte(doc), &p))
	require.Len(t, p.Statement, 1)
	assert.Equal(t, "sts:GetWebIdentityToken", p.Statement[0].Action)
	// The audience key is multi-valued: ForAllValues:StringEquals, never StringEquals.
	assert.Equal(t, "https://issuer.example.com", p.Statement[0].Condition["ForAllValues:StringEquals"]["sts:IdentityTokenAudience"])
	assert.NotContains(t, p.Statement[0].Condition["StringEquals"], "sts:IdentityTokenAudience")
	assert.Equal(t, "ES384", p.Statement[0].Condition["StringEquals"]["sts:SigningAlgorithm"])
	assert.Equal(t, "300", p.Statement[0].Condition["NumericLessThanEquals"]["sts:DurationSeconds"])
}

func TestDeployCreatesLayersRoleFunctionAndSchedule(t *testing.T) {
	cfg := sampleConfig()

	m := run(t, cfg, "example")
	assert.Equal(t, version, cfg.Layer.Version)

	assert.Equal(t, []string{"layer-amd64", "layer-arm64"}, m.names("aws:lambda/layerVersion:LayerVersion"))
	assert.Equal(t, "otlp-lambda-arm64", m.input("aws:lambda/layerVersion:LayerVersion", "layer-arm64", "layerName"))
	assert.Equal(t, []string{"function"}, m.names("aws:lambda/function:Function"))
	assert.Equal(t, []string{"schedule"}, m.names("aws:cloudwatch/eventRule:EventRule"))
	assert.Equal(t, "rate(5 minutes)", m.input("aws:cloudwatch/eventRule:EventRule", "schedule", "scheduleExpression"))

	// The role is named exactly after the function and sits under the boundary.
	role := "aws:iam/role:Role"
	assert.Equal(t, "lambda-otel-probe", m.input(role, "role", "name"))
	assert.Empty(t, m.input(role, "role", "path"), "role path stays the default /")
	assert.Equal(t, "boundary-arn", m.input(role, "role", "permissionsBoundary"))
	attachment := m.input("aws:iam/rolePolicyAttachment:RolePolicyAttachment", "role-basic-execution", "policyArn")
	assert.Equal(t, string(iam.ManagedPolicyAWSLambdaBasicExecutionRole), attachment)

	// ONE audience: the policy, the layer's STS audience and the issuer URL.
	audience := cfg.Probe.STSAudience()
	policy := m.input("aws:iam/rolePolicy:RolePolicy", "role-identity-token", "policy")
	assert.Contains(t, policy, `"ForAllValues:StringEquals":{"sts:IdentityTokenAudience":"`+audience+`"}`)

	env := m.env("function")
	assert.Equal(t, audience, env["ACCESS_ROSTER_AUDIENCE"])
	assert.Equal(t, cfg.Probe.IssuerURL, env["ACCESS_ROSTER_ISSUER"])
	assert.Equal(t, cfg.Probe.OTLPEndpoint, env["ACCESS_ROSTER_OTLP_ENDPOINT"])
	assert.Equal(t, "lambda-otel-probe", env["OTEL_SERVICE_NAME"])
}

func TestDeployRefusesOtherAccounts(t *testing.T) {
	cfg := sampleConfig()

	for _, account := range []string{"other", "third", "fourth"} {
		err := runErr(t, cfg, account, &mocks{}, fakeRelease(t, false))
		require.Error(t, err, account)
		assert.Contains(t, err.Error(), "configured for account")
	}
}

func TestHandlerCarriesTheMarker(t *testing.T) {
	assert.Contains(t, handlerSource, `MARKER = "lambda-otel-probe heartbeat"`)
}

func run(t *testing.T, cfg *Config, account string) *mocks {
	t.Helper()

	m := &mocks{}
	require.NoError(t, runErr(t, cfg, account, m, fakeRelease(t, false)))

	return m
}

func runErr(t *testing.T, cfg *Config, account string, m *mocks, fetch releaseassets.Fetcher) error {
	t.Helper()

	return pulumi.RunErr(func(c *pulumi.Context) error {
		p, err := aws.NewProvider(c, "p", &aws.ProviderArgs{})
		if err != nil {
			return err
		}

		return Deploy(c, slog.New(slog.DiscardHandler), cfg, Inputs{
			Account:             account,
			PermissionsBoundary: "boundary-arn",
			Provider:            p,
			Fetch:               fetch,
			CacheDir:            t.TempDir(),
		})
	}, pulumi.WithMocks("nexus-devel", "lambda-otel-probe", m))
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

	out := args.Inputs.Copy()
	out["arn"] = resource.NewStringProperty("mock-arn/" + args.Name)

	return args.Name + "-id", out, nil
}

func (m *mocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *mocks) find(typ, name string) (resource.PropertyMap, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.resources {
		if r.typ == typ && r.name == name {
			return r.inputs, true
		}
	}

	return nil, false
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

func (m *mocks) input(typ, name, key string) string {
	in, ok := m.find(typ, name)
	if !ok {
		return ""
	}

	val, ok := in[resource.PropertyKey(key)]
	if !ok || val.IsNull() || !val.IsString() {
		return ""
	}

	return val.StringValue()
}

func (m *mocks) env(fn string) map[string]string {
	in, _ := m.find("aws:lambda/function:Function", fn)
	out := map[string]string{}

	env := in["environment"]
	if !env.IsObject() {
		return out
	}

	vars := env.ObjectValue()["variables"]
	if !vars.IsObject() {
		return out
	}

	for k, v := range vars.ObjectValue() {
		out[string(k)] = v.StringValue()
	}

	return out
}

func sampleConfig() *Config {
	return &Config{
		Version: Version,
		Account: "example",
		Layer: Layer{
			Version:            version,
			Name:               "otlp-lambda",
			Architectures:      []string{"arm64", "amd64"},
			CompatibleRuntimes: []string{"python3.13", "provided.al2023"},
		},
		Probe: Probe{
			FunctionName: "lambda-otel-probe",
			Architecture: "arm64",
			Runtime:      "python3.13",
			Schedule:     "rate(5 minutes)",
			IssuerURL:    "https://issuer.example.com",
			OTLPEndpoint: "https://otlp.example.com",
		},
	}
}
