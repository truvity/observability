package ec2_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	statusboxec2 "github.com/truvity/observability/pkg/statusbox/ec2"
)

func dnsArgs() statusboxec2.DNSRegisterRoleArgs {
	return statusboxec2.DNSRegisterRoleArgs{
		Name:                   "box-dns-register",
		Description:            "The box registers its own record",
		BoxRoleARN:             "arn:" + part + ":iam::" + account + ":role/box-instance",
		HostedZoneID:           "Z0EXAMPLE12345",
		RecordName:             "status.example.internal",
		PermissionsBoundaryARN: "arn:" + part + ":iam::" + account + ":policy/boundary",
		ResourceNamePrefix:     "stack/",
	}
}

func TestDNSRegisterPolicyAllowsOneChangeOnly(t *testing.T) {
	var doc struct {
		Statement []struct {
			Action    string
			Resource  string
			Condition map[string]map[string][]string
		}
	}

	require.NoError(t, json.Unmarshal([]byte(statusboxec2.DNSRegisterPolicy("Z0EXAMPLE12345", "status.example.internal")), &doc))
	require.Len(t, doc.Statement, 1)

	s := doc.Statement[0]
	assert.Equal(t, "route53:ChangeResourceRecordSets", s.Action)
	assert.Equal(t, "arn:"+part+":route53:::hostedzone/Z0EXAMPLE12345", s.Resource)
	assert.Equal(t, map[string][]string{
		"route53:ChangeResourceRecordSetsNormalizedRecordNames": {"status.example.internal"},
		"route53:ChangeResourceRecordSetsRecordTypes":           {"A"},
		"route53:ChangeResourceRecordSetsActions":               {"UPSERT"},
	}, s.Condition["ForAllValues:StringEquals"])
}

func TestDNSRegisterTrustNamesTheBoxRoleOnly(t *testing.T) {
	trust := statusboxec2.DNSRegisterTrustPolicy("arn:" + part + ":iam::" + account + ":role/box-instance")
	assert.Equal(t, `{"Statement":[{"Action":"sts:AssumeRole","Effect":"Allow","Principal":{"AWS":"arn:`+part+`:iam::`+account+`:role/box-instance"}}],"Version":"2012-10-17"}`, trust)
}

func TestDNSRegisterRoleKeepsItsNames(t *testing.T) {
	rec := newRecorder()

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := aws.NewProvider(ctx, "p", &aws.ProviderArgs{})
		if err != nil {
			return err
		}

		a := dnsArgs()
		a.Provider = p

		_, err = statusboxec2.NewDNSRegisterRole(ctx, a)

		return err
	}, pulumi.WithMocks("proj", "stack", rec))
	require.NoError(t, err)

	assert.Equal(t, []string{"stack/box-dns-register"}, rec.names["aws:iam/policy:Policy"])
	assert.Equal(t, []string{"stack/box-dns-register"}, rec.names["aws:iam/role:Role"])
	assert.Equal(t, []string{"stack/box-dns-register/attachment"}, rec.names["aws:iam/rolePolicyAttachment:RolePolicyAttachment"])
}

func TestDNSRegisterRoleRefusesBadInput(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := statusboxec2.NewDNSRegisterRole(ctx, statusboxec2.DNSRegisterRoleArgs{})

		return err
	}, pulumi.WithMocks("proj", "stack", newRecorder()))
	require.Error(t, err)

	for _, want := range []string{"Name", "BoxRoleARN", "HostedZoneID", "RecordName"} {
		assert.True(t, strings.Contains(err.Error(), want), want)
	}
}
