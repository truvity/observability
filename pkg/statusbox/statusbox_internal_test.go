package statusbox

// White-box tests: this file is `package statusbox`, not
// `package statusbox_test`, because it needs two things no exported API
// gives it — the pure render function, so the golden test and the
// size-limit refusal are hermetic and need neither a Pulumi context nor
// the network, and the FetchChecksums package variable, so the one test
// that does exercise CloudInit end-to-end through Pulumi's mocks does
// not reach the real network either. Everything that can be asked of the
// exported API alone (Args's other refusals) still is, in
// statusbox_test.go, so that file also serves as this package's usage
// example.

import (
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

// update regenerates the golden file instead of comparing against it —
// the same shape as hack/golden.sh's own `update` mode, for the same
// reason: a rendering change should produce a reviewable diff, not a
// hand-edited fixture.
//
//	go test ./pkg/statusbox/... -run TestRenderGoldenTwoInstances -update
var update = flag.Bool("update", false, "update the golden file this package's render test compares against")

// fixedSHA256 stands in for whatever FetchChecksums would really return
// for setup.sh — 64 hex characters, shaped like a sha256 sum and nothing
// else, so a test asserting the shape survives rendering does not also
// have to assert a value nobody chose for meaning.
const fixedSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func twoInstanceArgs() Args {
	return Args{
		Version: "v1.0.0",
		Instances: []Instance{
			{
				Name:   "example-co",
				Port:   8081,
				Public: true,
				Config: "endpoints:\n" +
					"  - name: example-co-website\n" +
					"    url: https://status.example.test/health\n" +
					"    interval: 1m\n" +
					"    conditions:\n" +
					"      - \"[STATUS] == 200\"\n",
			},
			{
				Name:   "ops",
				Port:   8084,
				Public: false,
				Config: "external-endpoints:\n" +
					"  - name: alerting-pipeline-deadman\n" +
					"    group: deadman\n" +
					"    heartbeat:\n" +
					"      interval: 5m\n" +
					"    alerts:\n" +
					"      - type: slack\n" +
					"      - type: webhook\n" +
					"        webhook-url: \"${ALERT_URL_SLACK}\"\n",
			},
		},
		Hostname: "statusbox",
		Hostnames: map[string]string{
			"example-co": "status.example.test",
		},
	}
}

// generateTestCAPEM returns a freshly generated, self-signed CA
// certificate (BasicConstraints.IsCA true) PEM-encoded — a real
// certificate rather than a hand-built byte blob, so a test asserting
// parseTrustedCAs or render's handling of Args.TrustedCAs exercises the
// actual x509.ParseCertificate path a box's own render does, not a
// stand-in for it. ECDSA P-256 keeps generation fast; nothing here
// signs anything else, so key strength is not the property under test.
func generateTestCAPEM(t *testing.T, commonName string) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// generateTestLeafPEM is the same as generateTestCAPEM except
// BasicConstraints.IsCA is false — a certificate parseTrustedCAs must
// refuse, exercising the same "not a CA" branch statusbox_test.go's
// TestRefusals proves through the exported API.
func generateTestLeafPEM(t *testing.T, commonName string) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// decodeUserData reverses wrapUserData: it pulls the base64 blob out of
// the rendered `#!/bin/bash\nbash -c "$(echo '<blob>' | base64 -d |
// gunzip)"\n` wrapper, decodes and gunzips it, and returns the inner
// bootstrap script — the only way a test can assert on what render
// actually staged (a heredoc marker, a manifest field) without
// re-implementing cloud-init.
func decodeUserData(t *testing.T, wrapped string) string {
	t.Helper()
	re := regexp.MustCompile(`echo '(.+)' \| base64`)
	m := re.FindStringSubmatch(wrapped)
	require.Len(t, m, 2, "wrapped user-data did not match the expected echo '<b64>' | base64 -d | gunzip shape:\n%s", wrapped)
	raw, err := base64.StdEncoding.DecodeString(m[1])
	require.NoError(t, err)
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	out, err := io.ReadAll(gz)
	require.NoError(t, err)
	return string(out)
}

func TestRenderGoldenTwoInstances(t *testing.T) {
	got, err := render(twoInstanceArgs(), fixedSHA256, "test-tailscale-authkey", "test-tunnel-token",
		map[string]string{"slack": "test-alert-slack-url"},
		map[string]string{"OIDC_CLIENT_SECRET": "test-oidc-client-secret"})
	require.NoError(t, err)

	golden := filepath.Join("testdata", "golden", "two-instance.txt")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		t.Logf("updated %s", golden)
		return
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err, "run 'go test ./pkg/statusbox/... -run TestRenderGoldenTwoInstances -update' and review the diff")
	require.Equal(t, string(want), got, "the rendered cloud-init moved: run 'go test ./pkg/statusbox/... -run TestRenderGoldenTwoInstances -update' and review "+
		"the diff")

	// The leading shebang line is Lightsail's own requirement, not an
	// accident of this test's fixture: cloud-init classifies user-data by
	// its FIRST LINE alone, and anything but `#!` there is stored as
	// text/plain and never runs at boot — a box that silently never
	// finishes booting, with no error anywhere to catch it. A rendering
	// that regresses to something else on its first line is exactly that
	// box.
	require.True(t, strings.HasPrefix(got, "#!/bin/bash\n"),
		"rendered user-data must start with \"#!/bin/bash\\n\": cloud-init classifies user-data by its first line, and anything but a shebang there becomes "+
			"text/plain and is never executed")

	// Beyond that first line, Lightsail imposes no line-count constraint
	// at all — only the 16 KB size cap TestRenderRefusesUserDataOverTheLimit
	// guards below — so the wrapper is exactly two physical lines: the
	// shebang, then the one `bash -c "$(...)"` line that decodes and runs
	// the gzipped, base64-encoded bootstrap. Every character of the
	// fixture past that point, except its own trailing newlines, is
	// inside a base64 blob that cannot contain one.
	require.Equal(t, 2, strings.Count(got, "\n"),
		"rendered user-data must be exactly the shebang line plus one further line: a third physical line would mean wrapUserData's shape changed without this "+
			"test being updated alongside it")
}

