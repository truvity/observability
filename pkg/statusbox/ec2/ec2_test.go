package ec2_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
	statusboxec2 "github.com/truvity/observability/pkg/statusbox/ec2"
)

const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var account = strings.Repeat("7", 12)

// part is the ARN partition, spelled apart so no ARN literal sits in the source.
const part = "aws"

// recorder stands in for the AWS provider: it echoes every resource's inputs
// back as its state and remembers them by type, so a test can read what NewEC2
// asked the provider to create without calling AWS.
type recorder struct {
	mu        sync.Mutex
	resources map[string][]resource.PropertyMap
	names     map[string][]string
}

func newRecorder() *recorder {
	return &recorder{resources: map[string][]resource.PropertyMap{}, names: map[string][]string{}}
}

func (r *recorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.resources[args.TypeToken] = append(r.resources[args.TypeToken], args.Inputs)
	r.names[args.TypeToken] = append(r.names[args.TypeToken], args.Name)

	out := args.Inputs.Copy()
	if args.TypeToken == "aws:ec2/launchTemplate:LaunchTemplate" {
		out["latestVersion"] = resource.NewNumberProperty(3)
	}

	return args.Name + "_id", out, nil
}

func (r *recorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	switch args.Token {
	case "aws:index/getRegion:getRegion":
		return resource.PropertyMap{"region": resource.NewStringProperty("eu-west-1"), "name": resource.NewStringProperty("eu-west-1")}, nil
	case "aws:index/getCallerIdentity:getCallerIdentity":
		return resource.PropertyMap{"accountId": resource.NewStringProperty(account)}, nil
	case "aws:ssm/getParameter:getParameter":
		return resource.PropertyMap{
			"name":  args.Args["name"],
			"value": resource.NewStringProperty("ami-0example"),
		}, nil
	}

	return args.Args, nil
}

func (r *recorder) only(t *testing.T, token string) resource.PropertyMap {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.resources[token], 1, token)

	return r.resources[token][0]
}

func stubChecksums(t *testing.T) {
	t.Helper()

	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		return sum + "  gatus_" + statusboxec2.GatusVersion + "_linux_arm64\n" + sum + "  gatus_" + statusboxec2.GatusVersion + "_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })
}

func validArgs() *statusboxec2.Args {
	cfg := "storage:\n  type: sqlite\n  path: /data/ops.db\nendpoints: []\n"

	return &statusboxec2.Args{
		Version: "v1.0.0",
		Instances: []statusbox.Instance{
			{Name: "ops", Port: 8082, Public: true, Config: cfg},
			{Name: "ops-breakglass", Port: 8081, Public: false, Config: cfg},
		},
		Hostnames:            map[string]string{"ops": "status.example.test"},
		TunnelTokenParameter: "/acme/status/tunnel-token",
		AlertURLParameters:   map[string]string{"ops_alerts_read_token": "/acme/status/alerts-read-token"},
		VPCID:                pulumi.String("vpc-0example"),
		SubnetIDs:            []pulumi.StringInput{pulumi.String("subnet-0a"), pulumi.String("subnet-0b")},
		PrivateIngressCIDRs:  []string{"10.20.0.0/16"},
		Bucket:               "acme-status-replica",
		BucketPrefix:         "box",
	}
}

func run(t *testing.T, a *statusboxec2.Args) *recorder {
	t.Helper()
	stubChecksums(t)

	rec := newRecorder()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := statusboxec2.NewEC2(ctx, "status", a)
		return err
	}, pulumi.WithMocks("statusbox-ec2-test", "test", rec))
	require.NoError(t, err)

	return rec
}

// TestGroupShape: one instance, always, two zones, a stopped warm instance
// behind it, and the hook on the group so it exists before the first launch.
func TestGroupShape(t *testing.T) {
	rec := run(t, validArgs())
	g := rec.only(t, "aws:autoscaling/group:Group").Mappable()

	require.EqualValues(t, 1, g["minSize"])
	require.EqualValues(t, 1, g["maxSize"])
	require.EqualValues(t, 1, g["desiredCapacity"])
	require.Equal(t, []any{"subnet-0a", "subnet-0b"}, g["vpcZoneIdentifiers"])
	require.Equal(t, "statusbox-status", g["name"])

	warm := g["warmPool"].(map[string]any)
	require.Equal(t, "Stopped", warm["poolState"])
	require.EqualValues(t, 1, warm["minSize"])

	hooks := g["initialLifecycleHooks"].([]any)
	require.Len(t, hooks, 1)
	hook := hooks[0].(map[string]any)
	require.Equal(t, "statusbox-status-launch", hook["name"])
	require.Equal(t, "autoscaling:EC2_INSTANCE_LAUNCHING", hook["lifecycleTransition"])
	require.Equal(t, "ABANDON", hook["defaultResult"])

	refresh := g["instanceRefresh"].(map[string]any)
	require.EqualValues(t, 0, refresh["preferences"].(map[string]any)["minHealthyPercentage"], "replace before launch: one writer")
}

