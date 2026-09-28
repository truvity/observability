package lightsail

// White-box test: this file is `package lightsail`, not
// `package lightsail_test`, because diskAttachmentOptions is unexported.
// It exists because Pulumi's own test mocks (see lightsail_test.go)
// cannot prove this option is set: pulumi.DeleteBeforeReplace is a
// resource-registration option, not a resource input, so it never
// reaches MockResourceArgs — the RunErr-and-inspect-PortInfos pattern
// the other tests use has nothing to inspect here. pulumi.NewResourceOptions
// is Pulumi's own documented way to get a read-only snapshot of the
// effect of a list of options "inside mocks and component resources"
// without standing up a program at all, so this test uses that directly.

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

// TestDiskAttachmentOptionsSetsDeleteBeforeReplace proves DiskAttachment
// is registered with pulumi.DeleteBeforeReplace(true). Without it,
// Pulumi's default create-before-delete order tries to attach the disk
// to the new instance while the old DiskAttachment (the same disk,
// same DiskName) still holds it, and Lightsail refuses with
// "AttachDisk ... the state of this disk is: in-use" — see
// diskAttachmentOptions's doc comment in lightsail.go for the full
// mechanism.
func TestDiskAttachmentOptionsSetsDeleteBeforeReplace(t *testing.T) {
	opts, err := pulumi.NewResourceOptions(diskAttachmentOptions(nil)...)
	require.NoError(t, err)
	require.True(t, opts.DeleteBeforeReplace, "DiskAttachment must be deleted before its replacement is created, or replacing the box fails attaching the still-attached disk")
}

// TestDiskAttachmentOptionsKeepsCallerOptions proves
// diskAttachmentOptions is additive: it must not drop whatever the
// caller already asked for (in NewLightsail's case, at least
// pulumi.Parent(box)) while adding DeleteBeforeReplace.
func TestDiskAttachmentOptionsKeepsCallerOptions(t *testing.T) {
	sentinel := pulumi.AdditionalSecretOutputs([]string{"diskPath"})
	opts, err := pulumi.NewResourceOptions(diskAttachmentOptions([]pulumi.ResourceOption{sentinel})...)
	require.NoError(t, err)
	require.True(t, opts.DeleteBeforeReplace)
	require.Equal(t, []string{"diskPath"}, opts.AdditionalSecretOutputs, "diskAttachmentOptions must preserve options the caller already set, not replace them")
}
