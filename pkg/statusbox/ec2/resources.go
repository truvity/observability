package ec2

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/truvity/asg"
	"github.com/truvity/asg/bootstrap"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
)

const (
	// hookHeartbeatSeconds is how long a lifecycle hook waits for the instance
	// to complete it before the launch is abandoned and replaced: a first boot
	// downloads three binaries on a nano.
	hookHeartbeatSeconds = 900

	// warmPoolMaxPrepared caps the prepared instances: the group's one plus the
	// one stopped instance waiting.
	warmPoolMaxPrepared = 2

	// sgDeleteTimeout bounds the provider's retry of DependencyViolation when
	// the replaced group is deleted. The old group is deleted at the end of the
	// update, while the instance refresh (about 8 minutes, the warm pool too)
	// is still terminating the instances that hold interfaces on it.
	sgDeleteTimeout = "30m"

	// privatePageDescription is the security group rule of a private page.
	privatePageDescription = "the private status page, from the peered network"
)

// Box is what NewEC2 creates. It is the appliance of github.com/truvity/asg,
// registered as the component "statusbox:ec2:Box", plus what
// belongs to the status box alone.
type Box struct {
	*asg.Appliance

	// Group (promoted from the appliance) is the Auto Scaling group: min = max
	// = desired = 1, two Availability Zones, a warm pool of one stopped
	// instance. LaunchTemplate carries the image, the user-data, IMDSv2 and the
	// root volume. Role and InstanceProfile are the instance's identity; its
	// policy is scoped to the parameters, the replica prefix, the group and the
	// key. SecurityGroup admits nothing in except PrivateIngressCIDRs on the
	// private instance's port, and SSH.IngressCIDRs on TCP 22 when SSH is set.

	// ConfigObjects are the S3 objects holding the rendered Gatus
	// configurations (and the extra CA bundle): content-addressed, in
	// <Bucket>/<BucketPrefix>/config/. The box fetches them at its first boot.
	ConfigObjects []*s3.BucketObjectv2

	// UserData is what the launch template carries (gzip-wrapped); it holds no
	// secret.
	UserData string
}

// privatePorts are the ports of the instances that are not Public.
func (a Args) privatePorts() []int {
	var ports []int

	for _, inst := range a.Instances {
		if !inst.Public {
			ports = append(ports, inst.Port)
		}
	}

	return ports
}

// NewEC2 creates the box. See the package comment and docs/statusbox.md.
//
// The user-data is applied at launch, so a change to anything it renders from
// (a Version, a Config, a parameter name, the Litestream pin) is a new launch
// template version, which the group's instance refresh rolls out: the one
// instance is replaced and restores its databases from the replica.
//
// The resources are built by github.com/truvity/asg. The
// user-data goes through bootstrap.Verbatim and the role's policy through
// IAM.Policy, the options that carry an existing program over without a
// change; every logical name, type and parent is the module's own (see
// resourceset_test.go).
func NewEC2(ctx *pulumi.Context, name string, a *Args, opts ...pulumi.ResourceOption) (*Box, error) {
	if a == nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): args is nil", name)
	}

	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): the name becomes the Auto Scaling group name and must match %s", name, nameRE)
	}

	userData, err := a.UserData(name)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): %w", name, err)
	}

	rendered, err := bootstrap.Verbatim(userData, userDataLimit)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): %w", name, err)
	}

	app, err := asg.New(ctx, name, a.applianceArgs(name, rendered), opts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): %w", name, err)
	}

	// The objects are the appliance's Extra ones, after the user-data's own
	// (there are none: the user-data is verbatim).
	return &Box{Appliance: app, ConfigObjects: app.Objects, UserData: userData}, nil
}