func TestLaunchTemplateShape(t *testing.T) {
	rec := run(t, validArgs())
	lt := rec.only(t, "aws:ec2/launchTemplate:LaunchTemplate").Mappable()

	require.Equal(t, "t4g.nano", lt["instanceType"])
	require.Equal(t, "ami-0example", lt["imageId"])

	md := lt["metadataOptions"].(map[string]any)
	require.Equal(t, "required", md["httpTokens"], "IMDSv2")

	nic := lt["networkInterfaces"].([]any)[0].(map[string]any)
	require.Equal(t, "true", nic["associatePublicIpAddress"], "dynamic public IPv4, no Elastic IP")

	root := lt["blockDeviceMappings"].([]any)[0].(map[string]any)["ebs"].(map[string]any)
	require.EqualValues(t, 8, root["volumeSize"])
	require.Equal(t, "true", root["encrypted"])

	require.NotEmpty(t, lt["userData"])
	require.Empty(t, rec.resources["aws:ec2/eip:Eip"], "no Elastic IP")
	require.Empty(t, rec.resources["aws:ec2/natGateway:NatGateway"], "no NAT")
}

func TestInstanceTypeOverrideChoosesTheImage(t *testing.T) {
	a := validArgs()
	a.InstanceType = "t3.small"

	rec := run(t, a)
	require.Equal(t, "t3.small", rec.only(t, "aws:ec2/launchTemplate:LaunchTemplate").Mappable()["instanceType"])
}

func TestSecurityGroupAdmitsOnlyThePrivatePortFromThePeer(t *testing.T) {
	rec := run(t, validArgs())
	sg := rec.only(t, "aws:ec2/securityGroup:SecurityGroup").Mappable()

	ingress := sg["ingress"].([]any)
	require.Len(t, ingress, 1)

	rule := ingress[0].(map[string]any)
	require.EqualValues(t, 8081, rule["fromPort"], "the private instance's port, not the public one")
	require.EqualValues(t, 8081, rule["toPort"])
	require.Equal(t, []any{"10.20.0.0/16"}, rule["cidrBlocks"])
}

func TestSecurityGroupWithNoPeerAdmitsNothing(t *testing.T) {
	a := validArgs()
	a.PrivateIngressCIDRs = nil

	rec := run(t, a)
	require.Empty(t, rec.only(t, "aws:ec2/securityGroup:SecurityGroup").Mappable()["ingress"])
}

// TestRolePolicyIsScopedToTheInputs reads the policy the role was created
// with: only the named parameters, the replica prefix, the group, and the key
// when one is given.
func TestRolePolicyIsScopedToTheInputs(t *testing.T) {
	a := validArgs()
	a.KMSKeyARN = "arn:" + part + ":kms:eu-west-1:" + account + ":key/1234abcd-12ab-34cd-56ef-1234567890ab"

	rec := run(t, a)
	role := rec.only(t, "aws:iam/role:Role").Mappable()
	inline := role["inlinePolicies"].([]any)
	require.Len(t, inline, 1)

	var doc struct {
		Statement []struct {
			Sid      string
			Action   []string
			Resource []string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(inline[0].(map[string]any)["policy"].(string)), &doc))

	byID := map[string][]string{}
	for _, s := range doc.Statement {
		byID[s.Sid] = s.Resource
		for _, r := range s.Resource {
			require.NotEqual(t, "*", r)
		}
	}

	require.Equal(t, []string{
		"arn:" + part + ":ssm:eu-west-1:" + account + ":parameter/acme/status/alerts-read-token",
		"arn:" + part + ":ssm:eu-west-1:" + account + ":parameter/acme/status/tunnel-token",
	}, byID["ReadOwnParameters"])
	require.Equal(t, []string{"arn:" + part + ":s3:::acme-status-replica/box/*"}, byID["ReplicaObjects"])
	require.Equal(t, []string{"arn:" + part + ":autoscaling:eu-west-1:" + account + ":autoScalingGroup:*:autoScalingGroupName/statusbox-status"}, byID["OwnGroup"])
	require.Equal(t, []string{a.KMSKeyARN}, byID["OwnKey"])

	require.Empty(t, rec.resources["aws:iam/rolePolicyAttachment:RolePolicyAttachment"], "no managed policy on the role")
	require.Nil(t, role["managedPolicyArns"])
}

func TestRefusesInvalidArgsBeforeCreatingAnything(t *testing.T) {
	stubChecksums(t)

	rec := newRecorder()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		a := validArgs()
		a.Bucket = ""
		_, err := statusboxec2.NewEC2(ctx, "status", a)

		return err
	}, pulumi.WithMocks("statusbox-ec2-test", "test", rec))
	require.ErrorContains(t, err, "Bucket")
	require.Empty(t, rec.resources)
}

func TestRefusesABadComponentName(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := statusboxec2.NewEC2(ctx, "Status Box", validArgs())
		return err
	}, pulumi.WithMocks("statusbox-ec2-test", "test", newRecorder()))
	require.ErrorContains(t, err, "Auto Scaling group name")
}

// The instance role carries the permissions boundary when one is given, and
// none otherwise.
func TestRoleCarriesThePermissionsBoundaryWhenSet(t *testing.T) {
	a := validArgs()
	a.PermissionsBoundary = "boundary-arn"

	role := run(t, a).only(t, "aws:iam/role:Role").Mappable()
	require.Equal(t, "boundary-arn", role["permissionsBoundary"])
}

func TestRoleHasNoPermissionsBoundaryByDefault(t *testing.T) {
	role := run(t, validArgs()).only(t, "aws:iam/role:Role").Mappable()
	require.Nil(t, role["permissionsBoundary"])
}