func TestRenderRefusesUserDataOverTheLimit(t *testing.T) {
	// Deliberately high-entropy: repeated text compresses away under
	// gzip almost to nothing, which would prove nothing about the
	// refusal. A fixed PRNG source keeps the fixture — and the failure
	// — reproducible between runs without committing tens of kilobytes
	// of random bytes to the repository.
	rng := rand.New(rand.NewSource(1))
	huge := make([]byte, 40*1024)
	for i := range huge {
		huge[i] = byte(rng.Intn(256))
	}
	oversized := fmt.Sprintf("endpoints:\n  - name: filler\n    url: https://example.test\n# %x\n", huge)

	args := twoInstanceArgs()
	args.Instances[0].Config = oversized

	_, err := render(args, fixedSHA256, "test-tailscale-authkey", "test-tunnel-token", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "byte limit")
}

// TestRenderEmptyTrustedCAsOmitsStagedFileAndManifestField proves the
// half of Args.TrustedCAs's contract TestRenderGoldenTwoInstances only
// proves indirectly (its fixture never sets the field, so the golden
// file matching is already evidence render is unchanged): that the
// INNER script — not just the outer wrapper — carries neither a staged
// trusted-cas.pem.gz.b64 heredoc nor a "trustedCAs" manifest key when
// Args.TrustedCAs is empty.
func TestRenderEmptyTrustedCAsOmitsStagedFileAndManifestField(t *testing.T) {
	got, err := render(twoInstanceArgs(), fixedSHA256, "test-tailscale-authkey", "test-tunnel-token", nil, nil)
	require.NoError(t, err)

	inner := decodeUserData(t, got)
	require.NotContains(t, inner, "trustedCAs")
	require.NotContains(t, inner, "trusted-cas.pem.gz.b64")
}

// TestRenderWithTrustedCAsStagesFileAndManifestField is the other half:
// a non-empty Args.TrustedCAs must show up in the inner script both as
// a staged, gzip+base64 heredoc (the same shape every Instance.Config
// already travels in) and as a "trustedCAs":true field in
// manifest.json, which is how setup.sh's own setup_trusted_cas tells
// "a bundle was staged" apart from "none was" without probing the
// filesystem — see that function's own doc comment in setup.sh.
func TestRenderWithTrustedCAsStagesFileAndManifestField(t *testing.T) {
	args := twoInstanceArgs()
	args.TrustedCAs = generateTestCAPEM(t, "test-root-ca")

	got, err := render(args, fixedSHA256, "test-tailscale-authkey", "test-tunnel-token", nil, nil)
	require.NoError(t, err)

	inner := decodeUserData(t, got)
	require.Contains(t, inner, `"trustedCAs":true`)
	require.Contains(t, inner, "cat > /opt/statusbox/staged/trusted-cas.pem.gz.b64 <<'STATUSBOX_TRUSTED_CAS'")
}

// TestRenderWithTrustedCAsFitsWithinUserDataLimit is the size bound the
// design calls for: a root bundle is a few KB, and CloudInit's own
// 16 KB ceiling (userDataLimit) has to have comfortable headroom left
// for one even alongside a real instance fixture's own Config. Two
// certificates, not one, because an estate rotating its root or
// carrying an intermediate alongside it is the realistic case this
// bound has to survive, not the smallest one that could pass.
func TestRenderWithTrustedCAsFitsWithinUserDataLimit(t *testing.T) {
	bundle := generateTestCAPEM(t, "root-one") + generateTestCAPEM(t, "root-two")
	require.Greater(t, len(bundle), 900, "test fixture: two PEM certificates should already be close to 1 KB, or this test proves less than it claims to")

	args := twoInstanceArgs()
	args.TrustedCAs = bundle

	got, err := render(args, fixedSHA256, "test-tailscale-authkey", "test-tunnel-token", nil, nil)
	require.NoError(t, err)
	require.Less(t, len(got), userDataLimit,
		"a two-certificate trusted-CA bundle (%d bytes of PEM) plus the two-instance fixture must still leave headroom under the %d-byte "+
			"limit", len(bundle), userDataLimit)
}

