package server

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// --- GET /v1/models ---------------------------------------------------------

// modelObject is one row of the models list.
//
// The first four fields are OpenAI's, so an existing client parses this without
// a special case. Everything after them is additive: a client that ignores the
// extensions still sees a valid /v1/models response, and one that reads them
// gets the context window and pricing it would otherwise have to hard-code.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`

	Provider          string   `json:"provider,omitempty"`
	ContextWindow     int      `json:"context_window,omitempty"`
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	InputCostPerMTok  float64  `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok float64  `json:"output_cost_per_mtok,omitempty"`
	Deprecated        bool     `json:"deprecated,omitempty"`

	// Alias marks a GW-2 name rather than a provider model id, and AliasOf says
	// what it currently resolves to. Listing aliases here rather than on a
	// separate route is what makes them discoverable to a caller that is picking
	// a model from a dropdown.
	Alias   bool   `json:"alias,omitempty"`
	AliasOf string `json:"alias_of,omitempty"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

func (s *Server) handleListModels(c *fiber.Ctx) error {
	ctx, cancel := s.opContext(c)
	defer cancel()

	tenantID := httpx.TenantID(c)
	snap, err := s.Catalog.Get(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.Unavailable("The model catalog is unavailable.").WithCause(err))
	}

	out := make([]modelObject, 0, len(snap.Models))
	for _, e := range snap.Models {
		out = append(out, entryObject(e, snap.FetchedAt))
	}
	out = append(out, s.aliasObjects(ctx, tenantID, snap)...)

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return c.JSON(modelList{Object: "list", Data: out})
}

func (s *Server) handleGetModel(c *fiber.Ctx) error {
	id := strings.Trim(param(c, "*"), "/")
	if id == "" {
		return httpx.Fail(c, apierr.ModelNotFound(""))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	tenantID := httpx.TenantID(c)
	snap, err := s.Catalog.Get(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.Unavailable("The model catalog is unavailable.").WithCause(err))
	}

	if e, ok := snap.Lookup(id); ok {
		return c.JSON(entryObject(e, snap.FetchedAt))
	}

	// Not a catalog id. It may still be an alias, and resolving it here is more
	// useful than a 404: this is how a caller finds out what "fast" means today.
	for _, obj := range s.aliasObjects(ctx, tenantID, snap) {
		if obj.ID == id {
			return c.JSON(obj)
		}
	}
	return httpx.Fail(c, apierr.ModelNotFound(id))
}

func entryObject(e catalog.Entry, fetchedAt time.Time) modelObject {
	return modelObject{
		ID:                e.ID,
		Object:            "model",
		Created:           fetchedAt.Unix(),
		OwnedBy:           e.Provider,
		Provider:          e.Provider,
		ContextWindow:     e.ContextWindow,
		MaxOutputTokens:   e.MaxOutputTokens,
		Capabilities:      e.Capabilities,
		InputCostPerMTok:  e.InputCostPerMTok,
		OutputCostPerMTok: e.OutputCostPerMTok,
		Deprecated:        e.Deprecated,
	}
}

// aliasObjects renders the tenant's aliases, each resolved against the current
// catalog. An alias that resolves to nothing is still listed — with AliasOf
// empty — because hiding it would leave an operator wondering where the alias
// they configured went.
func (s *Server) aliasObjects(ctx context.Context, tenantID string, snap *catalog.Snapshot) []modelObject {
	aliases, err := s.Store.ListAliases(ctx, tenantID)
	if err != nil {
		return nil
	}

	out := make([]modelObject, 0, len(aliases))
	for _, a := range aliases {
		obj := modelObject{
			ID:      a.Name,
			Object:  "model",
			Created: a.CreatedAt.Unix(),
			OwnedBy: "cognigate",
			Alias:   true,
		}
		if cands, _, rerr := s.Resolver.Resolve(ctx, tenantID, a.Name); rerr == nil && len(cands) > 0 {
			e := cands[0].Entry
			obj.AliasOf = e.ID
			obj.Provider = e.Provider
			obj.ContextWindow = e.ContextWindow
			obj.MaxOutputTokens = e.MaxOutputTokens
			obj.Capabilities = e.Capabilities
			obj.InputCostPerMTok = e.InputCostPerMTok
			obj.OutputCostPerMTok = e.OutputCostPerMTok
		}
		out = append(out, obj)
	}
	return out
}

// --- GET /v1/usage ----------------------------------------------------------

type usageResponse struct {
	Object string `json:"object"`
	Window string `json:"window"`
	Since  string `json:"since"`
	Until  string `json:"until"`
	store.UsageTotals
}

