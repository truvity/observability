package main

import "github.com/prometheus/client_golang/prometheus"

// Metrics is what an operator reads to tell a backend that is down from a
// tool that is failing. No label ever carries an argument or a result: the
// tool label is one of the allowlist's exposed names, or "unknown".
type Metrics struct {
	// Calls counts tools/call by exposed tool and outcome: ok, tool_error
	// (the backend answered with an error result), unavailable (the backend
	// could not be reached), upstream_error (a protocol error from the
	// backend), timeout, canceled, denied (the authorization hook).
	Calls *prometheus.CounterVec
	// Duration is the latency of tools/call by exposed tool.
	Duration *prometheus.HistogramVec
	// Ready is 1 once every allowlisted tool was found and the surface is
	// being served.
	Ready prometheus.Gauge
	// SyncFailures counts failed attempts to build the tool snapshot, by
	// backend and reason (unreachable, missing_tool).
	SyncFailures *prometheus.CounterVec
	// Unlisted counts tools a backend offers that the allowlist does not
	// name, by backend: the signal that an upgrade added a tool.
	Unlisted *prometheus.GaugeVec
	// Tools is the number of tools exposed.
	Tools prometheus.Gauge
}

// NewMetrics registers Metrics against reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_aggregator_tool_calls_total",
			Help: "tools/call requests by exposed tool and outcome.",
		}, []string{"tool", "outcome"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mcp_aggregator_tool_call_duration_seconds",
			Help:    "tools/call latency by exposed tool.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"tool"}),
		Ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_aggregator_ready",
			Help: "1 once every allowlisted tool was found on its backend.",
		}),
		SyncFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_aggregator_sync_failures_total",
			Help: "Failed attempts to build the tool snapshot, by backend and reason.",
		}, []string{"backend", "reason"}),
		Unlisted: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "mcp_aggregator_unlisted_tools",
			Help: "Tools a backend offers that the allowlist does not name.",
		}, []string{"backend"}),
		Tools: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mcp_aggregator_tools",
			Help: "Number of tools exposed.",
		}),
	}
	reg.MustRegister(m.Calls, m.Duration, m.Ready, m.SyncFailures, m.Unlisted, m.Tools)
	return m
}
