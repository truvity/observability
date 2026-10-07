// Package alertqueue provisions the SQS queue alert-ingress polls: one
// standard queue that SNS topics publish to, and its dead-letter queue.
//
// The package is the consumer's half of the SQS input (see docs/alert-ingress.md,
// "SQS input"). The queue, its redrive policy and the queue policy belong to
// whoever runs alert-ingress; the SNS subscriptions belong to whoever owns each
// topic, and are not created here. The Pod Identity association is the
// caller's too: the package outputs the IAM policy document the consumer role
// needs and nothing else about identity.
//
// The package carries no estate data. Names, topics, the key and the alarm
// topic all arrive in Inputs.
package alertqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	// DefaultMaxReceiveCount is deliberately high: a message the consumer
	// keeps rejecting for a transient reason (Alertmanager down) must not be
	// parked after a handful of tries.
	DefaultMaxReceiveCount = 100
	// DefaultVisibilityTimeoutSeconds is how long a received message stays
	// hidden before a failed one is retried.
	DefaultVisibilityTimeoutSeconds = 300
	// DefaultRetentionSeconds is 14 days, the SQS maximum, on both queues.
	DefaultRetentionSeconds = 14 * 24 * 60 * 60

	maxReceiveCountMax = 1000
	visibilityMax      = 12 * 60 * 60
	retentionMin       = 60
	retentionMax       = 14 * 24 * 60 * 60

	// dlqSuffix is appended to Name for the dead-letter queue.
	dlqSuffix = "-dlq"

	policyVersion = "2012-10-17"
)

