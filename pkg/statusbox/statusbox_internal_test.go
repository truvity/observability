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
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		Hostnames: map[string]string{
			"example-co": "status.example.test",
		},
	}
}

func TestRenderGoldenTwoInstances(t *testing.T) {
	got, err := render(twoInstanceArgs(), fixedSHA256, "test-tailscale-authkey", "test-tunnel-token",
		map[string]string{"slack": "test-alert-slack-url"})
	require.NoError(t, err)

	golden := filepath.Join("testdata", "golden", "two-instance.txt")
	if *update {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
		t.Logf("updated %s", golden)
		return
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err, "run 'go test ./pkg/statusbox/... -run TestRenderGoldenTwoInstances -update' and review the diff")
	require.Equal(t, string(want), got, "the rendered cloud-init moved: run 'go test ./pkg/statusbox/... -run TestRenderGoldenTwoInstances -update' and review the diff")

	// The one line requirement is Lightsail's own, not an accident of
	// this test's fixture: a rendering that regresses to multiple
	// physical lines is a box that never boots, and every character of
	// the fixture below except its own newline is inside a base64
	// blob that cannot contain one.
	require.NotContains(t, strings.TrimRight(got, "\n"), "\n",
		"rendered user-data must be a single physical line: the first provider's UserData field accepts nothing else")
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

	_, err := render(args, fixedSHA256, "test-tailscale-authkey", "test-tunnel-token", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "byte limit")
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
		map[string]string{"slack": "test-alert-slack-url"})
	require.NoError(t, err)
	require.Equal(t, want, got, "CloudInit must render the same thing render() does once its Pulumi inputs resolve to the same values")
}
