package main

import "github.com/prometheus/client_golang/prometheus"

// Results of one webhook request: the whole label vocabulary. No label
// carries a recipient, a chat id or an alert.
const (
	resultDelivered        = "delivered"
	resultUnauthorized     = "unauthorized"
	resultBadRequest       = "bad_request"
	resultUnknownRecipient = "unknown_recipient"
	resultTelegramError    = "telegram_error"
	resultConfigError      = "config_error"
)

var allResults = []string{resultDelivered, resultUnauthorized, resultBadRequest, resultUnknownRecipient, resultTelegramError, resultConfigError}

// Metrics is the only place a failed page is visible: GoAlert does not
// read the webhook's answer, so it records every message it handed over
// as sent.
type Metrics struct {
	// Notifications counts webhook requests by payload type and result.
	Notifications *prometheus.CounterVec
	// SendDuration is the latency of one Telegram call.
	SendDuration prometheus.Histogram
	// LastDelivered is the Unix time of the last delivered message.
	LastDelivered prometheus.Gauge
}

// NewMetrics registers Metrics against reg, with every (type, result) pair
// of an alert pre-created at zero so a rule on increase() sees the first
// failure.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Notifications: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "telegram_relay_notifications_total",
			Help: "GoAlert webhook requests by payload type and result (delivered, unauthorized, bad_request, unknown_recipient, telegram_error, config_error).",
		}, []string{"type", "result"}),
		SendDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "telegram_relay_send_duration_seconds",
			Help:    "Latency of one Telegram Bot API sendMessage call.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 1.5, 2, 2.5, 3},
		}),
		LastDelivered: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "telegram_relay_last_delivered_timestamp_seconds",
			Help: "Unix time of the last message Telegram accepted.",
		}),
	}
	reg.MustRegister(m.Notifications, m.SendDuration, m.LastDelivered)
	for _, r := range allResults {
		m.Notifications.WithLabelValues(typeAlert, r)
	}
	return m
}