var (
	topicARN = regexp.MustCompile(`^arn:aws[a-z-]*:sns:[a-z0-9-]+:[0-9]{12}:[A-Za-z0-9_-]{1,256}$`)
	kmsARN   = regexp.MustCompile(`^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/[A-Za-z0-9-]+$`)
	// queueName is SQS's rule, less room for the "-dlq" suffix (80 total).
	queueName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,75}$`)
)

type (
	// Inputs are everything Deploy needs. Zero numeric values mean the default.
	Inputs struct {
		// Name is the queue's name; the dead-letter queue is Name+"-dlq".
		Name string
		// TopicARNs are the SNS topics allowed to publish to the queue. Topics
		// of other accounts and regions are allowed.
		TopicARNs []string
		// KMSKeyARN, when set, encrypts both queues with that customer key
		// instead of SQS-managed SSE. The key is the caller's: its key policy
		// must let sns.amazonaws.com call kms:GenerateDataKey and kms:Decrypt
		// (docs/alert-ingress.md shows the statement), and the consumer role
		// needs kms:Decrypt, which ConsumerPolicy includes.
		KMSKeyARN string

		MaxReceiveCount          int
		VisibilityTimeoutSeconds int
		RetentionSeconds         int

		// AlarmTopicARN, when set, receives the dead-letter queue depth
		// alarm's ALARM and OK transitions. It must be in the queue's region.
		AlarmTopicARN string
	}

	// Outputs are what the caller wires onward.
	Outputs struct {
		QueueARN pulumi.StringOutput
		QueueURL pulumi.StringOutput
		// DeadLetterQueueARN is the dead-letter queue's ARN.
		DeadLetterQueueARN pulumi.StringOutput
		// ConsumerPolicy is an IAM policy document (JSON) the consumer's role
		// needs: receive, delete and get-attributes on the queue, plus
		// kms:Decrypt on the key when one is set. Attach it to the Pod
		// Identity role; this package creates neither.
		ConsumerPolicy pulumi.StringOutput
	}
)

// withDefaults fills the zero numeric values.
func (in Inputs) withDefaults() Inputs {
	if in.MaxReceiveCount == 0 {
		in.MaxReceiveCount = DefaultMaxReceiveCount
	}

	if in.VisibilityTimeoutSeconds == 0 {
		in.VisibilityTimeoutSeconds = DefaultVisibilityTimeoutSeconds
	}

	if in.RetentionSeconds == 0 {
		in.RetentionSeconds = DefaultRetentionSeconds
	}

	return in
}

// Validate reports every problem it can find, on the defaulted inputs.
func (in Inputs) Validate() error {
	in = in.withDefaults()

	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("alertqueue: "+format, args...))
	}

	if !queueName.MatchString(in.Name) {
		bad("name %q must be 1-75 characters of letters, digits, '-' and '_'", in.Name)
	}

	if len(in.TopicARNs) == 0 {
		bad("topicARNs is empty: a queue nobody may publish to receives nothing")
	}

	seen := map[string]bool{}

	for _, a := range in.TopicARNs {
		if !topicARN.MatchString(a) {
			bad("topic ARN %q is not an SNS topic ARN", a)
		}

		if seen[a] {
			bad("topic ARN %q is listed twice", a)
		}

		seen[a] = true
	}

	if in.KMSKeyARN != "" && !kmsARN.MatchString(in.KMSKeyARN) {
		bad("kmsKeyARN %q is not a KMS key ARN (key/<id>; an alias cannot be named in a key policy)", in.KMSKeyARN)
	}

	if in.AlarmTopicARN != "" && !topicARN.MatchString(in.AlarmTopicARN) {
		bad("alarmTopicARN %q is not an SNS topic ARN", in.AlarmTopicARN)
	}

	if in.MaxReceiveCount < 1 || in.MaxReceiveCount > maxReceiveCountMax {
		bad("maxReceiveCount %d is outside 1-%d", in.MaxReceiveCount, maxReceiveCountMax)
	}

	if in.VisibilityTimeoutSeconds < 0 || in.VisibilityTimeoutSeconds > visibilityMax {
		bad("visibilityTimeoutSeconds %d is outside 0-%d", in.VisibilityTimeoutSeconds, visibilityMax)
	}

	if in.RetentionSeconds < retentionMin || in.RetentionSeconds > retentionMax {
		bad("retentionSeconds %d is outside %d-%d", in.RetentionSeconds, retentionMin, retentionMax)
	}

	return errors.Join(errs...)
}

// QueuePolicy renders the queue policy: SNS may send, only from the listed
// topics. The condition is ArnEquals on aws:SourceArn, which is how a topic
// in another account or region is admitted without a wildcard principal.
func QueuePolicy(queueARN string, topics []string) (string, error) {
	doc := map[string]any{
		"Version": policyVersion,
		"Statement": []any{map[string]any{
			"Sid":       "AllowSNSTopics",
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": "sns.amazonaws.com"},
			"Action":    "sqs:SendMessage",
			"Resource":  queueARN,
			"Condition": map[string]any{
				"ArnEquals": map[string]any{"aws:SourceArn": topics},
			},
		}},
	}

	b, err := json.Marshal(doc)

	return string(b), err
}

// ConsumerPolicyDocument renders the policy of the polling role.
func ConsumerPolicyDocument(queueARN, kmsKeyARN string) (string, error) {
	stmts := []any{map[string]any{
		"Sid":    "ConsumeAlertQueue",
		"Effect": "Allow",
		"Action": []string{
			"sqs:ReceiveMessage",
			"sqs:DeleteMessage",
			"sqs:GetQueueAttributes",
		},
		"Resource": queueARN,
	}}

	if kmsKeyARN != "" {
		stmts = append(stmts, map[string]any{
			"Sid":      "DecryptAlertQueue",
			"Effect":   "Allow",
			"Action":   "kms:Decrypt",
			"Resource": kmsKeyARN,
		})
	}

	b, err := json.Marshal(map[string]any{"Version": policyVersion, "Statement": stmts})

	return string(b), err
}

// redrivePolicy renders the main queue's redrive policy.
func redrivePolicy(dlqARN string, maxReceive int) (string, error) {
	b, err := json.Marshal(map[string]any{"deadLetterTargetArn": dlqARN, "maxReceiveCount": maxReceive})

	return string(b), err
}

// Deploy creates the queues, the queue policy and, when AlarmTopicARN is set,
// the dead-letter depth alarm. The resource names ("dlq", "queue",
// "queue-policy", "dlq-depth") are state identity and must not change. Pass the
// AWS provider of the queue's account and region as an option.
func Deploy(c *pulumi.Context, in Inputs, opts ...pulumi.ResourceOption) (*Outputs, error) {
	in = in.withDefaults()

	if err := in.Validate(); err != nil {
		return nil, err
	}

	encryption := func(a *sqs.QueueArgs) {
		if in.KMSKeyARN != "" {
			a.KmsMasterKeyId = pulumi.String(in.KMSKeyARN)
			a.KmsDataKeyReusePeriodSeconds = pulumi.Int(300)

			return
		}

		a.SqsManagedSseEnabled = pulumi.Bool(true)
	}

	dlqArgs := &sqs.QueueArgs{
		Name:                    pulumi.String(in.Name + dlqSuffix),
		MessageRetentionSeconds: pulumi.Int(in.RetentionSeconds),
		// A parked message is looked at by a person: the default hiding time
		// is enough, and the main queue's value is not the DLQ's concern.
	}
	encryption(dlqArgs)

	dlq, err := sqs.NewQueue(c, "dlq", dlqArgs, opts...)
	if err != nil {
		return nil, fmt.Errorf("create dead-letter queue: %w", err)
	}

	redrive := dlq.Arn.ApplyT(func(arn string) (string, error) {
		return redrivePolicy(arn, in.MaxReceiveCount)
	}).(pulumi.StringOutput)

	qArgs := &sqs.QueueArgs{
		Name:                     pulumi.String(in.Name),
		VisibilityTimeoutSeconds: pulumi.Int(in.VisibilityTimeoutSeconds),
		MessageRetentionSeconds:  pulumi.Int(in.RetentionSeconds),
		RedrivePolicy:            redrive,
	}
	encryption(qArgs)

	q, err := sqs.NewQueue(c, "queue", qArgs, opts...)
	if err != nil {
		return nil, fmt.Errorf("create queue: %w", err)
	}

	policy := q.Arn.ApplyT(func(arn string) (string, error) {
		return QueuePolicy(arn, in.TopicARNs)
	}).(pulumi.StringOutput)

	if _, err := sqs.NewQueuePolicy(c, "queue-policy", &sqs.QueuePolicyArgs{
		QueueUrl: q.Url,
		Policy:   policy,
	}, opts...); err != nil {
		return nil, fmt.Errorf("create queue policy: %w", err)
	}

	if in.AlarmTopicARN != "" {
		actions := pulumi.Array{pulumi.String(in.AlarmTopicARN)}

		if _, err := cloudwatch.NewMetricAlarm(c, "dlq-depth", &cloudwatch.MetricAlarmArgs{
			Name:               pulumi.String(in.Name + dlqSuffix + "-not-empty"),
			AlarmDescription:   pulumi.String("Messages are waiting in the alert queue's dead-letter queue: " + strconv.Itoa(in.MaxReceiveCount) + " failed receives, or a rejected topic or signature. Fix the cause, then redrive."),
			Namespace:          pulumi.String("AWS/SQS"),
			MetricName:         pulumi.String("ApproximateNumberOfMessagesVisible"),
			Dimensions:         pulumi.StringMap{"QueueName": dlq.Name},
			Statistic:          pulumi.String("Maximum"),
			Period:             pulumi.Int(300),
			EvaluationPeriods:  pulumi.Int(1),
			Threshold:          pulumi.Float64(0),
			ComparisonOperator: pulumi.String("GreaterThanThreshold"),
			// An empty queue reports 0, and a quiet one reports nothing:
			// neither is a problem.
			TreatMissingData: pulumi.String("notBreaching"),
			AlarmActions:     actions,
			OkActions:        actions,
		}, opts...); err != nil {
			return nil, fmt.Errorf("create dead-letter queue alarm: %w", err)
		}
	}

	consumer := q.Arn.ApplyT(func(arn string) (string, error) {
		return ConsumerPolicyDocument(arn, in.KMSKeyARN)
	}).(pulumi.StringOutput)

	return &Outputs{
		QueueARN:           q.Arn,
		QueueURL:           q.Url,
		DeadLetterQueueARN: dlq.Arn,
		ConsumerPolicy:     consumer,
	}, nil
}
