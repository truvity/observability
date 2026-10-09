package ec2

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/autoscaling"
	awsec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ssm"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	// rootDeviceName is Amazon Linux 2023's root device.
	rootDeviceName = "/dev/xvda"

	// hookHeartbeatSeconds is how long a lifecycle hook waits for the instance
	// to complete it before the launch is abandoned and replaced: a first boot
	// downloads three binaries on a nano.
	hookHeartbeatSeconds = 900
)

// Box is what NewEC2 creates.
type Box struct {
	pulumi.ResourceState

	// Group is the Auto Scaling group: min = max = desired = 1, two Availability
	// Zones, a warm pool of one stopped instance.
	Group *autoscaling.Group
	// LaunchTemplate carries the image, the user-data, IMDSv2 and the root volume.
	LaunchTemplate *awsec2.LaunchTemplate
	// Role and InstanceProfile are the instance's identity; its policy is
	// scoped to the parameters, the replica prefix, the group and the key.
	Role            *iam.Role
	InstanceProfile *iam.InstanceProfile
	// SecurityGroup admits nothing in except PrivateIngressCIDRs on the
	// private instance's port.
	SecurityGroup *awsec2.SecurityGroup

	// UserData is what the launch template carries (gzip-wrapped); it holds no
	// secret.
	UserData string
}

func (a Args) invokeOptions() []pulumi.InvokeOption {
	if a.Provider == nil {
		return nil
	}

	return []pulumi.InvokeOption{pulumi.Provider(a.Provider)}
}

func (a Args) tags(name string) pulumi.StringMap {
	m := pulumi.StringMap{"Name": pulumi.String(name), "ManagedBy": pulumi.String("pulumi")}
	for k, v := range a.Tags {
		m[k] = pulumi.String(v)
	}

	return m
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

	box := &Box{UserData: userData}
	if err := ctx.RegisterComponentResource("statusbox:ec2:Box", name, box, opts...); err != nil {
		return nil, err
	}

	childOpts := append([]pulumi.ResourceOption{pulumi.Parent(box)}, opts...)
	physical := namesFor(name)

	region := aws.GetRegionOutput(ctx, aws.GetRegionOutputArgs{}, a.invokeOptions()...).Region()
	account := aws.GetCallerIdentityOutput(ctx, aws.GetCallerIdentityOutputArgs{}, a.invokeOptions()...).AccountId()

	policy := pulumi.All(region, account).ApplyT(func(v []any) string {
		return a.instancePolicy(physical, v[0].(string), v[1].(string))
	}).(pulumi.StringOutput)

	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(physical.role),
		AssumeRolePolicy: pulumi.String(assumeRolePolicy()),
		InlinePolicies: iam.RoleInlinePolicyArray{
			iam.RoleInlinePolicyArgs{Name: pulumi.String("statusbox"), Policy: policy},
		},
		Tags: a.tags(physical.role),
	}
	if a.PermissionsBoundary != "" {
		roleArgs.PermissionsBoundary = pulumi.String(a.PermissionsBoundary)
	}

	role, err := iam.NewRole(ctx, name, roleArgs, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): role: %w", name, err)
	}

	box.Role = role

	if a.SessionManager {
		if _, err := iam.NewRolePolicyAttachment(ctx, name+"-ssm-core", &iam.RolePolicyAttachmentArgs{
			Role:      role.Name,
			PolicyArn: pulumi.String("arn:" + partition + ":iam::" + partition + ":policy/AmazonSSMManagedInstanceCore"),
		}, childOpts...); err != nil {
			return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): Session Manager policy: %w", name, err)
		}
	}

	profile, err := iam.NewInstanceProfile(ctx, name, &iam.InstanceProfileArgs{
		Name: pulumi.String(physical.role),
		Role: role.Name,
		Tags: a.tags(physical.role),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): instance profile: %w", name, err)
	}

	box.InstanceProfile = profile

	sg, err := a.securityGroup(ctx, name, physical, childOpts)
	if err != nil {
		return nil, err
	}

	box.SecurityGroup = sg

	// The image is looked up from the public SSM parameter at deploy time and
	// pinned in the launch template: a newer Amazon Linux release is a visible
	// diff and a rolling refresh, never a silent change on the next launch.
	ami := ssm.LookupParameterOutput(ctx, ssm.LookupParameterOutputArgs{Name: pulumi.String(a.architecture().ssmAMI)}, a.invokeOptions()...).Value()

	lt, err := awsec2.NewLaunchTemplate(ctx, name, &awsec2.LaunchTemplateArgs{
		Name:                 pulumi.String(physical.asg),
		UpdateDefaultVersion: pulumi.Bool(true),
		ImageId:              ami,
		InstanceType:         pulumi.String(a.instanceType()),
		UserData:             pulumi.String(base64Of(userData)),
		IamInstanceProfile:   awsec2.LaunchTemplateIamInstanceProfileArgs{Arn: profile.Arn},
		BlockDeviceMappings: awsec2.LaunchTemplateBlockDeviceMappingArray{
			awsec2.LaunchTemplateBlockDeviceMappingArgs{
				DeviceName: pulumi.String(rootDeviceName),
				Ebs: awsec2.LaunchTemplateBlockDeviceMappingEbsArgs{
					VolumeSize:          pulumi.Int(a.rootVolumeGiB()),
					VolumeType:          pulumi.String("gp3"),
					Encrypted:           pulumi.String("true"),
					DeleteOnTermination: pulumi.String("true"),
				},
			},
		},
		// A dynamic public IPv4 address, no Elastic IP and no NAT: the box only
		// dials out, and nothing is admitted in (see SecurityGroup).
		NetworkInterfaces: awsec2.LaunchTemplateNetworkInterfaceArray{
			awsec2.LaunchTemplateNetworkInterfaceArgs{
				AssociatePublicIpAddress: pulumi.String("true"),
				DeviceIndex:              pulumi.Int(0),
				SecurityGroups:           pulumi.StringArray{sg.ID().ToStringOutput()},
			},
		},
		MetadataOptions: awsec2.LaunchTemplateMetadataOptionsArgs{
			HttpTokens:              pulumi.String("required"),
			HttpEndpoint:            pulumi.String("enabled"),
			HttpPutResponseHopLimit: pulumi.Int(1),
		},
		TagSpecifications: awsec2.LaunchTemplateTagSpecificationArray{
			awsec2.LaunchTemplateTagSpecificationArgs{ResourceType: pulumi.String("instance"), Tags: a.tags(physical.asg)},
			awsec2.LaunchTemplateTagSpecificationArgs{ResourceType: pulumi.String("volume"), Tags: a.tags(physical.asg)},
		},
		Tags: a.tags(physical.asg),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): launch template: %w", name, err)
	}

	box.LaunchTemplate = lt

	subnets := make(pulumi.StringArray, len(a.SubnetIDs))
	for i, s := range a.SubnetIDs {
		subnets[i] = s.ToStringOutput()
	}

	group, err := autoscaling.NewGroup(ctx, name, &autoscaling.GroupArgs{
		Name:               pulumi.String(physical.asg),
		MinSize:            pulumi.Int(1),
		MaxSize:            pulumi.Int(1),
		DesiredCapacity:    pulumi.Int(1),
		VpcZoneIdentifiers: subnets,
		LaunchTemplate: autoscaling.GroupLaunchTemplateArgs{
			Id:      lt.ID(),
			Version: lt.LatestVersion.ApplyT(func(v int) string { return fmt.Sprint(v) }).(pulumi.StringOutput),
		},
		HealthCheckType:        pulumi.String("EC2"),
		HealthCheckGracePeriod: pulumi.Int(300),
		// The hook is declared on the group itself, so it exists before the
		// first instance launches.
		InitialLifecycleHooks: autoscaling.GroupInitialLifecycleHookArray{
			autoscaling.GroupInitialLifecycleHookArgs{
				Name:                pulumi.String(physical.hook),
				LifecycleTransition: pulumi.String("autoscaling:EC2_INSTANCE_LAUNCHING"),
				HeartbeatTimeout:    pulumi.Int(hookHeartbeatSeconds),
				DefaultResult:       pulumi.String("ABANDON"),
			},
		},
		// One stopped instance, already installed, waiting. Group size 1 plus
		// this is 2 prepared instances at most.
		WarmPool: autoscaling.GroupWarmPoolArgs{
			PoolState:                pulumi.String("Stopped"),
			MinSize:                  pulumi.Int(1),
			MaxGroupPreparedCapacity: pulumi.Int(2),
		},
		// With one instance, a refresh replaces it before launching the next:
		// the replica has exactly one writer at any time.
		InstanceRefresh: autoscaling.GroupInstanceRefreshArgs{
			Strategy: pulumi.String("Rolling"),
			Preferences: autoscaling.GroupInstanceRefreshPreferencesArgs{
				MinHealthyPercentage: pulumi.Int(0),
				InstanceWarmup:       pulumi.String("300"),
			},
			Triggers: pulumi.StringArray{pulumi.String("launch_template")},
		},
		Tags: autoscaling.GroupTagArray{
			autoscaling.GroupTagArgs{Key: pulumi.String("Name"), Value: pulumi.String(physical.asg), PropagateAtLaunch: pulumi.Bool(true)},
			autoscaling.GroupTagArgs{Key: pulumi.String("ManagedBy"), Value: pulumi.String("pulumi"), PropagateAtLaunch: pulumi.Bool(true)},
		},
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): group: %w", name, err)
	}

	box.Group = group

	if err := ctx.RegisterResourceOutputs(box, pulumi.Map{}); err != nil {
		return nil, err
	}

	return box, nil
}