// applianceArgs maps the box onto the appliance library the way step A of its
// migration describes: the old user-data verbatim, the old policy document, and
// the Compat options that keep every name where it was.
func (a Args) applianceArgs(name string, userData *bootstrap.Rendered) *asg.Args {
	physical := namesFor(name)

	// The objects the user-data names by digest. They are the box's own, so
	// they are Extra: the library uploads them and the role (whose policy is
	// the box's own document) reads them under the replica prefix.
	var (
		extra    []bootstrap.Object
		logical  = map[string]string{}
		tagNames = map[string]string{}
	)

	for _, o := range a.bucketObjects() {
		sum := o.SHA256()

		extra = append(extra, bootstrap.Object{Key: o.Key(a.BucketPrefix), SHA256: sum, Content: []byte(o.Content)})
		logical[sum] = name + "-" + o.resourceSuffix()
		tagNames[sum] = name + "-" + o.Name
	}

	yes := true
	maxPrepared := warmPoolMaxPrepared

	args := &asg.Args{
		Names:     asg.Names{Base: physical.asg, Role: physical.role, Hook: physical.hook},
		Partition: partition,
		Tags:      a.Tags,
		Provider:  a.Provider,
		UserData:  userData,
		Objects: asg.ObjectsArgs{
			Bucket:    a.Bucket,
			KMSKeyARN: a.KMSKeyARN,
			Extra:     extra,
			Tags: func(o bootstrap.Object) map[string]string {
				tags := map[string]string{"Name": tagNames[o.SHA256], "ManagedBy": "pulumi"}
				for k, v := range a.Tags {
					tags[k] = v
				}

				return tags
			},
		},
		Image: asg.ImageArgs{SSMParameter: a.architecture().ssmAMI},
		Instance: asg.InstanceArgs{
			Type:       a.instanceType(),
			RootVolume: &asg.RootVolumeArgs{SizeGiB: a.rootVolumeGiB()},
			// A dynamic public IPv4 address, no Elastic IP and no NAT: the box
			// only dials out, and nothing is admitted in (see SecurityGroup).
			PublicIP: &yes,
		},
		Group:    asg.GroupArgs{SubnetIDs: a.SubnetIDs},
		Hook:     &asg.HookArgs{HeartbeatTimeoutSeconds: hookHeartbeatSeconds},
		WarmPool: &asg.WarmPoolArgs{MinSize: 1, MaxGroupPreparedCapacity: &maxPrepared},
		// With one instance, a refresh replaces it before launching the next:
		// the replica has exactly one writer at any time.
		Refresh: &asg.RefreshArgs{},
		IAM: asg.IAMArgs{
			PermissionsBoundary: a.PermissionsBoundary,
			PolicyName:          "statusbox",
			// Step A: the module's own document, not the library's generated
			// one. Step B replaces it with a generated least-privilege policy.
			Policy: func(pc asg.PolicyContext) (string, error) {
				return a.instancePolicy(physical, pc.Region, pc.Account), nil
			},
		},
		Security: asg.SecurityArgs{
			VPCID:         a.VPCID,
			Description:   a.securityGroupDescription(),
			Ingress:       a.securityGroupIngress(),
			DeleteTimeout: sgDeleteTimeout,
		},
		Compat: asg.Compat{
			ComponentType:          "statusbox:ec2:Box",
			GroupTagsExcludeCommon: true,
			Names: asg.Logical{
				ManagedPolicy: func(int, string) string { return name + "-ssm-core" },
				Object:        func(o bootstrap.Object) string { return logical[o.SHA256] },
			},
		},
	}

	if a.SessionManager {
		args.IAM.ManagedPolicyARNs = []string{"arn:" + partition + ":iam::" + partition + ":policy/AmazonSSMManagedInstanceCore"}
	}

	return args
}

// securityGroupDescription is the group's description. A description cannot
// change in place, so it moves only for a box that turns SSH on (that box's
// group is replaced, create first: see asg.Names.SecurityGroup).
func (a Args) securityGroupDescription() string {
	if len(a.sshCIDRs()) > 0 {
		return "statusbox: no inbound except the private page and SSH (opkssh) from the listed networks"
	}

	return "statusbox: no inbound except the private page from the listed networks"
}

// securityGroupIngress admits nothing in but PrivateIngressCIDRs to the private
// instances' ports and, with SSH, SSH.IngressCIDRs to TCP 22. Everything is let
// out (probes, S3, SSM, the tunnel).
func (a Args) securityGroupIngress() []asg.Rule {
	var rules []asg.Rule

	if len(a.PrivateIngressCIDRs) > 0 {
		for _, port := range a.privatePorts() {
			rules = append(rules, asg.Rule{
				Description: privatePageDescription,
				Protocol:    "tcp",
				FromPort:    port,
				ToPort:      port,
				CIDRs:       a.PrivateIngressCIDRs,
			})
		}
	}

	if cidrs := a.sshCIDRs(); len(cidrs) > 0 {
		rules = append(rules, asg.Rule{
			Description: "SSH (opkssh), from the listed networks",
			Protocol:    "tcp",
			FromPort:    sshPort,
			ToPort:      sshPort,
			CIDRs:       cidrs,
		})
	}

	return rules
}
