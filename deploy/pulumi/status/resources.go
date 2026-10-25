package status

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ssm"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The helpers a caller of Deploy needs around it: the box's replica bucket, the
// SecureStrings its boot reads, and the subnets it lands in. None of them knows
// an estate; the caller passes every name.

const (
	// DefaultLitestreamResourceName is the Pulumi resource name
	// LitestreamBucket gives the bucket and each of its configuration
	// resources when LitestreamBucketSpec.ResourceName is empty.
	DefaultLitestreamResourceName = "statusbox-litestream"

	// DefaultNoncurrentDays is how long a replaced Litestream generation's
	// versions are kept when LitestreamBucketSpec.NoncurrentDays is zero.
	DefaultNoncurrentDays = 30

	abortMultipartDays = 7
)

type (
	// LitestreamBucketSpec describes the box's replica bucket.
	LitestreamBucketSpec struct {
		// Name is the S3 bucket name (globally unique; the caller builds it).
		Name string
		// Prefix is the Litestream replica prefix inside the bucket, without
		// a trailing slash: the module appends the instance name. The
		// lifecycle rule is scoped to it.
		Prefix string
		// ResourceName is the Pulumi name of the bucket and of each resource
		// attached to it. Empty means DefaultLitestreamResourceName. A caller
		// that already deployed the bucket keeps its name, so the URNs do not
		// move.
		ResourceName string
		// NoncurrentDays keeps a replaced generation's versions for this many
		// days, then lets them go: the bucket is versioned, the history is not
		// a record. Zero means DefaultNoncurrentDays.
		NoncurrentDays int
	}

	// SecureParameter is one SSM SecureString the box reads at boot.
	SecureParameter struct {
		// Name is the full parameter name, e.g. "/statusbox/ping-url". It
		// must be one of the names the module's role is scoped to
		// (EC2Inputs.*Parameter).
		Name string
		// Value is the secret. It reaches Pulumi as a secret and never a log
		// line or an error.
		Value string
	}
)

// LitestreamBucket creates the box's replica bucket: private, versioned,
// encrypted (SSE-S3), with a lifecycle rule that expires noncurrent versions
// and abandoned multipart uploads under the prefix. It returns the bucket name,
// the value of EC2Inputs.Bucket. The module's role is scoped to the prefix.
func LitestreamBucket(c *pulumi.Context, provider *aws.Provider, spec LitestreamBucketSpec) (string, error) {
	if spec.Name == "" || spec.Prefix == "" {
		return "", fmt.Errorf("status: LitestreamBucket needs a Name and a Prefix")
	}

	res := spec.ResourceName
	if res == "" {
		res = DefaultLitestreamResourceName
	}

	days := spec.NoncurrentDays
	if days == 0 {
		days = DefaultNoncurrentDays
	}

	opt := pulumi.Provider(provider)

	bucket, err := s3.NewBucketV2(c, res, &s3.BucketV2Args{Bucket: pulumi.String(spec.Name)}, opt)
	if err != nil {
		return "", fmt.Errorf("status: create bucket: %w", err)
	}

	if _, err := s3.NewBucketPublicAccessBlock(c, res, &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, opt); err != nil {
		return "", fmt.Errorf("status: block public access: %w", err)
	}

	if _, err := s3.NewBucketVersioningV2(c, res, &s3.BucketVersioningV2Args{
		Bucket:                  bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{Status: pulumi.String("Enabled")},
	}, opt); err != nil {
		return "", fmt.Errorf("status: enable versioning: %w", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(c, res, &s3.BucketServerSideEncryptionConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
			&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
				ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
					SseAlgorithm: pulumi.String("AES256"),
				},
			},
		},
	}, opt); err != nil {
		return "", fmt.Errorf("status: encrypt bucket: %w", err)
	}

	if _, err := s3.NewBucketLifecycleConfigurationV2(c, res, &s3.BucketLifecycleConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketLifecycleConfigurationV2RuleArray{
			&s3.BucketLifecycleConfigurationV2RuleArgs{
				Id:     pulumi.String("noncurrent"),
				Status: pulumi.String("Enabled"),
				Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{Prefix: pulumi.String(spec.Prefix + "/")},
				NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationV2RuleNoncurrentVersionExpirationArgs{
					NoncurrentDays: pulumi.Int(days),
				},
				AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationV2RuleAbortIncompleteMultipartUploadArgs{
					DaysAfterInitiation: pulumi.Int(abortMultipartDays),
				},
			},
		},
	}, opt); err != nil {
		return "", fmt.Errorf("status: bucket lifecycle: %w", err)
	}

	return spec.Name, nil
}

// PutSecureParameters writes SecureStrings in the box's own account under the
// default AWS-managed key, as the secrets the box reads at boot. Each resource
// is named "param-" plus the parameter name without `prefix` (the caller's
// parameter prefix, e.g. "/statusbox/"). description is the parameters' SSM
// description.
func PutSecureParameters(c *pulumi.Context, provider *aws.Provider, prefix, description string, params []SecureParameter) error {
	for _, p := range params {
		_, err := ssm.NewParameter(c, "param-"+strings.TrimPrefix(p.Name, prefix), &ssm.ParameterArgs{
			Name:        pulumi.String(p.Name),
			Type:        pulumi.String("SecureString"),
			Value:       pulumi.ToSecret(pulumi.String(p.Value)).(pulumi.StringOutput),
			Description: pulumi.String(description),
		}, pulumi.Provider(provider))
		if err != nil {
			return fmt.Errorf("status: create parameter %s: %w", p.Name, err)
		}
	}

	return nil
}

// PublicSubnetIDs orders a VPC's public subnets (zone -> subnet id) by zone
// name, the order EC2Inputs.SubnetIDs wants; the module needs two or more
// zones.
func PublicSubnetIDs(byZone map[string]string) ([]pulumi.StringInput, error) {
	if len(byZone) < 2 {
		return nil, fmt.Errorf("status: %d public subnets, want two or more (one per zone)", len(byZone))
	}

	zones := make([]string, 0, len(byZone))
	for zone := range byZone {
		zones = append(zones, zone)
	}

	sort.Strings(zones)

	ids := make([]pulumi.StringInput, 0, len(zones))
	for _, zone := range zones {
		ids = append(ids, pulumi.String(byZone[zone]))
	}

	return ids, nil
}