// securityGroup admits nothing in but PrivateIngressCIDRs to the private
// instances' ports, and lets everything out (probes, S3, SSM, the tunnel).
func (a Args) securityGroup(ctx *pulumi.Context, name string, physical names, childOpts []pulumi.ResourceOption) (*awsec2.SecurityGroup, error) {
	var ingress awsec2.SecurityGroupIngressArray

	for _, port := range a.privatePorts() {
		if len(a.PrivateIngressCIDRs) == 0 {
			break
		}

		cidrs := make(pulumi.StringArray, len(a.PrivateIngressCIDRs))
		for i, c := range a.PrivateIngressCIDRs {
			cidrs[i] = pulumi.String(c)
		}

		ingress = append(ingress, awsec2.SecurityGroupIngressArgs{
			Description: pulumi.String("the private status page, from the peered network"),
			Protocol:    pulumi.String("tcp"),
			FromPort:    pulumi.Int(port),
			ToPort:      pulumi.Int(port),
			CidrBlocks:  cidrs,
		})
	}

	sg, err := awsec2.NewSecurityGroup(ctx, name, &awsec2.SecurityGroupArgs{
		Name:        pulumi.String(physical.asg),
		Description: pulumi.String("statusbox: no inbound except the private page from the listed networks"),
		VpcId:       a.VPCID,
		Ingress:     ingress,
		Egress: awsec2.SecurityGroupEgressArray{
			awsec2.SecurityGroupEgressArgs{
				Protocol:   pulumi.String("-1"),
				FromPort:   pulumi.Int(0),
				ToPort:     pulumi.Int(0),
				CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
			},
		},
		Tags: a.tags(physical.asg),
	}, childOpts...)
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: NewEC2(%q, ...): security group: %w", name, err)
	}

	return sg, nil
}
