package alertqueue

import (
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	queueType  = "aws:sqs/queue:Queue"
	policyType = "aws:sqs/queuePolicy:QueuePolicy"
	alarmType  = "aws:cloudwatch/metricAlarm:MetricAlarm"

	topicA = "arn:aws:sns:eu-west-1:111111111111:security"
	topicB = "arn:aws:sns:us-east-1:222222222222:budgets"
	keyARN = "arn:aws:kms:eu-west-1:111111111111:key/1234abcd-12ab-34cd-56ef-1234567890ab"
)

func sample() Inputs {
	return Inputs{Name: "alerts", TopicARNs: []string{topicA, topicB}}
}

type (
	recorded struct {
		typ, name string
		inputs    resource.PropertyMap
	}

	mocks struct {
		mu        sync.Mutex
		resources []recorded
	}
)

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.resources = append(m.resources, recorded{args.TypeToken, args.Name, args.Inputs})

	out := args.Inputs.Copy()
	out["arn"] = resource.NewStringProperty("arn:aws:sqs:eu-west-1:111111111111:" + args.Name)
	out["url"] = resource.NewStringProperty("https://sqs.eu-west-1.amazonaws.com/111111111111/" + args.Name)

	return args.Name + "-id", out, nil
}

func (m *mocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (m *mocks) names(typ string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string

	for _, r := range m.resources {
		if r.typ == typ {
			out = append(out, r.name)
		}
	}

	slices.Sort(out)

	return out
}

func (m *mocks) inputs(typ, name string) resource.PropertyMap {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.resources {
		if r.typ == typ && r.name == name {
			return r.inputs
		}
	}

	return nil
}

func (m *mocks) str(typ, name, key string) string {
	v := m.inputs(typ, name)[resource.PropertyKey(key)]
	if v.IsString() {
		return v.StringValue()
	}

	return ""
}

func (m *mocks) num(typ, name, key string) float64 {
	v := m.inputs(typ, name)[resource.PropertyKey(key)]
	if v.IsNumber() {
		return v.NumberValue()
	}

	return -1
}

func deploy(t *testing.T, in Inputs) (*mocks, map[string]string) {
	t.Helper()

	m := &mocks{}
	got := map[string]string{}

	require.NoError(t, pulumi.RunErr(func(c *pulumi.Context) error {
		out, err := Deploy(c, in)
		if err != nil {
			return err
		}

		out.QueueARN.ApplyT(func(s string) string { got["arn"] = s; return s })
		out.QueueURL.ApplyT(func(s string) string { got["url"] = s; return s })
		out.DeadLetterQueueARN.ApplyT(func(s string) string { got["dlq"] = s; return s })
		out.ConsumerPolicy.ApplyT(func(s string) string { got["consumer"] = s; return s })

		return nil
	}, pulumi.WithMocks("proj", "stack", m)))

	return m, got
}

func TestDeployCreatesQueuesWithDefaults(t *testing.T) {
	m, got := deploy(t, sample())

	assert.Equal(t, []string{"dlq", "queue"}, m.names(queueType))
	assert.Equal(t, []string{"queue-policy"}, m.names(policyType))
	assert.Empty(t, m.names(alarmType), "no alarm without an alarm topic")

	for _, n := range []string{"dlq", "queue"} {
		assert.Equal(t, 1209600.0, m.num(queueType, n, "messageRetentionSeconds"), n)
		assert.Equal(t, true, m.inputs(queueType, n)["sqsManagedSseEnabled"].BoolValue(), n)
		assert.Empty(t, m.str(queueType, n, "kmsMasterKeyId"), n)
	}

	assert.Equal(t, "alerts", m.str(queueType, "queue", "name"))
	assert.Equal(t, "alerts-dlq", m.str(queueType, "dlq", "name"))
	assert.Equal(t, 300.0, m.num(queueType, "queue", "visibilityTimeoutSeconds"))
	assert.JSONEq(t,
		`{"deadLetterTargetArn":"arn:aws:sqs:eu-west-1:111111111111:dlq","maxReceiveCount":100}`,
		m.str(queueType, "queue", "redrivePolicy"))

	assert.Equal(t, "arn:aws:sqs:eu-west-1:111111111111:queue", got["arn"])
	assert.Equal(t, "https://sqs.eu-west-1.amazonaws.com/111111111111/queue", got["url"])
	assert.Equal(t, "arn:aws:sqs:eu-west-1:111111111111:dlq", got["dlq"])
}

func TestDeployWithKMSAndOverrides(t *testing.T) {
	in := sample()
	in.KMSKeyARN = keyARN
	in.MaxReceiveCount = 7
	in.VisibilityTimeoutSeconds = 30
	in.RetentionSeconds = 86400

	m, got := deploy(t, in)

	for _, n := range []string{"dlq", "queue"} {
		assert.Equal(t, keyARN, m.str(queueType, n, "kmsMasterKeyId"), n)
		assert.NotContains(t, m.inputs(queueType, n), resource.PropertyKey("sqsManagedSseEnabled"), n)
		assert.Equal(t, 86400.0, m.num(queueType, n, "messageRetentionSeconds"), n)
	}

	assert.Equal(t, 30.0, m.num(queueType, "queue", "visibilityTimeoutSeconds"))
	assert.Contains(t, m.str(queueType, "queue", "redrivePolicy"), `"maxReceiveCount":7`)
	assert.Contains(t, got["consumer"], `"kms:Decrypt"`)
	assert.Contains(t, got["consumer"], keyARN)
}

func TestQueuePolicyGolden(t *testing.T) {
	doc, err := QueuePolicy("arn:aws:sqs:eu-west-1:111111111111:alerts", []string{topicA, topicB})
	require.NoError(t, err)

	assert.JSONEq(t, `{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "AllowSNSTopics",
    "Effect": "Allow",
    "Principal": {"Service": "sns.amazonaws.com"},
    "Action": "sqs:SendMessage",
    "Resource": "arn:aws:sqs:eu-west-1:111111111111:alerts",
    "Condition": {"ArnEquals": {"aws:SourceArn": [
      "arn:aws:sns:eu-west-1:111111111111:security",
      "arn:aws:sns:us-east-1:222222222222:budgets"
    ]}}
  }]
}`, doc)
}

func TestConsumerPolicyGolden(t *testing.T) {
	const q = "arn:aws:sqs:eu-west-1:111111111111:alerts"

	doc, err := ConsumerPolicyDocument(q, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Sid":"ConsumeAlertQueue","Effect":"Allow",
"Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueAttributes"],"Resource":"`+q+`"}]}`, doc)

	doc, err = ConsumerPolicyDocument(q, keyARN)
	require.NoError(t, err)
	assert.Contains(t, doc, `{"Action":"kms:Decrypt","Effect":"Allow","Resource":"`+keyARN+`","Sid":"DecryptAlertQueue"}`)
}

func TestDeployCreatesTheDLQAlarm(t *testing.T) {
	in := sample()
	in.AlarmTopicARN = topicA

	m, _ := deploy(t, in)

	require.Equal(t, []string{"dlq-depth"}, m.names(alarmType))
	assert.Equal(t, "alerts-dlq-not-empty", m.str(alarmType, "dlq-depth", "name"))
	assert.Equal(t, "AWS/SQS", m.str(alarmType, "dlq-depth", "namespace"))
	assert.Equal(t, "ApproximateNumberOfMessagesVisible", m.str(alarmType, "dlq-depth", "metricName"))
	assert.Equal(t, "GreaterThanThreshold", m.str(alarmType, "dlq-depth", "comparisonOperator"))
	assert.Equal(t, 0.0, m.num(alarmType, "dlq-depth", "threshold"))
	assert.Equal(t, 300.0, m.num(alarmType, "dlq-depth", "period"))
	assert.Equal(t, "notBreaching", m.str(alarmType, "dlq-depth", "treatMissingData"))

	in2 := m.inputs(alarmType, "dlq-depth")
	for _, k := range []resource.PropertyKey{"alarmActions", "okActions"} {
		require.Len(t, in2[k].ArrayValue(), 1, string(k))
		assert.Equal(t, topicA, in2[k].ArrayValue()[0].StringValue())
	}
}

func TestValidationRefusals(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Inputs)
		want string
	}{
		"empty topics":      {func(i *Inputs) { i.TopicARNs = nil }, "topicARNs is empty"},
		"bad topic":         {func(i *Inputs) { i.TopicARNs = []string{"arn:aws:sqs:eu-west-1:111111111111:x"} }, "not an SNS topic ARN"},
		"duplicate topic":   {func(i *Inputs) { i.TopicARNs = []string{topicA, topicA} }, "listed twice"},
		"wildcard topic":    {func(i *Inputs) { i.TopicARNs = []string{"arn:aws:sns:*:*:*"} }, "not an SNS topic ARN"},
		"bad name":          {func(i *Inputs) { i.Name = "a b" }, "name"},
		"empty name":        {func(i *Inputs) { i.Name = "" }, "name"},
		"alias as key":      {func(i *Inputs) { i.KMSKeyARN = "arn:aws:kms:eu-west-1:111111111111:alias/x" }, "kmsKeyARN"},
		"bad alarm topic":   {func(i *Inputs) { i.AlarmTopicARN = "nope" }, "alarmTopicARN"},
		"max receive high":  {func(i *Inputs) { i.MaxReceiveCount = 1001 }, "maxReceiveCount"},
		"max receive neg":   {func(i *Inputs) { i.MaxReceiveCount = -1 }, "maxReceiveCount"},
		"visibility high":   {func(i *Inputs) { i.VisibilityTimeoutSeconds = 43201 }, "visibilityTimeoutSeconds"},
		"retention low":     {func(i *Inputs) { i.RetentionSeconds = 59 }, "retentionSeconds"},
		"retention too big": {func(i *Inputs) { i.RetentionSeconds = 1209601 }, "retentionSeconds"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := sample()
			tc.mut(&in)

			err := in.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)

			// Deploy refuses before it registers anything.
			m := &mocks{}
			runErr := pulumi.RunErr(func(c *pulumi.Context) error {
				_, err := Deploy(c, in)

				return err
			}, pulumi.WithMocks("proj", "stack", m))
			require.Error(t, runErr)
			assert.Empty(t, m.names(queueType))
		})
	}

	require.NoError(t, sample().Validate())
}
