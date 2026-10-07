package main

import "github.com/prometheus/client_golang/prometheus"

// The closed set of `reason` label values on alert_ingress_rejected_total.
const (
	// ReasonMalformed: the body could not be read or is not an envelope.
	ReasonMalformed = "malformed"
	// ReasonSignature: the signature, its certificate URL or its algorithm
	// did not verify.
	ReasonSignature = "signature"
	// ReasonTopic: validly signed, but the TopicArn is not allow-listed.
	ReasonTopic = "unknown_topic"
	// ReasonConfirmation: an allow-listed SubscriptionConfirmation whose
	// SubscribeURL was refused or failed.
	ReasonConfirmation = "confirmation"
	// ReasonType: a signed, allow-listed message of a Type this service
	// does not handle (UnsubscribeConfirmation).
	ReasonType = "unsupported_type"
)

var rejectReasons = []string{ReasonMalformed, ReasonSignature, ReasonTopic, ReasonConfirmation, ReasonType}

// Metrics is what this service exports. alert_ingress_messages_total is also the whole
// of its own deadman: the chart's VMRule watches
// alert_ingress_messages_total{mapping="heartbeat"} for silence, because
// nothing else on the estate's side is told when a notification topic
// stops delivering — the provider retries and then gives up quietly.
type Metrics struct {
	// Rejected counts every message turned away with a 403, by `reason`,
	// one of the Reason* constants below — a closed set, so the label's
	// cardinality is fixed no matter what a sender puts in a request.
	// alert_ingress_messages_total{outcome="rejected"} counts the same
	// events without the reason; this is the one the chart's
	// AlertIngressMessagesRejected rule watches.
	Rejected *prometheus.CounterVec

	// Messages is incremented once per request this service answers,
	// labelled by `outcome` (received, mapped, unmapped, rejected) and by
	// `mapping` (a mapping's own name, "heartbeat", "unmapped", or empty
	// for a rejection made before a topic was even read).
	Messages *prometheus.CounterVec

	// The queue input's own series, all alert_ingress_sqs_*.
	SQSReceived      prometheus.Counter
	SQSProcessed     prometheus.Counter
	SQSDeleted       prometheus.Counter
	SQSConfirmed     prometheus.Counter
	SQSFailed        prometheus.Counter
	SQSRejected      *prometheus.CounterVec
	SQSReceiveErrors prometheus.Counter
	SQSDeleteErrors  prometheus.Counter
	SQSOldestAge     prometheus.Gauge
}

// NewMetrics registers Metrics against reg and returns it.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingress_messages_total",
			Help: "Cloud notifications this service has processed, by outcome and mapping.",
		}, []string{"outcome", "mapping"}),
	}

	m.Rejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "alert_ingress_rejected_total",
		Help: "Messages refused with a 403, by reason: malformed, signature, unknown_topic, confirmation, unsupported_type.",
	}, []string{"reason"})

	// Every reason exists at zero from the start, so a rate() over a
	// reason that has not happened yet is 0 rather than absent.
	for _, r := range rejectReasons {
		m.Rejected.WithLabelValues(r)
	}

	m.SQSReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_received_total",
		Help: "Messages received from the queue (a redelivery counts again).",
	})
	m.SQSProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_processed_total",
		Help: "Queue messages handled to completion: the alert reached Alertmanager, or the message needed no alert.",
	})
	m.SQSDeleted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_deleted_total",
		Help: "Queue messages deleted after being processed or rejected.",
	})
	m.SQSConfirmed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_confirmed_total",
		Help: "SNS subscription confirmations found in the queue and confirmed.",
	})
	m.SQSFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_failed_total",
		Help: "Queue messages left on the queue because Alertmanager did not accept the alert; they return after the visibility timeout.",
	})
	m.SQSRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_rejected_total",
		Help: "Queue messages deleted as unprocessable, by reason: malformed, signature, unknown_topic, unsupported_type.",
	}, []string{"reason"})
	m.SQSReceiveErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_receive_errors_total",
		Help: "Failed ReceiveMessage calls.",
	})
	m.SQSDeleteErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "alert_ingress_sqs_delete_errors_total",
		Help: "Failed DeleteMessage calls; the message will be delivered again.",
	})
	m.SQSOldestAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "alert_ingress_sqs_oldest_message_age_seconds",
		Help: "Age, by SentTimestamp, of the oldest message in the latest receive; 0 after an empty receive.",
	})

	for _, r := range rejectReasons {
		m.SQSRejected.WithLabelValues(r)
	}

	reg.MustRegister(m.Messages, m.Rejected, m.SQSReceived, m.SQSProcessed, m.SQSDeleted, m.SQSConfirmed, m.SQSFailed,
		m.SQSRejected, m.SQSReceiveErrors, m.SQSDeleteErrors, m.SQSOldestAge)

	return m
}