func (s *Server) handleUsage(c *fiber.Ctx) error {
	window, since, until, err := usageWindow(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	totals, err := s.Store.Usage(ctx, httpx.TenantID(c), since, until)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return c.JSON(usageResponse{
		Object:      "usage",
		Window:      window,
		Since:       since.Format(time.RFC3339),
		Until:       until.Format(time.RFC3339),
		UsageTotals: totals,
	})
}

type breakdownResponse struct {
	Object  string              `json:"object"`
	Window  string              `json:"window"`
	GroupBy string              `json:"group_by"`
	Since   string              `json:"since"`
	Until   string              `json:"until"`
	Data    []store.UsageBucket `json:"data"`
}

func (s *Server) handleUsageBreakdown(c *fiber.Ctx) error {
	window, since, until, err := usageWindow(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	groupBy := strings.TrimSpace(query(c, "group_by", "model"))
	switch groupBy {
	case "model", "provider", "key":
	default:
		return httpx.Fail(c, apierr.
			InvalidRequest(`group_by must be one of "model", "provider", "key".`).
			WithParam("group_by"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	buckets, err := s.Store.UsageBreakdown(ctx, httpx.TenantID(c), since, until, groupBy)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	if buckets == nil {
		buckets = []store.UsageBucket{}
	}
	return c.JSON(breakdownResponse{
		Object:  "usage_breakdown",
		Window:  window,
		GroupBy: groupBy,
		Since:   since.Format(time.RFC3339),
		Until:   until.Format(time.RFC3339),
		Data:    buckets,
	})
}

// usageWindow parses ?window=day|month into the half-open period it names. It
// reuses the quota period boundaries deliberately: a tenant checking why it was
// rejected must see the same window the rejection was computed over.
func usageWindow(c *fiber.Ctx) (string, time.Time, time.Time, error) {
	window := strings.TrimSpace(query(c, "window", "day"))
	switch window {
	case "day", "month":
	default:
		return "", time.Time{}, time.Time{}, apierr.
			InvalidRequest(`window must be "day" or "month".`).
			WithParam("window")
	}
	since, until := periodWindow(window, time.Now().UTC())
	return window, since, until, nil
}

// --- GET /v1/health ---------------------------------------------------------

// healthReport is the GW-5 view: what the gateway can currently reach, and how
// fresh what it knows is.
type healthReport struct {
	Status    string            `json:"status"` // ok | degraded
	Version   string            `json:"version"`
	Store     componentHealth   `json:"store"`
	Catalog   catalogHealth     `json:"catalog"`
	Providers []providerHealth  `json:"providers"`
	Breakers  map[string]string `json:"breakers,omitempty"`
	CheckedAt string            `json:"checked_at"`
}

type componentHealth struct {
	Kind      string `json:"kind"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

type catalogHealth struct {
	Models     int    `json:"models"`
	AgeSeconds int64  `json:"age_seconds"`
	Stale      bool   `json:"stale"`
	FetchedAt  string `json:"fetched_at,omitempty"`
}

type providerHealth struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Models  int    `json:"models"`
	Error   string `json:"error,omitempty"`
}

// healthCache holds the last report per tenant.
//
// GW-5 caches because /v1/health is what monitoring polls, and an uncached
// version would fan every poll out into a store ping plus a catalog read — which
// is how a health check becomes the load it was installed to detect.
type healthCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]healthCacheEntry
}

type healthCacheEntry struct {
	report  healthReport
	expires time.Time
}

func (h *healthCache) get(tenantID string) (healthReport, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[tenantID]
	if !ok || time.Now().After(e.expires) {
		return healthReport{}, false
	}
	return e.report, true
}

func (h *healthCache) put(tenantID string, r healthReport) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries == nil {
		h.entries = map[string]healthCacheEntry{}
	}
	ttl := h.ttl
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	h.entries[tenantID] = healthCacheEntry{report: r, expires: time.Now().Add(ttl)}
}

func (s *Server) handleHealth(c *fiber.Ctx) error {
	tenantID := httpx.TenantID(c)
	if report, ok := s.health.get(tenantID); ok {
		return s.sendHealth(c, report)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	report := s.buildHealth(ctx, tenantID)
	s.health.put(tenantID, report)
	return s.sendHealth(c, report)
}

// sendHealth answers 200 even when degraded. A degraded gateway is still
// serving, and returning 503 here would have every orchestrator restart a
// process whose only problem is that one provider's catalog is stale.
func (s *Server) sendHealth(c *fiber.Ctx, r healthReport) error {
	return c.JSON(r)
}

func (s *Server) buildHealth(ctx context.Context, tenantID string) healthReport {
	now := time.Now().UTC()
	report := healthReport{
		Status:    "ok",
		Version:   s.version(),
		Store:     componentHealth{Kind: s.Store.Kind(), Reachable: true},
		CheckedAt: now.Format(time.RFC3339),
	}

	if err := s.Store.Ping(ctx); err != nil {
		report.Store.Reachable = false
		report.Store.Error = err.Error()
		report.Status = "degraded"
	}

	providers, err := s.Store.ListProviders(ctx, tenantID)
	if err != nil {
		report.Status = "degraded"
	}

	snap, err := s.Catalog.Get(ctx, tenantID)
	switch {
	case err != nil:
		report.Status = "degraded"
	default:
		report.Catalog = catalogHealth{
			Models:     len(snap.Models),
			AgeSeconds: int64(snap.Age(now).Seconds()),
			Stale:      snap.Stale,
		}
		if !snap.FetchedAt.IsZero() {
			report.Catalog.FetchedAt = snap.FetchedAt.UTC().Format(time.RFC3339)
		}
		if snap.Stale || snap.Age(now) > s.Config.Catalog.StaleWarnAfter {
			report.Status = "degraded"
		}
	}

	modelsByProvider := map[string]int{}
	if snap != nil {
		for _, e := range snap.Models {
			modelsByProvider[e.Provider]++
		}
	}
	for _, p := range providers {
		ph := providerHealth{Name: p.Name, Enabled: p.Enabled, Models: modelsByProvider[p.Name]}
		if snap != nil {
			if msg, ok := snap.Errors[p.Name]; ok {
				ph.Error = msg
				report.Status = "degraded"
			}
		}
		report.Providers = append(report.Providers, ph)
	}

	// Only the breakers that are not closed are reported. Listing every healthy
	// pair would make the response grow with the catalog and bury the one line
	// that matters.
	//
	// The breaker is process-wide, so its snapshot holds every tenant's entries.
	// Only this tenant's are reported, and the tenant segment is stripped so the
	// keys read as "<provider>/<model>" — the same shape as X-CogniGate-Served-By.
	// Reporting the raw snapshot would tell one tenant the provider names and
	// current failures of every other tenant on the deployment.
	if s.Dispatcher != nil {
		open := map[string]string{}
		for key, state := range s.Dispatcher.Breaker().Snapshot() {
			if state == routing.StateClosed {
				continue
			}
			owner, provider, model := routing.SplitKey(key)
			if owner != tenantID {
				continue
			}
			open[provider+"/"+model] = state.String()
			report.Status = "degraded"
		}
		if len(open) > 0 {
			report.Breakers = open
		}
	}

	return report
}

// --- GET /v1/meta -----------------------------------------------------------

// metaResponse tells a client what this deployment actually supports, so a
// caller can discover the surface rather than infer it from documentation that
// may describe a different version (GW-9).
type metaResponse struct {
	Object    string            `json:"object"`
	Version   string            `json:"version"`
	Mode      string            `json:"mode"` // dev | server
	Store     string            `json:"store"`
	Planes    map[string]string `json:"planes"`
	Endpoints []string          `json:"endpoints"`
	Features  map[string]bool   `json:"features"`
	Limits    metaLimits        `json:"limits"`
}

type metaLimits struct {
	MaxRequestBytes     int64 `json:"max_request_bytes"`
	MaxResponseBytes    int64 `json:"max_response_bytes"`
	RequestTimeoutSec   int   `json:"request_timeout_seconds"`
	StreamIdleTimeout   int   `json:"stream_idle_timeout_seconds"`
	MaxConcurrentPerKey int   `json:"max_concurrent_per_key"`
	MaxFallbackDepth    int   `json:"max_fallback_depth"`
}

func (s *Server) handleMeta(c *fiber.Ctx) error {
	mode := "server"
	if s.Dev {
		mode = "dev"
	}
	return c.JSON(metaResponse{
		Object:  "meta",
		Version: s.version(),
		Mode:    mode,
		Store:   s.Store.Kind(),
		Planes: map[string]string{
			"data":  store.DataKeyPrefix,
			"admin": store.AdminKeyPrefix,
		},
		// Advertised as implemented, and nothing else. GW-9 makes this list the
		// contract: anything absent answers 404 not_supported rather than being
		// quietly proxied.
		Endpoints: []string{
			"POST /v1/chat/completions",
			"GET /v1/models",
			"GET /v1/models/{id}",
			"GET /v1/usage",
			"GET /v1/usage/breakdown",
			"GET /v1/health",
			"GET /v1/meta",
		},
		Features: map[string]bool{
			"streaming":         true,
			"aliases":           true,
			"fallback_chains":   true,
			"quotas":            true,
			"circuit_breaker":   true,
			"webhooks":          s.Events != nil,
			"response_cache":    s.Config.Cache.Enabled,
			"metrics":           s.Config.Metrics.Enabled,
			"quota_enforcement": s.Config.Quotas.Enforcement == "on",
		},
		Limits: metaLimits{
			MaxRequestBytes:     s.Config.Limits.MaxRequestBytes,
			MaxResponseBytes:    s.Config.Limits.MaxResponseBytes,
			RequestTimeoutSec:   int(s.Config.Limits.RequestTimeout.Seconds()),
			StreamIdleTimeout:   int(s.Config.Limits.StreamIdleTimeout.Seconds()),
			MaxConcurrentPerKey: s.Config.Limits.MaxConcurrentPerKey,
			MaxFallbackDepth:    s.Config.Routing.MaxFallbackDepth,
		},
	})
}
