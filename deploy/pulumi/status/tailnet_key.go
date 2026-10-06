package status

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/pulumi/pulumi-tailscale/sdk/go/tailscale"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/truvity/observability/pkg/statusbox"
)

// tailnetKeyDescription is the tailnet key's description PREFIX; the
// generation fingerprint is appended, space-separated. Tailscale refuses
// anything but letters, digits, spaces, hyphens and underscores here (400
// "description had invalid characters"), and only at apply time -- a preview
// accepts it; it also caps the whole description at 50 characters.
const tailnetKeyDescription = "statusbox join key"

type (
	// statusboxShape is the Lightsail parameters that name a specific
	// machine, passed straight to lightsail.LightsailArgs. Named here so
	// tailnetKeyGeneration can fold them into its own fingerprint the
	// moment an estate configures any of them — today BlueprintID,
	// BundleID and DiskSizeGB are always the zero value, which asks the
	// library for its own defaults (debian_12, nano_3_0, 8GB); only
	// AvailabilityZone comes from the caller's config today.
	statusboxShape struct {
		AvailabilityZone string
		BlueprintID      string
		BundleID         string
		DiskSizeGB       int
	}
)

// tailnetKeyGeneration is a short, stable fingerprint of every input
// that drives the BOX's own replacement except the key itself: the
// observability release, each instance's rendered Gatus config (in
// Deploy's own order — ops-breakglass, then ops if the box has a public
// instance), the Lightsail parameters that name a specific machine, and
// box.generation (the caller's config), the operator's own manual lever for
// forcing a fresh box and key with no other change.
//
// Deploy appends this to tailnetKeyDescription, space-separated, as the
// TailnetKey's Description — the ONE input this package relies on to
// force the key to replace alongside the box; see that call site's own
// comment for why Description and not Tags, Expiry or any of the
// resource's other ForceNew inputs, and why no pulumi.ReplaceOnChanges
// or DeleteBeforeReplace is needed on top of it.
//
// Secrets (Secrets.TunnelToken, Secrets.AlertURLs, Secrets.Env) are NOT
// folded in: they are Pulumi Outputs resolved asynchronously, not plain
// strings this synchronous fingerprint could read, and — unlike a
// Version bump or a catalog change — they rotate independently of a
// deploy rather than as part of one.
//
// A short sha256 prefix, not the full digest: the key's own Description
// has a 50-character budget (Tailscale's tailnet_key resource,
// github.com/tailscale/terraform-provider-tailscale
// tailscale/resource_tailnet_key.go: stringvalidator.LengthAtMost(50)),
// and tailnetKeyDescription's own fixed prose already spends part of
// it.
func tailnetKeyGeneration(observabilityVersion string, instances []statusbox.Instance, shape statusboxShape, boxGeneration int) string {
	// sha256.digest.Write never returns an error (crypto/sha256's own
	// contract, like every hash.Hash) — errors are deliberately
	// discarded rather than propagated through a signature that would
	// otherwise never actually fail.
	h := sha256.New()
	_, _ = fmt.Fprintln(h, observabilityVersion)

	for _, inst := range instances {
		_, _ = fmt.Fprintln(h, inst.Name, inst.Config)
	}

	_, _ = fmt.Fprintln(h, shape.AvailabilityZone, shape.BlueprintID, shape.BundleID, shape.DiskSizeGB)
	_, _ = fmt.Fprintln(h, boxGeneration)

	return hex.EncodeToString(h.Sum(nil))[:12]
}

// tailnetKeyArgs is the tailnet key's own input set: a plain function
// over generation (tailnetKeyGeneration's output) so it is testable
// without a pulumi.Context — tailscale.TailnetKeyArgs needs none to
// construct, only tailscale.NewTailnetKey does, to register the
// resource. See that call site in Deploy for why each field is set the
// way it is (Reusable/Ephemeral/RecreateIfInvalid, Description).
func tailnetKeyArgs(generation, tag string) *tailscale.TailnetKeyArgs {
	return &tailscale.TailnetKeyArgs{
		Reusable:      pulumi.Bool(false),
		Ephemeral:     pulumi.Bool(true),
		Preauthorized: pulumi.Bool(true),
		Description:   pulumi.String(tailnetKeyDescription + " " + generation),
		Tags:          pulumi.StringArray{pulumi.String(tag)},
	}
}
