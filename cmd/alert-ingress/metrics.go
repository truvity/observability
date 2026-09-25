package main

import "github.com/prometheus/client_golang/prometheus"

// Metrics is the one counter this service exports. It is also the whole
// of its own deadman: the chart's VMRule watches
// alert_ingress_messages_total{mapping="heartbeat"} for silence, because
// nothing else on the estate's side is told when a notification topic
// stops delivering — the provider retries and then gives up quietly.
type Metrics struct {
	// Messages is incremented once per request this service answers,
	// labelled by `outcome` (received, mapped, unmapped, rejected) and by
	// `mapping` (a mapping's own name, "heartbeat", "unmapped", or empty
	// for a rejection made before a topic was even read).
	Messages *prometheus.CounterVec
}

// NewMetrics registers Metrics against reg and returns it.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingress_messages_total",
			Help: "Cloud notifications this service has processed, by outcome and mapping.",
		}, []string{"outcome", "mapping"}),
	}

	reg.MustRegister(m.Messages)

	return m
}
