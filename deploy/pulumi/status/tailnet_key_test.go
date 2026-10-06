package status

import (
	"regexp"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/truvity/observability/pkg/statusbox"
)

func baseGenerationInputs() (string, []statusbox.Instance, statusboxShape, int) {
	return "v0.7.5",
		[]statusbox.Instance{
			{Name: privateInstanceName, Port: PrivatePort, Public: false, Config: "breakglass: config"},
			{Name: publicInstanceName, Port: PublicPort, Public: true, Config: "public: config"},
		},
		statusboxShape{AvailabilityZone: "eu-west-3a", BlueprintID: "debian_12", BundleID: "nano_3_0", DiskSizeGB: 8},
		0
}

// Tailscale refuses a key description outside this set, and only at apply time.
func TestTailnetKeyDescriptionIsAcceptedByTailscale(t *testing.T) {
	descriptionRE := regexp.MustCompile(`^[A-Za-z0-9 _-]{1,50}$`)

	assert.Regexp(t, descriptionRE, tailnetKeyDescription)

	full := tailnetKeyDescription + " " + tailnetKeyGeneration("v0.7.5", nil, statusboxShape{}, 0)
	assert.Regexp(t, descriptionRE, full, "prefix + generation must still fit Tailscale's own charset and 50-char budget")
}

// TestTailnetKeyGenerationStableOtherwise pins the fingerprint's
// determinism: the SAME inputs produce the SAME generation, run after
// run.
func TestTailnetKeyGenerationStableOtherwise(t *testing.T) {
	version, instances, shape, gen := baseGenerationInputs()

	a := tailnetKeyGeneration(version, instances, shape, gen)
	b := tailnetKeyGeneration(version, instances, shape, gen)

	assert.Equal(t, a, b)
	assert.NotEmpty(t, a)
}

// TestTailnetKeyGenerationChangesWithVersion pins D2: a truvity/
// observability version bump must mint a fresh key alongside the box it
// replaces.
func TestTailnetKeyGenerationChangesWithVersion(t *testing.T) {
	_, instances, shape, gen := baseGenerationInputs()

	a := tailnetKeyGeneration("v0.7.5", instances, shape, gen)
	b := tailnetKeyGeneration("v0.7.6", instances, shape, gen)

	assert.NotEqual(t, a, b)
}

// TestTailnetKeyGenerationChangesWithRenderedConfig pins D2: a catalog
// change that alters what an instance's Gatus config renders — with no
// Version bump at all — still replaces the box (deploy.go's own
// Instances literal is what CloudInit renders from), so it must still
// mint a fresh key.
func TestTailnetKeyGenerationChangesWithRenderedConfig(t *testing.T) {
	version, instances, shape, gen := baseGenerationInputs()

	changed := append([]statusbox.Instance(nil), instances...)
	changed[0].Config = "breakglass: a different config"

	a := tailnetKeyGeneration(version, instances, shape, gen)
	b := tailnetKeyGeneration(version, changed, shape, gen)

	assert.NotEqual(t, a, b)
}

// TestTailnetKeyGenerationChangesWithBoxShape pins D2: the Lightsail
// parameters that name a specific machine (availability zone here) are
// folded in too.
func TestTailnetKeyGenerationChangesWithBoxShape(t *testing.T) {
	version, instances, shape, gen := baseGenerationInputs()

	changed := shape
	changed.AvailabilityZone = "eu-west-3b"

	a := tailnetKeyGeneration(version, instances, shape, gen)
	b := tailnetKeyGeneration(version, instances, changed, gen)

	assert.NotEqual(t, a, b)
}

// TestTailnetKeyGenerationChangesWithBoxGeneration pins the manual
// lever (the caller's config box.generation): an operator can force a fresh
// box and key with NO other input changing at all.
func TestTailnetKeyGenerationChangesWithBoxGeneration(t *testing.T) {
	version, instances, shape, _ := baseGenerationInputs()

	a := tailnetKeyGeneration(version, instances, shape, 0)
	b := tailnetKeyGeneration(version, instances, shape, 1)

	assert.NotEqual(t, a, b)
}

// TestTailnetKeyIsEphemeralAndSingleUse pins D2: a NEW, ephemeral,
// single-use key per box instance — Reusable stays false (never
// recreated on its own; see the mint call site's own comment on
// RecreateIfInvalid), Ephemeral is true (the device itself leaves the
// tailnet once a replaced box stops renewing its session), and
// RecreateIfInvalid is left unset rather than "always".
func TestTailnetKeyIsEphemeralAndSingleUse(t *testing.T) {
	args := tailnetKeyArgs("abc123", "tag:box")

	assert.Equal(t, pulumi.Bool(false), args.Reusable)
	assert.Equal(t, pulumi.Bool(true), args.Ephemeral)
	assert.Equal(t, pulumi.Bool(true), args.Preauthorized)
	assert.Nil(t, args.RecreateIfInvalid, "recreate_if_invalid must stay unset, never \"always\" — see the mint call site's own comment")
	assert.Equal(t, pulumi.StringArray{pulumi.String("tag:box")}, args.Tags)
}

// TestTailnetKeyArgsDescriptionCarriesTheGeneration pins the mechanism
// D2 relies on to force a fresh key: the generation fingerprint lands in
// Description, the one input the provider marks ForceNew that this
// package also controls end to end.
func TestTailnetKeyArgsDescriptionCarriesTheGeneration(t *testing.T) {
	a := tailnetKeyArgs("aaaaaaaaaaaa", "tag:box")
	b := tailnetKeyArgs("bbbbbbbbbbbb", "tag:box")

	assert.NotEqual(t, a.Description, b.Description)
	assert.Equal(t, pulumi.String(tailnetKeyDescription+" aaaaaaaaaaaa"), a.Description)
}