// TestValidArgsPassesValidate is the baseline every case in
// statusbox_test.go's TestRefusals mutates away from: it proves the
// shared fixture is accepted as it stands, so a refusal that fired
// unmodified would be a false positive in every one of those cases
// rather than evidence the mutation was what triggered it.
func TestValidArgsPassesValidate(t *testing.T) {
	a := twoInstanceArgs()
	a.Secrets = Secrets{
		TailscaleAuthKey: pulumi.String("test-tailscale-authkey"),
		TunnelToken:      pulumi.String("test-tunnel-token"),
	}
	require.NoError(t, a.validate())
}

// TestParseTrustedCAs exercises parseTrustedCAs directly — the pure
// validation core Args.validate calls — so each of these shapes is
// proven at the unit level, not only through the one representative
// case statusbox_test.go's TestRefusals adds through the exported API.
func TestParseTrustedCAs(t *testing.T) {
	t.Run("a single CA certificate is accepted", func(t *testing.T) {
		require.NoError(t, parseTrustedCAs(generateTestCAPEM(t, "root-one")))
	})

	t.Run("two concatenated CA certificates are accepted", func(t *testing.T) {
		bundle := generateTestCAPEM(t, "root-one") + generateTestCAPEM(t, "root-two")
		require.NoError(t, parseTrustedCAs(bundle))
	})

	t.Run("not PEM at all is refused", func(t *testing.T) {
		err := parseTrustedCAs("not a pem bundle at all\n")
		require.Error(t, err)
		require.Contains(t, err.Error(), "no PEM CERTIFICATE block")
	})

	t.Run("a PEM block of the wrong type is refused", func(t *testing.T) {
		block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not really a key either")})
		err := parseTrustedCAs(string(block))
		require.Error(t, err)
		require.Contains(t, err.Error(), `not CERTIFICATE`)
	})

	t.Run("a leaf certificate without the CA bit is refused", func(t *testing.T) {
		err := parseTrustedCAs(generateTestLeafPEM(t, "leaf.example.test"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "is not a CA certificate")
	})

	t.Run("trailing data after the last PEM block is refused", func(t *testing.T) {
		err := parseTrustedCAs(generateTestCAPEM(t, "root-one") + "not part of any PEM block\n")
		require.Error(t, err)
		require.Contains(t, err.Error(), "are not themselves a PEM block")
	})
}

func TestSetupSHA256Parses(t *testing.T) {
	body := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef  observability_1.0.0_checksums.txt\n" +
		fixedSHA256 + "  setup.sh\n"

	sha, err := setupSHA256(body, "v1.0.0")
	require.NoError(t, err)
	require.Equal(t, fixedSHA256, sha)
}

func TestSetupSHA256RefusesAMissingEntry(t *testing.T) {
	_, err := setupSHA256("deadbeef  something-else\n", "v1.0.0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "carries no entry for setup.sh")
}

// mocks is the smallest MockResourceMonitor that can stand in for a real
// one: it echoes every resource's own inputs back as its state, which is
// all CloudInit's own tests need since this package registers no
// resource of its own.
type mocks int

func (mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "_id", args.Inputs, nil
}

func (mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

// TestCloudInitEndToEnd exercises the one path the golden test above
// does not: CloudInit's own Pulumi plumbing, which combines
// Secrets — each a pulumi.StringInput an estate's real secret store
// would resolve asynchronously — with pulumi.All before render ever
// sees a plain string. FetchChecksums is swapped out for the duration
// of the test so this, like every other test in this package, never
// reaches the network.
func TestCloudInitEndToEnd(t *testing.T) {
	previous := FetchChecksums
	FetchChecksums = func(version string) (string, error) {
		require.Equal(t, "v1.0.0", version)
		return fixedSHA256 + "  setup.sh\n", nil
	}
	t.Cleanup(func() { FetchChecksums = previous })

	args := twoInstanceArgs()
	args.Secrets = Secrets{
		TailscaleAuthKey: pulumi.String("test-tailscale-authkey"),
		TunnelToken:      pulumi.String("test-tunnel-token"),
		AlertURLs: map[string]pulumi.StringInput{
			"slack": pulumi.String("test-alert-slack-url"),
		},
		Env: map[string]pulumi.StringInput{
			"OIDC_CLIENT_SECRET": pulumi.String("test-oidc-client-secret"),
		},
	}

	var got string
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		out, err := CloudInit(ctx, args)
		if err != nil {
			return err
		}
		done := make(chan struct{})
		out.ApplyT(func(v string) string {
			got = v
			close(done)
			return v
		})
		<-done
		return nil
	}, pulumi.WithMocks("statusbox-test", "test", mocks(0)))
	require.NoError(t, err)

	want, err := render(twoInstanceArgs(), fixedSHA256, "test-tailscale-authkey", "test-tunnel-token",
		map[string]string{"slack": "test-alert-slack-url"},
		map[string]string{"OIDC_CLIENT_SECRET": "test-oidc-client-secret"})
	require.NoError(t, err)
	require.Equal(t, want, got, "CloudInit must render the same thing render() does once its Pulumi inputs resolve to the same values")
}
