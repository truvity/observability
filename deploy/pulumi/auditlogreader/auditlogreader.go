// Package auditlogreader mints the Pod Identity role of the CloudWatch reader
// that forwards a cluster's API-server AUDIT log to the observability store
// (`observability-emitters` `cloudwatchLogs`).
//
// EKS writes the control-plane log to the CloudWatch group
// /aws/eks/<cluster>/cluster and offers no other way out of it. The reader
// runs in the cluster's own observability namespace, reads its own account's
// group, and writes to the store through the collectors' usual path; the role
// is therefore one per cluster, in the cluster's own account, read-only on
// that one group.
//
// The package carries no estate data: the cluster, the region, the account and
// the permissions boundary arrive in Inputs.
package auditlogreader

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

const (
	// Namespace is the namespace of the observability install, the chart's
	// default destination.
	Namespace = "observability"

	// ServiceAccount is the ServiceAccount of the CloudWatch reader. Set it
	// explicitly in `cloudwatchLogs.serviceAccount.name` and keep it equal to
	// this byte for byte, or Pod Identity binds to a name nothing runs as.
	ServiceAccount = "observability-cloudwatch"

	partition     = "aws"
	policyVersion = "2012-10-17"
)

// Inputs are everything Deploy needs.
type Inputs struct {
	// ClusterName is the cluster's name; it prefixes the role and names the
	// control-plane log group.
	ClusterName string
	// EKSClusterName is the EKS cluster the association is made on (an output
	// when the cluster is created in the same stack).
	EKSClusterName pulumi.StringInput
	Region         string
	AccountID      string
	// PermissionsBoundaryARN is the boundary policy of the role; empty sets none.
	PermissionsBoundaryARN string
	// Provider is the AWS provider of the cluster's account.
	Provider *aws.Provider
}

// RoleName is the role of one cluster's CloudWatch reader.
func RoleName(cluster string) string {
	return cluster + "-observability-audit-reader"
}

// LogGroupName is the control-plane log group of a cluster.
func LogGroupName(cluster string) string {
	return fmt.Sprintf("/aws/eks/%s/cluster", cluster)
}

func (in Inputs) validate() error {
	var errs []error

	if in.ClusterName == "" {
		errs = append(errs, errors.New("ClusterName is required"))
	}

	if in.EKSClusterName == nil {
		errs = append(errs, errors.New("EKSClusterName is required"))
	}

	if in.Provider == nil {
		errs = append(errs, errors.New("Provider is required"))
	}

	if in.Region == "" {
		errs = append(errs, errors.New("Region is required"))
	}

	if in.AccountID == "" {
		errs = append(errs, errors.New("AccountID is required"))
	}

	return errors.Join(errs...)
}

// Deploy mints the reader's role, its policy and the Pod Identity association
// for this cluster. The children are named "audit-log-reader/<role>/role",
// ".../policy" and ".../pia-<key>", so a stack that created them with those
// names before this package existed keeps its URNs.
func Deploy(ctx *pulumi.Context, in Inputs) error {
	if err := in.validate(); err != nil {
		return fmt.Errorf("auditlogreader: %w", err)
	}

	roleName := RoleName(in.ClusterName)

	policy, err := Policy(in.ClusterName, in.Region, in.AccountID)
	if err != nil {
		return err
	}

	_, err = podidentity.New(ctx, "audit-log-reader/"+roleName, &podidentity.Args{
		Provider:            in.Provider,
		ClusterName:         in.EKSClusterName,
		Region:              in.Region,
		Namespace:           Namespace,
		ServiceAccounts:     []string{ServiceAccount},
		RoleName:            roleName,
		PermissionsBoundary: in.PermissionsBoundaryARN,
		AccountID:           in.AccountID,
		ClusterARN:          fmt.Sprintf("arn:%s:eks:%s:%s:cluster/%s", partition, in.Region, in.AccountID, in.ClusterName),
		InlinePolicy: &podidentity.InlinePolicy{
			Name:     "audit-log-reader",
			Document: pulumi.String(policy),
		},
		Names: func(c podidentity.Child) string {
			switch c.Kind {
			case podidentity.KindRolePolicy:
				return "audit-log-reader/" + roleName + "/policy"
			case podidentity.KindAssociation:
				return "audit-log-reader/" + roleName + "/pia-" + c.Key
			default:
				return "audit-log-reader/" + roleName + "/role"
			}
		},
	})
	if err != nil {
		return fmt.Errorf("audit log reader pod identity: %w", err)
	}

	return nil
}

// Policy is the reader's identity policy: read-only on the cluster's own
// control-plane log group. DescribeLogGroups cannot be scoped to one group (the
// receiver discovers the group by name prefix), so it is granted on the
// account's log groups in this region; it lists names and settings, never
// events.
func Policy(cluster, region, account string) (string, error) {
	logs := fmt.Sprintf("arn:%s:logs:%s:%s:log-group:", partition, region, account)
	group := logs + LogGroupName(cluster)

	raw, err := json.Marshal(map[string]any{
		"Version": policyVersion,
		"Statement": []map[string]any{
			{
				"Sid":      "ListLogGroups",
				"Effect":   "Allow",
				"Action":   []string{"logs:DescribeLogGroups"},
				"Resource": logs + "*",
			},
			{
				"Sid":    "ReadControlPlaneLog",
				"Effect": "Allow",
				"Action": []string{
					"logs:DescribeLogStreams", "logs:FilterLogEvents", "logs:GetLogEvents",
				},
				"Resource": []string{group, group + ":*"},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("audit log reader policy: %w", err)
	}

	return string(raw), nil
}
