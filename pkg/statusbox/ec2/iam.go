package ec2

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	iamVersion = "2012-10-17"
)

// assumeRolePolicy lets EC2 assume the instance role.
func assumeRolePolicy() string {
	return mustJSON(map[string]any{
		"Version": iamVersion,
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "ec2.amazonaws.com"},
			"Action":    "sts:AssumeRole",
		}},
	})
}

// instancePolicy is the whole of what the instance may do, scoped to exactly
// the parameters it reads, the replica prefix of the bucket, the one Auto
// Scaling group it belongs to and, if one is given, the one KMS key. There is
// no managed policy on the role: AmazonSSMManagedInstanceCore would let it read
// every parameter in the account.
func (a Args) instancePolicy(box names, region, account string) string {
	statements := []map[string]any{}

	if params := a.parameterNames(); len(params) > 0 {
		arns := make([]string, len(params))
		for i, p := range params {
			arns[i] = fmt.Sprintf("arn:%s:ssm:%s:%s:parameter%s", partition, region, account, p)
		}

		statements = append(statements, map[string]any{
			"Sid":      "ReadOwnParameters",
			"Effect":   "Allow",
			"Action":   []string{"ssm:GetParameter"},
			"Resource": arns,
		})
	}

	objects := fmt.Sprintf("arn:%s:s3:::%s/*", partition, a.Bucket)
	listPrefix := "*"

	if a.BucketPrefix != "" {
		objects = fmt.Sprintf("arn:%s:s3:::%s/%s/*", partition, a.Bucket, a.BucketPrefix)
		listPrefix = a.BucketPrefix + "/*"
	}

	statements = append(statements,
		map[string]any{
			"Sid":      "ReplicaObjects",
			"Effect":   "Allow",
			"Action":   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
			"Resource": []string{objects},
		},
		map[string]any{
			"Sid":      "ReplicaList",
			"Effect":   "Allow",
			"Action":   []string{"s3:ListBucket"},
			"Resource": []string{"arn:" + partition + ":s3:::" + a.Bucket},
			"Condition": map[string]any{
				"StringLike": map[string]any{"s3:prefix": []string{listPrefix}},
			},
		},
		map[string]any{
			"Sid":      "ReplicaRegion",
			"Effect":   "Allow",
			"Action":   []string{"s3:GetBucketLocation"},
			"Resource": []string{"arn:" + partition + ":s3:::" + a.Bucket},
		},
		map[string]any{
			"Sid":    "OwnGroup",
			"Effect": "Allow",
			"Action": []string{"autoscaling:CompleteLifecycleAction", "autoscaling:SetInstanceHealth"},
			"Resource": []string{
				fmt.Sprintf("arn:%s:autoscaling:%s:%s:autoScalingGroup:*:autoScalingGroupName/%s", partition, region, account, box.asg),
			},
		},
	)

	if a.KMSKeyARN != "" {
		statements = append(statements, map[string]any{
			"Sid":      "OwnKey",
			"Effect":   "Allow",
			"Action":   []string{"kms:Decrypt", "kms:GenerateDataKey"},
			"Resource": []string{a.KMSKeyARN},
		})
	}

	return mustJSON(map[string]any{"Version": iamVersion, "Statement": statements})
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("statusbox/ec2: marshal policy: %v", err))
	}

	return strings.TrimSpace(string(b))
}
