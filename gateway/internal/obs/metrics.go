// Package obs holds the observability surface GW-8 specifies: the Prometheus
// series, and the structured logger every component writes through.
//
// The metric names below are a public contract. Dashboards and alerts are built
// on them, so renaming one is a breaking change even though nothing in Go
// references the string.
package obs

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the full set of series the gateway exports.
//
// Label cardinality is deliberately bounded: tenant, provider, model and status
// are all drawn from configuration or a small enumeration. Nothing derived from
// a request body ever becomes a label — that is both a cardinality explosion
// and, under GW-14, a content leak.
type Metrics struct {
	Requests         *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	UpstreamDuration *prometheus.HistogramVec
	Tokens           *prometheus.CounterVec
	Cost             *prometheus.CounterVec
	FallbackCascades *prometheus.CounterVec
	BreakerState     *prometheus.GaugeVec
	CatalogAge       *prometheus.GaugeVec
	QuotaState       *prometheus.GaugeVec
	TelemetryDropped prometheus.Counter

	registry *prometheus.Registry
}

// NewMetrics registers every series against a private registry, so the gateway
// exports exactly what it declares plus the standard Go and process collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,

		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_requests_total",
			Help: "Requests handled, by tenant, route, and response status class.",
		}, []string{"tenant", "route", "status"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cognigate_request_duration_seconds",
			Help: "End-to-end request latency as the client experiences it.",
			// Buckets span a fast cache hit to a long completion; LLM latency
			// is measured in seconds, so the default buckets would put almost
			// every observation in the overflow bucket.
			Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"tenant", "route"}),

		UpstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "cognigate_upstream_duration_seconds",
			Help:    "Time spent waiting on the upstream provider.",
			Buckets: []float64{0.01, 0.05, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"provider", "model"}),

		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_tokens_total",
			Help: "Tokens consumed, split by direction.",
		}, []string{"tenant", "provider", "model", "kind"}),

		Cost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_cost_usd_total",
			Help: "Estimated spend in USD, from catalog pricing.",
		}, []string{"tenant", "provider", "model"}),

		FallbackCascades: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_fallback_cascades_total",
			Help: "Requests served by something other than the primary candidate.",
		}, []string{"tenant", "depth"}),

		BreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cognigate_breaker_state",
			Help: "Circuit breaker position: 0 closed, 1 open, 2 half-open.",
		}, []string{"provider", "model"}),

		CatalogAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cognigate_catalog_age_seconds",
			Help: "Seconds since the tenant's model catalog was last refreshed.",
		}, []string{"tenant"}),

		QuotaState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cognigate_quota_state",
			Help: "Quota position: 0 ok, 1 soft-exceeded, 2 hard-exceeded.",
		}, []string{"tenant"}),

		TelemetryDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cognigate_telemetry_dropped_total",
			Help: "Usage records discarded because the telemetry buffer was full.",
		}),
	}

	reg.MustRegister(
		m.Requests, m.RequestDuration, m.UpstreamDuration,
		m.Tokens, m.Cost, m.FallbackCascades,
		m.BreakerState, m.CatalogAge, m.QuotaState, m.TelemetryDropped,
	)
	return m
}

// Registry exposes the gatherer for the /metrics handler.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Token direction labels.
const (
	TokenKindPrompt     = "prompt"
	TokenKindCompletion = "completion"
)
