package main

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the reader's own series, scraped from the metrics port.
type Metrics struct {
	events      prometheus.Counter
	queries     *prometheus.CounterVec
	throttled   prometheus.Counter
	splits      prometheus.Counter
	truncated   prometheus.Counter
	watermark   prometheus.Gauge
	lastSuccess prometheus.Gauge
}

// NewMetrics registers the series on r.
func NewMetrics(r prometheus.Registerer) *Metrics {
	m := &Metrics{
		events: prometheus.NewCounter(prometheus.CounterOpts{Name: "cwlogs_insights_events_total", Help: "Events handed to the collector."}),
		queries: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cwlogs_insights_windows_total", Help: "Windows read, by result."},
			[]string{"result"}),
		throttled: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cwlogs_insights_throttled_total", Help: "API calls retried after throttling or the concurrent-query limit."}),
		splits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cwlogs_insights_window_splits_total", Help: "Windows split because a query returned the 10000-row cap."}),
		truncated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cwlogs_insights_truncated_total", Help: "One-second windows still at the 10000-row cap (events may be lost)."}),
		watermark:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "cwlogs_insights_watermark_timestamp_seconds", Help: "End of the last window read."}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{Name: "cwlogs_insights_last_success_timestamp_seconds", Help: "When a window last completed."}),
	}

	r.MustRegister(m.events, m.queries, m.throttled, m.splits, m.truncated, m.watermark, m.lastSuccess)

	return m
}
