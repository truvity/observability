package auditlogreader

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNames(t *testing.T) {
	assert.Equal(t, "c1-observability-audit-reader", RoleName("c1"))
	assert.Equal(t, "/aws/eks/c1/cluster", LogGroupName("c1"))
}

func TestPolicyIsReadOnlyOnTheOwnGroup(t *testing.T) {
	account := strings.Repeat("1", 12)

	doc, err := Policy("c1", "eu-west-1", account)
	require.NoError(t, err)

	var p struct {
		Statement []struct {
			Sid      string
			Effect   string
			Action   []string
			Resource any
		}
	}
	require.NoError(t, json.Unmarshal([]byte(doc), &p))
	require.Len(t, p.Statement, 2)

	prefix := "arn:" + "aws" + ":logs:eu-west-1:" + account + ":log-group:"

	assert.Equal(t, "ListLogGroups", p.Statement[0].Sid)
	assert.Equal(t, prefix+"*", p.Statement[0].Resource)
	assert.Equal(t, []string{"logs:DescribeLogGroups"}, p.Statement[0].Action)

	assert.Equal(t, "ReadControlPlaneLog", p.Statement[1].Sid)
	assert.Equal(t, []any{prefix + "/aws/eks/c1/cluster", prefix + "/aws/eks/c1/cluster:*"}, p.Statement[1].Resource)
	assert.ElementsMatch(t, []string{"logs:DescribeLogStreams", "logs:FilterLogEvents", "logs:GetLogEvents"}, p.Statement[1].Action)

	for _, s := range p.Statement {
		assert.Equal(t, "Allow", s.Effect)
	}
}

func TestInputsAreValidated(t *testing.T) {
	err := Inputs{}.validate()
	require.Error(t, err)

	for _, want := range []string{"ClusterName", "EKSClusterName", "Provider", "Region", "AccountID"} {
		assert.Contains(t, err.Error(), want)
	}

	ok := Inputs{ClusterName: "c", EKSClusterName: pulumi.String("c"), Region: "r", AccountID: "a", Provider: &aws.Provider{}}
	assert.NoError(t, ok.validate())
}

type nameMocks struct {
	mu    sync.Mutex
	names []string
}

func (m *nameMocks) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.names = append(m.names, a.TypeToken+" "+a.Name)

	out := a.Inputs.Copy()
	out["arn"] = resource.NewStringProperty("arn:" + "aws" + ":iam::" + strings.Repeat("1", 12) + ":role/" + a.Name)

	return a.Name + "-id", out, nil
}

func (m *nameMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

// The children keep the names a stack created before this package existed.
func TestDeployKeepsTheResourceNames(t *testing.T) {
	m := &nameMocks{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := aws.NewProvider(ctx, "p", &aws.ProviderArgs{})
		if err != nil {
			return err
		}

		return Deploy(ctx, Inputs{
			Provider:       p,
			ClusterName:    "c1",
			EKSClusterName: pulumi.String("c1"),
			Region:         "eu-west-1",
			AccountID:      strings.Repeat("1", 12),
		})
	}, pulumi.WithMocks("proj", "stack", m))
	require.NoError(t, err)

	joined := strings.Join(m.names, "\n")
	for _, want := range []string{
		"audit-log-reader/c1-observability-audit-reader/role",
		"audit-log-reader/c1-observability-audit-reader/policy",
		"audit-log-reader/c1-observability-audit-reader/pia-",
	} {
		assert.Contains(t, joined, want)
	}
}

func TestInsightsPolicyAddsQueryStatementsOnly(t *testing.T) {
	account := strings.Repeat("1", 12)

	base, err := Policy("c1", "eu-west-1", account)
	require.NoError(t, err)

	doc, err := PolicyInsights("c1", "eu-west-1", account)
	require.NoError(t, err)

	type policyDoc struct {
		Statement []struct {
			Sid      string
			Action   []string
			Resource any
		}
	}

	var b, p policyDoc
	require.NoError(t, json.Unmarshal([]byte(base), &b))
	require.NoError(t, json.Unmarshal([]byte(doc), &p))
	require.Len(t, p.Statement, 4)
	assert.Equal(t, b.Statement, p.Statement[:2], "filter-mode statements are unchanged")

	group := "arn:" + "aws" + ":logs:eu-west-1:" + account + ":log-group:/aws/eks/c1/cluster"

	assert.Equal(t, "StartInsightsQuery", p.Statement[2].Sid)
	assert.Equal(t, []string{"logs:StartQuery"}, p.Statement[2].Action)
	assert.Equal(t, []any{group, group + ":*"}, p.Statement[2].Resource)

	assert.Equal(t, []string{"logs:GetQueryResults", "logs:StopQuery"}, p.Statement[3].Action)
	assert.Equal(t, "*", p.Statement[3].Resource)
}
