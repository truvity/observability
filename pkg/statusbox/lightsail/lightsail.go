// Package lightsail is the first provider pkg/statusbox is provisioned
// on: one aws.lightsail.Instance running the rendered cloud-init, with
// its firewall declared shut and a disk that survives the instance
// being replaced.
//
// It exists to be small. Every decision an estate makes about WHAT runs
// on the box — the instances, their configuration, the secrets — is
// pkg/statusbox's; this package only knows how to ask one cloud provider
// for a virtual machine and hand it that rendered string. A sibling
// package for a second provider is meant to be a similarly small amount
// of code over the same statusbox.CloudInit, which is why the AWS SDK
// stops at this package boundary and never reaches into
// pkg/statusbox itself.
package lightsail

import (
	"fmt"

	awslightsail "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lightsail"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/observability/pkg/statusbox"
)

// defaultBlueprintID, defaultBundleID and defaultDiskSizeGB are what an
// estate gets for free: a Debian base image (setup.sh is written and
// tested against Debian's apt and systemd), the smallest bundle
// Lightsail sells, and the smallest disk Lightsail sells. Several Gatus
// containers and a handful of SQLite files need neither CPU, memory nor
// disk to spare — see docs/statusbox.md ("The shape").
const (
	defaultBlueprintID = "debian_12"
	defaultBundleID    = "nano_3_0"
	defaultDiskSizeGB  = 8

	// diskPath is where the attached disk is exposed inside the
	// instance. Fixed rather than an input: setup.sh mounts it at
	// exactly this path (see setup.sh), so a caller-chosen path would be
	// a second place that has to agree with the first.
	diskPath = "/dev/xvdf"
)

// LightsailArgs is statusbox.Args plus the little this one provider
// needs beyond the provider-neutral core.
type LightsailArgs struct {
	statusbox.Args

	// AvailabilityZone is required: Lightsail is not offered in every
	// AWS Region, and unlike most of this provider's resources it has no
	// notion of "whatever the provider configuration's Region defaults
	// to" that this package could fall back to — see the provider's own
	// note on aws.lightsail.Instance. `aws lightsail get-regions
	// --include-availability-zones` lists what is available.
	AvailabilityZone string

	// BlueprintID overrides defaultBlueprintID. Leave it empty unless
	// setup.sh has been ported to and tested against a different image:
	// it assumes Debian's apt and systemd throughout.
	BlueprintID string

	// BundleID overrides defaultBundleID — a larger bundle if an estate
	// is running enough instances, or enough traffic through them, that
	// nano_3_0 is no longer enough.
	BundleID string

	// DiskSizeGB overrides defaultDiskSizeGB.
	DiskSizeGB int
}

// Box is what NewLightsail creates.
type Box struct {
	pulumi.ResourceState

	// Instance is the virtual machine itself. Its UserData is applied
	// once, at creation — see docs/statusbox.md ("Immutable, by
	// construction") — so a Pulumi update that changes anything
	// CloudInit rendered from (a different Version, a changed Config, a
	// new instance) replaces this resource rather than updating it in
	// place: about two minutes of status-page blip while Disk and
	// DiskAttachment are reattached to the new one, no history lost.
	Instance *awslightsail.Instance

	// PublicPorts is the instance's firewall, and it is created with an
	// EMPTY port list — see NewLightsail's doc comment for why that is
	// the point rather than an oversight.
	PublicPorts *awslightsail.InstancePublicPorts

	// Disk and DiskAttachment are the /data volume every Gatus
	// instance's SQLite file lives on. They are their own resources,
	// addressed by name rather than owned by Instance, for exactly the
	// reason Instance is immutable: when a config change replaces the
	// instance, Disk is untouched and DiskAttachment simply repoints at
	// whatever Instance now exists, so a box's history of what it
	// probed survives a replacement that a Config change or a Version
	// bump would otherwise cause.
	Disk           *awslightsail.Disk
	DiskAttachment *awslightsail.Disk_attachment
}

// NewLightsail creates the box: the instance, its closed firewall, and
// the disk that outlives it.
//
// The firewall is closed BY DECLARATION. aws.lightsail.InstancePublicPorts
// is not omitted — omitting it would leave whatever the provider or a
// prior apply left open, silently, as a default nobody chose — it is
// created with PortInfos set to an empty list, which Lightsail's API
// reads as "close everything." A later change to this package that adds
// a port therefore shows up as a diff on THIS resource, in review, rather
// than as a port that was simply never declared shut.
func NewLightsail(ctx *pulumi.Context, name string, a *LightsailArgs, opts ...pulumi.ResourceOption) (*Box, error) {
	if a == nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): args is nil", name)
	}
	if a.AvailabilityZone == "" {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): AvailabilityZone is empty. Lightsail is not available in every AWS Region, and this provider has no configuration-level default to fall back to — see `aws lightsail get-regions --include-availability-zones`", name)
	}

	box := &Box{}
	if err := ctx.RegisterComponentResource("statusbox:lightsail:Box", name, box, opts...); err != nil {
		return nil, err
	}
	childOpts := append([]pulumi.ResourceOption{pulumi.Parent(box)}, opts...)

	userData, err := statusbox.CloudInit(ctx, a.Args)
	if err != nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): %w", name, err)
	}

	blueprint := a.BlueprintID
	if blueprint == "" {
		blueprint = defaultBlueprintID
	}
	bundle := a.BundleID
	if bundle == "" {
		bundle = defaultBundleID
	}
	diskSize := a.DiskSizeGB
	if diskSize == 0 {
		diskSize = defaultDiskSizeGB
	}

	instance, err := awslightsail.NewInstance(ctx, name, &awslightsail.InstanceArgs{
		AvailabilityZone: pulumi.String(a.AvailabilityZone),
		BlueprintId:      pulumi.String(blueprint),
		BundleId:         pulumi.String(bundle),
		UserData:         userData.ToStringPtrOutput(),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): instance: %w", name, err)
	}
	box.Instance = instance

	publicPorts, err := awslightsail.NewInstancePublicPorts(ctx, name, &awslightsail.InstancePublicPortsArgs{
		InstanceName: instance.Name,
		PortInfos:    awslightsail.InstancePublicPortsPortInfoArray{},
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): public ports: %w", name, err)
	}
	box.PublicPorts = publicPorts

	disk, err := awslightsail.NewDisk(ctx, name+"-data", &awslightsail.DiskArgs{
		AvailabilityZone: pulumi.String(a.AvailabilityZone),
		SizeInGb:         pulumi.Int(diskSize),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): disk: %w", name, err)
	}
	box.Disk = disk

	attachment, err := awslightsail.NewDisk_attachment(ctx, name+"-data", &awslightsail.Disk_attachmentArgs{
		DiskName:     disk.Name,
		InstanceName: instance.Name,
		DiskPath:     pulumi.String(diskPath),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/lightsail: NewLightsail(%q, ...): disk attachment: %w", name, err)
	}
	box.DiskAttachment = attachment

	if err := ctx.RegisterResourceOutputs(box, pulumi.Map{}); err != nil {
		return nil, err
	}
	return box, nil
}
