package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

// metrics holds the Prometheus instruments and their registry. We use a private
// registry (not the global default) so each server owns its metrics — tests can
// build servers without colliding on duplicate registration, and nothing leaks
// across instances.
type metrics struct {
	registry *prometheus.Registry

	// queries counts queries by tenant and outcome (ok|throttled|error).
	queries *prometheus.CounterVec
	// latency is the query handling time in seconds, by tenant.
	latency *prometheus.HistogramVec
	// tokens counts tokens consumed by tenant (only successful charges).
	tokens *prometheus.CounterVec
}

func newMetrics() *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		queries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sluice_queries_total",
			Help: "Total queries by tenant and outcome.",
		}, []string{"tenant", "outcome"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sluice_query_duration_seconds",
			Help:    "Query handling latency in seconds, by tenant.",
			Buckets: prometheus.DefBuckets,
		}, []string{"tenant"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sluice_tokens_consumed_total",
			Help: "Rate-limiter tokens consumed by tenant.",
		}, []string{"tenant"}),
	}
	m.registry.MustRegister(m.queries, m.latency, m.tokens)
	return m
}

// observe records the outcome of one query against the Prometheus instruments.
// tokens are counted only when the query was admitted (charged).
func (m *metrics) observe(tenant, outcome string, tokens int64, latencySeconds float64) {
	m.queries.WithLabelValues(tenant, outcome).Inc()
	m.latency.WithLabelValues(tenant).Observe(latencySeconds)
	if outcome == outcomeOK {
		m.tokens.WithLabelValues(tenant).Add(float64(tokens))
	}
}

// Query outcomes, shared by the metrics and the stats collector.
const (
	outcomeOK        = "ok"
	outcomeThrottled = "throttled"
	outcomeError     = "error"
)
