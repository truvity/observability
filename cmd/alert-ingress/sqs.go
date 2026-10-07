package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// sqsAPI is the part of the SQS client the consumer uses; a fake stands in
// for it in tests.
type sqsAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// NewSQSClient builds the client from the default AWS credential chain
// (pod identity, IRSA, environment). There is deliberately no way to hand
// it a key.
func NewSQSClient(ctx context.Context, region string) (*sqs.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}

	return sqs.NewFromConfig(cfg), nil
}

// SQSConsumer is the queue input. Each message is the SNS notification
// envelope (raw message delivery off), and goes through Server.Handle, the
// same pipeline the webhook uses. A message is deleted only once it is
// finished with: delivered to Alertmanager, ignored on purpose, or
// rejected as unprocessable. When Alertmanager does not accept the alert
// the message stays and returns after the visibility timeout; the queue's
// redrive policy, not this service, decides when it goes to the
// dead-letter queue.
type SQSConsumer struct {
	Client  sqsAPI
	Config  SQSConfig
	Server  *Server
	Metrics *Metrics
	Logger  *slog.Logger
	// ErrorBackoff is how long a worker waits after a failed receive.
	ErrorBackoff time.Duration
	// Now is the clock for message age; time.Now when nil.
	Now func() time.Time
}

// Run polls until ctx is cancelled, with Config.Concurrency workers.
func (c *SQSConsumer) Run(ctx context.Context) {
	var wg sync.WaitGroup

	for range c.Config.Concurrency {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for ctx.Err() == nil {
				if err := c.PollOnce(ctx); err != nil && ctx.Err() == nil {
					c.sleep(ctx, c.ErrorBackoff)
				}
			}
		}()
	}

	wg.Wait()
}

func (c *SQSConsumer) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// PollOnce makes one ReceiveMessage call and handles what it returns. The
// error is the receive error, already counted and logged.
func (c *SQSConsumer) PollOnce(ctx context.Context) error {
	out, err := c.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.Config.QueueURL),
		MaxNumberOfMessages: int32(c.Config.MaxMessages),
		WaitTimeSeconds:     int32(c.Config.WaitTimeSeconds),
		VisibilityTimeout:   int32(c.Config.VisibilityTimeoutSeconds),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameSentTimestamp,
			types.MessageSystemAttributeNameApproximateFirstReceiveTimestamp,
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			c.Metrics.SQSReceiveErrors.Inc()
			c.Logger.Error("receiving from the queue", "error", err)
		}

		return err
	}

	c.Metrics.SQSOldestAge.Set(c.oldestAge(out.Messages))

	for _, m := range out.Messages {
		c.Metrics.SQSReceived.Inc()
		c.handle(ctx, m)
	}

	return nil
}

// oldestAge is the age in seconds of the oldest message, by SentTimestamp
// (the first-receive time when that is missing); 0 for an empty batch.
func (c *SQSConsumer) oldestAge(msgs []types.Message) float64 {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	oldest := 0.0

	for _, m := range msgs {
		ms, ok := m.Attributes[string(types.MessageSystemAttributeNameSentTimestamp)]
		if !ok {
			ms, ok = m.Attributes[string(types.MessageSystemAttributeNameApproximateFirstReceiveTimestamp)]
		}

		if !ok {
			continue
		}

		v, err := strconv.ParseInt(ms, 10, 64)
		if err != nil {
			continue
		}

		if age := now().Sub(time.UnixMilli(v)).Seconds(); age > oldest {
			oldest = age
		}
	}

	return oldest
}

func (c *SQSConsumer) handle(ctx context.Context, m types.Message) {
	id := aws.ToString(m.MessageId)

	var env Envelope

	if m.Body == nil {
		c.reject(ctx, m, "", ReasonMalformed, "message has no body")
		return
	}

	if err := json.Unmarshal([]byte(*m.Body), &env); err != nil {
		// The decoder's message can quote body bytes; log only the kind.
		c.reject(ctx, m, "", ReasonMalformed, "body is not an SNS notification envelope")
		return
	}

	res := c.Server.Handle(env, false)

	switch res.verdict {
	case verdictRejected:
		c.reject(ctx, m, res.topic, res.reason, res.detail)
	case verdictFailed:
		c.Metrics.SQSFailed.Inc()
		c.Logger.Warn("alertmanager did not accept the alert; leaving the message for redelivery",
			"messageId", id, "topic", res.topic)
	default:
		c.Metrics.SQSProcessed.Inc()
		c.delete(ctx, m)
	}
}

// reject counts the message as rejected and deletes it: it can never
// succeed, and leaving it would only feed the dead-letter queue.
func (c *SQSConsumer) reject(ctx context.Context, m types.Message, topic, reason, detail string) {
	c.Server.countRejected(topic, reason, detail)
	c.Metrics.SQSRejected.WithLabelValues(reason).Inc()
	c.Logger.Warn("queue message rejected and deleted", "messageId", aws.ToString(m.MessageId), "reason", reason)
	c.delete(ctx, m)
}

func (c *SQSConsumer) delete(ctx context.Context, m types.Message) {
	_, err := c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.Config.QueueURL),
		ReceiptHandle: m.ReceiptHandle,
	})
	if err != nil {
		c.Metrics.SQSDeleteErrors.Inc()
		c.Logger.Error("deleting a handled queue message; it will be delivered again",
			"messageId", aws.ToString(m.MessageId), "error", err)

		return
	}

	c.Metrics.SQSDeleted.Inc()
}
