// Package obs holds the observability surface GW-8 specifies: the Prometheus
// series, and the structured logger every component writes through.
//
// The metric names below are a public contract. Dashboards and alerts are built
// on them, so renaming one is a breaking change even though nothing in Go
// references the string.
package obs

import (
	"sync"

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
	QuotaState       *prometheus.GaugeVec
	TelemetryDropped prometheus.Counter

	catalogAge *catalogAgeCollector
	registry   *prometheus.Registry
}

// NewMetrics registers every series against a private registry, so the gateway
// exports exactly what it declares plus the standard Go and process collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,

		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_requests_total",
			Help: "Requests handled, by tenant, provider, model, route and HTTP status code.",
			// `code` is the exact status ("200", "429"), not a class. That is
			// what promhttp's own instrumentation means by the label, so a
			// dashboard written against the convention works here, and the
			// value set is bounded by the status codes the registry defines.
			// Routes that never reach a provider — /v1/meta, an unauthenticated
			// request — carry empty provider and model rather than a
			// placeholder, so a query summing by provider does not invent one.
		}, []string{"tenant", "provider", "model", "route", "code"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cognigate_request_duration_seconds",
			Help: "End-to-end request latency as the client experiences it.",
			// Buckets span a fast cache hit to a long completion; LLM latency
			// is measured in seconds, so the default buckets would put almost
			// every observation in the overflow bucket.
			Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"tenant", "provider", "route"}),

		UpstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "cognigate_upstream_duration_seconds",
			Help:    "Time spent waiting on the upstream provider.",
			Buckets: []float64{0.01, 0.05, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"provider", "model"}),

		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_tokens_total",
			Help: "Tokens consumed, split by direction.",
		}, []string{"tenant", "provider", "model", "direction"}),

		Cost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_cost_usd_total",
			Help: "Estimated spend in USD, from catalog pricing.",
		}, []string{"tenant", "provider", "model"}),

		FallbackCascades: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cognigate_fallback_cascades_total",
			Help: "Cascade hops taken, by the pair of models the hop moved between.",
			// One increment per hop, not per request: a request that fell
			// through two candidates to reach a third contributes two rows, and
			// the pair on each says which link of the chain carried it. `reason`
			// is drawn from the small failure enumeration — never upstream error
			// text, which is unbounded and, under GW-14, may quote the request.
		}, []string{"tenant", "from_model", "to_model", "reason"}),

		BreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cognigate_breaker_state",
			Help: "Circuit breaker position: 0 closed, 1 half-open, 2 open.",
			// `tenant` is beyond the labels GW-8 names, which are a minimum. It
			// has to be here: a breaker is keyed by tenant because a provider
			// name is only unique within one, so without it two tenants'
			// "primary" would fight over a single series.
		}, []string{"tenant", "provider", "model"}),

		QuotaState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cognigate_quota_state",
			Help: "Quota position per slot: 0 ok, 1 soft-exceeded, 2 hard-exceeded.",
		}, []string{"tenant", "window", "unit"}),

		TelemetryDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cognigate_telemetry_dropped_total",
			Help: "Usage records discarded without being persisted: the telemetry buffer was full, the write failed permanently, or the process shut down before it could be retried.",
		}),
	}

	m.catalogAge = &catalogAgeCollector{
		desc: prometheus.NewDesc(
			"cognigate_catalog_age_seconds",
			"Seconds since the catalog backing this tenant's provider was last refreshed.",
			[]string{"tenant", "provider"}, nil),
	}

	reg.MustRegister(
		m.Requests, m.RequestDuration, m.UpstreamDuration,
		m.Tokens, m.Cost, m.FallbackCascades,
		m.BreakerState, m.QuotaState, m.TelemetryDropped, m.catalogAge,
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

// CatalogAgeSample is one catalog's age, as of the moment it is asked for.
type CatalogAgeSample struct {
	Tenant   string
	Provider string
	Seconds  float64
}

// SetCatalogAgeSource installs the function the catalog-age gauge reads.
//
// Age cannot be a gauge that a refresh sets, because the number a refresh would
// write is zero — and zero is what the series would then report forever, which
// is precisely backwards for a metric whose only purpose is to alert on a
// catalog that has stopped refreshing. So it is computed at scrape time
// instead, and the source walks the live catalog on every collect.
func (m *Metrics) SetCatalogAgeSource(fn func() []CatalogAgeSample) {
	if m == nil || m.catalogAge == nil {
		return
	}
	m.catalogAge.mu.Lock()
	m.catalogAge.source = fn
	m.catalogAge.mu.Unlock()
}

type catalogAgeCollector struct {
	desc *prometheus.Desc

	mu     sync.Mutex
	source func() []CatalogAgeSample
}

func (c *catalogAgeCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *catalogAgeCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	source := c.source
	c.mu.Unlock()
	if source == nil {
		return
	}
	for _, s := range source() {
		ch <- prometheus.MustNewConstMetric(
			c.desc, prometheus.GaugeValue, s.Seconds, s.Tenant, s.Provider)
	}
}
