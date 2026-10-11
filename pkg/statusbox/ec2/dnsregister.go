package ec2

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DNSRegisterRoleArgs is the role the box assumes to register its own private
// IP (SelfRegisterArgs.RoleARN names it). The role lives in the account of the
// hosted zone, which is usually not the box's.
//
// It trusts exactly the box's instance role (an exact ARN, no wildcard) and may
// do one thing: route53:ChangeResourceRecordSets on one hosted zone, for one
// record name, type A, action UPSERT. The three condition keys are
// ForAllValues, so a change batch that touches any other name, type or action
// is refused. route53:GetChange is not granted: the box does not wait for the
// change to propagate.
type DNSRegisterRoleArgs struct {
	// Name is the role's and its managed policy's name.
	Name string
	// Description is the managed policy's description. It is part of the
	// policy's identity (a change replaces it), so a caller adopting an
	// existing policy passes the text it was created with.
	Description string
	// BoxRoleARN is the box's instance role, the one principal trusted.
	BoxRoleARN string
	// HostedZoneID and RecordName are the one zone and record the role may change.
	HostedZoneID string
	RecordName   string
	// PermissionsBoundaryARN is the role's boundary; empty sets none.
	PermissionsBoundaryARN string
	// ResourceNamePrefix is put before the Pulumi names of the policy, the role
	// and the attachment ("<prefix><Name>", "<prefix><Name>/attachment"), so a
	// stack that created them under a prefix keeps its URNs.
	ResourceNamePrefix string
	// Provider is the AWS provider of the hosted zone's account.
	Provider *aws.Provider
}

func (a DNSRegisterRoleArgs) validate() error {
	var errs []error

	if a.Name == "" {
		errs = append(errs, errors.New("statusbox/ec2: DNSRegisterRoleArgs.Name is required"))
	}

	if !roleARNRE.MatchString(a.BoxRoleARN) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: DNSRegisterRoleArgs.BoxRoleARN %q is not a role ARN", a.BoxRoleARN))
	}

	if !hostedZoneRE.MatchString(a.HostedZoneID) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: DNSRegisterRoleArgs.HostedZoneID %q is not a hosted zone ID", a.HostedZoneID))
	}

	if !recordNameRE.MatchString(a.RecordName) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: DNSRegisterRoleArgs.RecordName %q is not a lower-case DNS name without a trailing dot", a.RecordName))
	}

	return errors.Join(errs...)
}

// DNSRegisterTrustPolicy is the role's trust policy: the box's instance role.
func DNSRegisterTrustPolicy(boxRoleARN string) string {
	return compactJSON(map[string]any{
		"Version": iamVersion,
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"AWS": boxRoleARN},
			"Action":    "sts:AssumeRole",
		}},
	})
}

// DNSRegisterPolicy is the role's permission policy.
func DNSRegisterPolicy(hostedZoneID, recordName string) string {
	return compactJSON(map[string]any{
		"Version": iamVersion,
		"Statement": []map[string]any{{
			"Sid":      "StatusBoxRegistersItsOwnRecord",
			"Effect":   "Allow",
			"Action":   "route53:ChangeResourceRecordSets",
			"Resource": "arn:" + partition + ":route53:::hostedzone/" + hostedZoneID,
			"Condition": map[string]any{
				"ForAllValues:StringEquals": map[string]any{
					"route53:ChangeResourceRecordSetsNormalizedRecordNames": []string{recordName},
					"route53:ChangeResourceRecordSetsRecordTypes":           []string{"A"},
					"route53:ChangeResourceRecordSetsActions":               []string{"UPSERT"},
				},
			},
		}},
	})
}

// NewDNSRegisterRole creates the managed policy, the role and the attachment
// between them.
func NewDNSRegisterRole(ctx *pulumi.Context, a DNSRegisterRoleArgs) (*iam.Role, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}

	var opts []pulumi.ResourceOption
	if a.Provider != nil {
		opts = append(opts, pulumi.Provider(a.Provider))
	}

	policy, err := iam.NewPolicy(ctx, a.ResourceNamePrefix+a.Name, &iam.PolicyArgs{
		Name:        pulumi.String(a.Name),
		Description: pulumi.String(a.Description),
		Policy:      pulumi.String(DNSRegisterPolicy(a.HostedZoneID, a.RecordName)),
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("dns register policy: %w", err)
	}

	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(a.Name),
		AssumeRolePolicy: pulumi.String(DNSRegisterTrustPolicy(a.BoxRoleARN)),
	}
	if a.PermissionsBoundaryARN != "" {
		roleArgs.PermissionsBoundary = pulumi.String(a.PermissionsBoundaryARN)
	}

	role, err := iam.NewRole(ctx, a.ResourceNamePrefix+a.Name, roleArgs, opts...)
	if err != nil {
		return nil, fmt.Errorf("dns register role: %w", err)
	}

	if _, err := iam.NewRolePolicyAttachment(ctx, a.ResourceNamePrefix+a.Name+"/attachment", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: policy.Arn,
	}, opts...); err != nil {
		return nil, fmt.Errorf("dns register attachment: %w", err)
	}

	return role, nil
}

// compactJSON is the one-line form: the policy text is a Pulumi input, so a
// stack that created the role with a compact document must see no diff.
func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("statusbox/ec2: marshal policy: %v", err))
	}

	return string(b)
}
