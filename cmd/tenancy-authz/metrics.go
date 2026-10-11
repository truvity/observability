package main

import "github.com/prometheus/client_golang/prometheus"

// Outcomes of one check. They are the whole label vocabulary: no label ever
// carries a subject, a group or a token.
const (
	outcomePassThrough     = "pass_through"
	outcomeMinted          = "minted"
	outcomeUnauthenticated = "unauthenticated"
	outcomeForbidden       = "forbidden"
	outcomeError           = "error"
)

// Metrics is what an operator reads to tell a refusal from an outage.
type Metrics struct {
	// Checks counts ext_authz checks by outcome: pass_through (not a
	// person's token; answered 200 with nothing changed), minted (a person's
	// token exchanged), unauthenticated (401), forbidden (403: no group
	// selects a principal), error (the service could not decide).
	Checks *prometheus.CounterVec
	// Duration is the latency of one check.
	Duration prometheus.Histogram
	// CacheHits counts minted tokens served from the cache.
	CacheHits prometheus.Counter
	// Refreshes counts attempts to fetch the upstream key set, by result.
	Refreshes *prometheus.CounterVec
	// Ready is 1 once the signing key is loaded and the upstream key set has
	// been fetched.
	Ready prometheus.Gauge
}

// NewMetrics registers Metrics against reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tenancy_authz_checks_total",
			Help: "External authorisation checks by outcome.",
		}, []string{"outcome"}),
		Duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "tenancy_authz_check_duration_seconds",
			Help:    "Latency of one external authorisation check.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}),
		CacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tenancy_authz_mint_cache_hits_total",
			Help: "Minted tokens served from the cache instead of signed again.",
		}),
		Refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tenancy_authz_upstream_refreshes_total",
			Help: "Attempts to fetch the upstream issuer's key set, by result (ok, error).",
		}, []string{"result"}),
		Ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tenancy_authz_ready",
			Help: "1 once the signing key is loaded and the upstream key set has been fetched.",
		}),
	}
	for _, o := range []string{outcomePassThrough, outcomeMinted, outcomeUnauthenticated, outcomeForbidden, outcomeError} {
		m.Checks.WithLabelValues(o)
	}
	reg.MustRegister(m.Checks, m.Duration, m.CacheHits, m.Refreshes, m.Ready)
	return m
}
