package status

import (
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicSubnetIDsAreInZoneOrderAndNeedTwo(t *testing.T) {
	_, err := PublicSubnetIDs(map[string]string{"zone-a": "subnet-a"})
	require.ErrorContains(t, err, "two or more")

	ids, err := PublicSubnetIDs(map[string]string{"zone-b": "subnet-b", "zone-a": "subnet-a", "zone-c": "subnet-c"})
	require.NoError(t, err)
	require.Len(t, ids, 3)
	assert.Equal(t, pulumi.String("subnet-a"), ids[0])
	assert.Equal(t, pulumi.String("subnet-b"), ids[1])
	assert.Equal(t, pulumi.String("subnet-c"), ids[2])
}

func TestLitestreamBucketIsPrivateVersionedEncryptedAndExpiring(t *testing.T) {
	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		name, err := LitestreamBucket(c, testProvider(t, c), LitestreamBucketSpec{Name: "replica-bucket", Prefix: "box"})
		require.NoError(t, err)
		assert.Equal(t, "replica-bucket", name)

		return nil
	}, pulumi.WithMocks("proj", "stack", m)))

	for _, typ := range []string{
		"aws:s3/bucketV2:BucketV2",
		"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock",
		"aws:s3/bucketVersioningV2:BucketVersioningV2",
		"aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2",
		"aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2",
	} {
		assert.Equal(t, []string{DefaultLitestreamResourceName}, m.names(typ), typ)
	}

	assert.Equal(t, "replica-bucket", m.input("aws:s3/bucketV2:BucketV2", DefaultLitestreamResourceName, "bucket"))
}

func TestLitestreamBucketKeepsACallersResourceName(t *testing.T) {
	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		_, err := LitestreamBucket(c, testProvider(t, c), LitestreamBucketSpec{Name: "b", Prefix: "p", ResourceName: "mine", NoncurrentDays: 9})
		return err
	}, pulumi.WithMocks("proj", "stack", m)))

	assert.Equal(t, []string{"mine"}, m.names("aws:s3/bucketV2:BucketV2"))
	assert.Equal(t, []string{"mine"}, m.names("aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2"))
}

func TestLitestreamBucketNeedsANameAndAPrefix(t *testing.T) {
	err := pulumi.RunErr(func(c *pulumi.Context) error {
		_, err := LitestreamBucket(c, testProvider(t, c), LitestreamBucketSpec{Name: "b"})
		return err
	}, pulumi.WithMocks("proj", "stack", &mocks{}))
	require.ErrorContains(t, err, "Name and a Prefix")
}

func TestPutSecureParametersNamesEachResourceWithoutThePrefix(t *testing.T) {
	m := &mocks{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		return PutSecureParameters(c, testProvider(t, c), "/box/", "copied on deploy", []SecureParameter{
			{Name: "/box/token", Value: "v1"},
			{Name: "/box/chat-id", Value: "v2"},
		})
	}, pulumi.WithMocks("proj", "stack", m)))

	assert.Equal(t, []string{"param-chat-id", "param-token"}, m.names("aws:ssm/parameter:Parameter"))
	assert.Equal(t, "/box/token", m.input("aws:ssm/parameter:Parameter", "param-token", "name"))
	assert.Equal(t, "SecureString", m.input("aws:ssm/parameter:Parameter", "param-token", "type"))
}

func testProvider(t *testing.T, c *pulumi.Context) *aws.Provider {
	t.Helper()

	p, err := aws.NewProvider(c, "box", &aws.ProviderArgs{Region: pulumi.String("eu-west-3")})
	require.NoError(t, err)

	return p
}
