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
// The four top-level fields are OpenAI's, so an existing client parses this
// without a special case. Everything CogniGate adds sits under a single
// `cognigate` key rather than beside them: a client that ignores the key still
// sees a valid /v1/models response, and nesting means OpenAI can add a field of
// its own tomorrow without colliding with one of ours.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`

	CogniGate modelExtensions `json:"cognigate"`
}

// modelExtensions is what CogniGate knows about a model that OpenAI's schema has
// nowhere to put — which provider serves it, what it costs, and whether the name
// is a GW-2 alias rather than a provider model id.
//
// Fields the provider does not expose are omitted rather than guessed: a client
// that reads context_window and gets nothing knows it has to ask the provider,
// while one that reads a plausible-looking default does not.
type modelExtensions struct {
	Provider          string   `json:"provider,omitempty"`
	ContextWindow     int      `json:"context_window,omitempty"`
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	InputCostPerMTok  float64  `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok float64  `json:"output_cost_per_mtok,omitempty"`
	Deprecated        bool     `json:"deprecated,omitempty"`
	// DiscoveredAt is when the catalog snapshot carrying this model was fetched,
	// which is how a caller tells a live listing from a stale one served through
	// a provider outage.
	DiscoveredAt string `json:"discovered_at,omitempty"`

	// Alias marks a GW-2 name rather than a provider model id, and ResolvesTo
	// says what it currently means. Listing aliases here rather than on a
	// separate route is what makes them discoverable to a caller that is picking
	// a model from a dropdown.
	Alias      bool   `json:"alias,omitempty"`
	ResolvesTo string `json:"resolves_to,omitempty"`
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
		ID:      e.ID,
		Object:  "model",
		Created: fetchedAt.Unix(),
		OwnedBy: e.Provider,
		CogniGate: modelExtensions{
			Provider:          e.Provider,
			ContextWindow:     e.ContextWindow,
			MaxOutputTokens:   e.MaxOutputTokens,
			Capabilities:      e.Capabilities,
			InputCostPerMTok:  e.InputCostPerMTok,
			OutputCostPerMTok: e.OutputCostPerMTok,
			Deprecated:        e.Deprecated,
			DiscoveredAt:      timestamp(fetchedAt),
		},
	}
}

// timestamp renders a catalog time for the wire, or nothing at all when the
// gateway has no value to report. A zero time formatted as RFC 3339 is year 1,
// which reads as data rather than as the absence of it.
func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// aliasObjects renders the tenant's aliases, each resolved against the current
// catalog. An alias that resolves to nothing is still listed — with ResolvesTo
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
			ID:        a.Name,
			Object:    "model",
			Created:   a.CreatedAt.Unix(),
			OwnedBy:   "cognigate",
			CogniGate: modelExtensions{Alias: true},
		}
		if cands, _, rerr := s.Resolver.Resolve(ctx, tenantID, a.Name); rerr == nil && len(cands) > 0 {
			e := cands[0].Entry
			obj.CogniGate.ResolvesTo = e.ID
			obj.CogniGate.Provider = e.Provider
			obj.CogniGate.ContextWindow = e.ContextWindow
			obj.CogniGate.MaxOutputTokens = e.MaxOutputTokens
			obj.CogniGate.Capabilities = e.Capabilities
			obj.CogniGate.InputCostPerMTok = e.InputCostPerMTok
			obj.CogniGate.OutputCostPerMTok = e.OutputCostPerMTok
			obj.CogniGate.DiscoveredAt = timestamp(snap.FetchedAt)
		}
		out = append(out, obj)
	}
	return out
}

// --- GET /v1/usage ----------------------------------------------------------

// usageLimit is one configured cap as the caller sees it. Every slot the quota
// actually constrains appears here; an unset slot is unlimited and has nothing
// to report, so it is absent rather than present with a cap of zero.
//
// Consumed and Remaining are reported in the slot's own unit — whole tokens or
// US dollars — and always sum to Cap, so a client can render a gauge without
// having to know which of the two it is looking at.
type usageLimit struct {
	Scope            string  `json:"scope"`  // tenant | key
	Window           string  `json:"window"` // day | month
	Unit             string  `json:"unit"`   // tokens | cost
	Cap              float64 `json:"cap"`
	SoftThresholdPct int     `json:"soft_threshold_pct"`
	Consumed         float64 `json:"consumed"`
	Remaining        float64 `json:"remaining"`
	ResetsAt         string  `json:"resets_at"`
	State            string  `json:"state"`
}

type usageResponse struct {
	Object string `json:"object"`
	Window string `json:"window"`
	Since  string `json:"since"`
	Until  string `json:"until"`
	store.UsageTotals
	// State is the caller's overall position, which is the worst of the limits
	// below — being under budget does not license passing a token cap.
	State  string       `json:"state"`
	Limits []usageLimit `json:"limits"`
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

	// Read live rather than through the quota cache: this endpoint is what a
	// caller opens to find out why it was rejected, and answering it from a
	// position computed up to five seconds ago would show them the wrong
	// numbers at exactly the moment the numbers matter.
	slots, err := s.quotaSlots(ctx, requestScope(c))
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}

	resp := usageResponse{
		Object:      "usage",
		Window:      window,
		Since:       since.Format(time.RFC3339),
		Until:       until.Format(time.RFC3339),
		UsageTotals: totals,
		State:       httpx.QuotaOK,
		// Never null: a caller with no quota configured gets an empty list, not
		// a field it has to nil-check.
		Limits: []usageLimit{},
	}
	for _, p := range slots {
		if !p.limited() {
			continue
		}
		scope := "tenant"
		if p.keyLevel {
			scope = "key"
		}
		remaining := p.cap - p.used
		if remaining < 0 {
			remaining = 0
		}
		resp.Limits = append(resp.Limits, usageLimit{
			Scope:            scope,
			Window:           p.window,
			Unit:             p.unit,
			Cap:              p.cap,
			SoftThresholdPct: p.softPct,
			Consumed:         p.used,
			Remaining:        remaining,
			ResetsAt:         p.resetsAt.Format(time.RFC3339),
			State:            p.state,
		})
		resp.State = worse(resp.State, p.state)
	}
	return c.JSON(resp)
}

type breakdownResponse struct {
	Object  string              `json:"object"`
	Window  string              `json:"window"`
	GroupBy string              `json:"group_by"`
	Since   string              `json:"since"`
	Until   string              `json:"until"`
	Data    []store.UsageBucket `json:"data"`
	// Truncated says the window held more groups than maxBreakdownBuckets, so
	// this is the top of the list and not all of it. A caller billing from these
	// rows needs to know the difference.
	Truncated bool `json:"truncated"`
}

// maxBreakdownBuckets bounds the response. Grouping by client_request_id is
// what makes this necessary — a tenant that labels every request with a
// distinct id has one group per request, and a month of traffic serialised in
// one body would exhaust the gateway rather than the client. The bound is
// applied to every grouping, because a rule with an exception is a rule nobody
// can predict, and the rows are spend-sorted so the cut falls on the cheapest.
const maxBreakdownBuckets = 200

func (s *Server) handleUsageBreakdown(c *fiber.Ctx) error {
	window, since, until, err := usageWindow(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	groupBy := strings.TrimSpace(query(c, "group_by", "model"))
	switch groupBy {
	case "model", "provider", "key", "client_request_id":
	default:
		return httpx.Fail(c, apierr.
			InvalidRequest(`group_by must be one of "model", "provider", "key", "client_request_id".`).
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
	truncated := len(buckets) > maxBreakdownBuckets
	if truncated {
		buckets = buckets[:maxBreakdownBuckets]
	}
	return c.JSON(breakdownResponse{
		Object:    "usage_breakdown",
		Window:    window,
		GroupBy:   groupBy,
		Since:     since.Format(time.RFC3339),
		Until:     until.Format(time.RFC3339),
		Data:      buckets,
		Truncated: truncated,
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

// healthReport is the GW-5 view: what the gateway can currently reach, how
// fresh what it knows is, and which of the tenant's configured names still mean
// something.
//
// Everything here is scoped to the calling tenant. A tenant learning that
// another tenant exists, or which providers it uses, would be a leak through the
// one endpoint every dashboard is expected to poll continuously.
type healthReport struct {
	Status    string           `json:"status"` // ok | degraded | unavailable
	Gateway   gatewayHealth    `json:"gateway"`
	Store     componentHealth  `json:"store"`
	Catalog   catalogHealth    `json:"catalog"`
	Providers []providerHealth `json:"providers"`
	// Aliases and Rules are never null: a tenant with none configured gets an
	// empty array, so a dashboard iterating them needs no special case.
	Aliases   []routing.NameState `json:"aliases"`
	Rules     []routing.NameState `json:"rules"`
	Quota     quotaHealth         `json:"quota"`
	CheckedAt string              `json:"checked_at"`
}

type gatewayHealth struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

type componentHealth struct {
	Kind      string `json:"kind"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

type catalogHealth struct {
	Models     int    `json:"models"`
	AgeSeconds int64  `json:"age_seconds"`
	State      string `json:"state"` // fresh | stale
	Stale      bool   `json:"stale"`
	FetchedAt  string `json:"fetched_at,omitempty"`
}

// providerHealth is one provider this tenant has registered, with the breaker
// position that decides whether traffic is currently reaching it.
type providerHealth struct {
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
	Models   int    `json:"models"`
	// Breaker is the worst position among the provider's model-scoped breakers,
	// because an operator scanning provider rows wants to be shown the tripped
	// model rather than reassured by the healthy one beside it.
	//
	// It is deliberately not a usability verdict: a provider reported open may
	// still be serving every model but one. Breakers reports which, and the
	// overall status is derived from that rather than from this field.
	Breaker      string `json:"breaker"` // closed | open | half-open
	BreakerUntil string `json:"breaker_until,omitempty"`
	// Breakers carries the per provider/model detail GW-5 asks for wherever the
	// breaker is model-scoped, which here it always is. Only non-closed entries
	// appear, so a healthy provider carries none and the common report stays
	// small.
	Breakers []modelBreaker   `json:"breakers,omitempty"`
	Catalog  catalogFreshness `json:"catalog"`
	Error    string           `json:"error,omitempty"`
}

// modelBreaker is one provider/model breaker that is not closed.
type modelBreaker struct {
	Model        string `json:"model"`
	Breaker      string `json:"breaker"` // open | half-open
	BreakerUntil string `json:"breaker_until,omitempty"`
}

type catalogFreshness struct {
	AgeSeconds int64  `json:"age_seconds"`
	State      string `json:"state"` // fresh | stale
}

type quotaHealth struct {
	State string `json:"state"` // ok | soft-exceeded | hard-exceeded
}

// healthCache holds the last report per tenant.
//
// GW-5 caches because /v1/health is what monitoring polls, and an uncached
// version would fan every poll out into a store ping plus a catalog read — which
// is how a health check becomes the load it was installed to detect.
//
// A non-positive health.cache_ttl turns the cache off rather than falling back
// to a default. An operator who writes 0 there is asking for an uncached report,
// and quietly caching for two seconds anyway would make the setting lie about
// what it does.
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
	if h.ttl <= 0 {
		return healthReport{}, false
	}
	e, ok := h.entries[tenantID]
	if !ok || time.Now().After(e.expires) {
		return healthReport{}, false
	}
	return e.report, true
}

func (h *healthCache) put(tenantID string, r healthReport) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ttl <= 0 {
		return
	}
	if h.entries == nil {
		h.entries = map[string]healthCacheEntry{}
	}
	h.entries[tenantID] = healthCacheEntry{report: r, expires: time.Now().Add(h.ttl)}
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

// sendHealth answers 200 for ok and degraded and 503 only for unavailable, so
// the same endpoint serves a dashboard that parses the body and a probe that
// reads nothing but the status code (GW-5).
//
// A degraded gateway is still serving. Returning 503 for it would have every
// orchestrator restart a process whose only problem is that one provider's
// catalog is stale — which fixes nothing and drops the traffic that was still
// succeeding.
func (s *Server) sendHealth(c *fiber.Ctx, r healthReport) error {
	if r.Status == healthUnavailable {
		return c.Status(fiber.StatusServiceUnavailable).JSON(r)
	}
	return c.JSON(r)
}

// GW-5 status values, in increasing order of severity.
const (
	healthOK          = "ok"
	healthDegraded    = "degraded"
	healthUnavailable = "unavailable"
)

func (s *Server) buildHealth(ctx context.Context, tenantID string) healthReport {
	now := time.Now().UTC()
	report := healthReport{
		Status: healthOK,
		Gateway: gatewayHealth{
			Version:       s.version(),
			UptimeSeconds: int64(now.Sub(s.startedAt).Seconds()),
		},
		Store:     componentHealth{Kind: s.Store.Kind(), Reachable: true},
		Aliases:   []routing.NameState{},
		Rules:     []routing.NameState{},
		Quota:     quotaHealth{State: httpx.QuotaOK},
		CheckedAt: now.Format(time.RFC3339),
	}

	// The store is the one dependency with no degraded mode. Everything below
	// reads through it, so if it is unreachable the rest of this report would be
	// guesses presented as facts.
	if err := s.Store.Ping(ctx); err != nil {
		report.Store.Reachable = false
		report.Store.Error = err.Error()
		report.Status = healthUnavailable
	}

	providers, err := s.Store.ListProviders(ctx, tenantID)
	if err != nil {
		report.Status = healthUnavailable
	}

	snap, err := s.Catalog.Get(ctx, tenantID)
	catalogStale := false
	switch {
	case err != nil:
		degrade(&report, healthDegraded)
	default:
		catalogStale = snap.Stale || snap.Age(now) > s.Config.Catalog.StaleWarnAfter
		report.Catalog = catalogHealth{
			Models:     len(snap.Models),
			AgeSeconds: int64(snap.Age(now).Seconds()),
			State:      freshness(catalogStale),
			Stale:      snap.Stale,
			FetchedAt:  timestamp(snap.FetchedAt),
		}
		if catalogStale {
			degrade(&report, healthDegraded)
		}
	}

	modelsByProvider := map[string]int{}
	if snap != nil {
		for _, e := range snap.Models {
			modelsByProvider[e.Provider]++
		}
	}

	// The breaker is process-wide, so its snapshot holds every tenant's entries.
	// Only this tenant's are read: reporting the raw snapshot would tell one
	// tenant the provider names and current failures of every other tenant on
	// the deployment.
	//
	// Two different things come out of this one walk, and conflating them is a
	// mistake with a 503 on the end of it. The provider row is a worst-of
	// rollup, for a reader scanning for trouble. Usability is counted per model
	// instead, because GW-5.AC-4 reserves "unavailable" for a tenant with no
	// path left at all — and a provider with one model tripped out of twelve
	// still has eleven.
	blocked := map[string]map[string]bool{}
	byProvider := map[string][]modelBreaker{}
	worst := map[string]routing.Status{}
	if s.Dispatcher != nil {
		for key, st := range s.Dispatcher.Breaker().Snapshot() {
			owner, providerName, model := routing.SplitKey(key)
			if owner != tenantID {
				continue
			}
			byProvider[providerName] = append(byProvider[providerName], modelBreaker{
				Model:        model,
				Breaker:      st.State.Health(),
				BreakerUntil: timestamp(st.Until),
			})
			if st.State == routing.StateOpen {
				if blocked[providerName] == nil {
					blocked[providerName] = map[string]bool{}
				}
				blocked[providerName][model] = true
			}
			prev, seen := worst[providerName]
			if !seen ||
				routing.BlockRank(st.State) > routing.BlockRank(prev.State) ||
				(st.State == prev.State && st.Until.After(prev.Until)) {
				worst[providerName] = st
			}
		}
	}
	for name := range byProvider {
		sort.Slice(byProvider[name], func(i, j int) bool {
			return byProvider[name][i].Model < byProvider[name][j].Model
		})
	}

	usable := 0
	for _, p := range providers {
		st := worst[p.Name] // zero value is a closed breaker, which is the default
		ph := providerHealth{
			Provider:     p.Name,
			Enabled:      p.Enabled,
			Models:       modelsByProvider[p.Name],
			Breaker:      st.State.Health(),
			BreakerUntil: timestamp(st.Until),
			Breakers:     byProvider[p.Name],
			Catalog: catalogFreshness{
				AgeSeconds: report.Catalog.AgeSeconds,
				State:      report.Catalog.State,
			},
		}
		if st.State != routing.StateClosed {
			degrade(&report, healthDegraded)
		}
		if snap != nil {
			if msg, ok := snap.Errors[p.Name]; ok {
				// A provider whose listing failed keeps whatever models the last
				// good poll found, so this is per provider rather than the
				// catalog-wide freshness above.
				ph.Error = msg
				ph.Catalog.State = freshness(true)
				degrade(&report, healthDegraded)
			}
		}
		if p.Enabled && reachable(p.Name, snap, blocked[p.Name]) {
			usable++
		}
		report.Providers = append(report.Providers, ph)
	}

	// Every path this tenant has is dead.
	//
	// Two guards, for two things that are not outages. A tenant with no enabled
	// provider is unconfigured rather than down, and answering 503 would tell an
	// operator setting CogniGate up for the first time that the gateway is
	// broken. A failed catalog read leaves no model list to count against, and
	// the providers are not the ones having that problem — it is already
	// recorded above as a degradation, which is what GW-5 calls it.
	if snap != nil && len(report.Providers) > 0 && usable == 0 {
		report.Status = healthUnavailable
	}

	if diag, err := s.Resolver.Diagnose(ctx, tenantID); err == nil {
		report.Aliases = diag.Aliases
		report.Rules = diag.Rules
		if diag.Degraded() {
			degrade(&report, healthDegraded)
		}
	}

	// Tenant-scoped deliberately: the health cache is keyed by tenant, so a
	// report that folded in the calling key's own quota would be served to the
	// tenant's other keys as if it were theirs.
	if verdict, err := s.evaluateQuota(ctx, quotaScope{tenantID: tenantID}); err == nil {
		report.Quota.State = verdict.State
		if verdict.State == httpx.QuotaHardExceeded {
			degrade(&report, healthDegraded)
		}
	}

	return report
}

// degrade lowers the reported status without ever raising it, so the order the
// checks above happen to run in cannot turn an unavailable gateway back into a
// merely degraded one.
func degrade(r *healthReport, to string) {
	if r.Status == healthOK {
		r.Status = to
	}
}

func freshness(stale bool) string {
	if stale {
		return "stale"
	}
	return "fresh"
}

// reachable reports whether any traffic could still reach a provider — whether
// the catalog lists a model for it whose breaker is not open.
//
// This is the question GW-5.AC-4 asks, and it is not the same question the
// provider row answers. A provider with one tripped model is displayed as open,
// because that is what an operator needs to see; it is still perfectly reachable
// for its other models, and calling the tenant unavailable because of it would
// return 503 from a gateway that is serving.
//
// A provider the catalog lists no models for is not reachable, because there is
// nothing to route to it. The breaker's model segment is the unqualified name
// entryCandidate builds its key from, so the same split is applied here rather
// than comparing against the catalog id.
func reachable(provider string, snap *catalog.Snapshot, open map[string]bool) bool {
	if snap == nil {
		return false
	}
	for _, e := range snap.Models {
		if e.Provider != provider {
			continue
		}
		_, model := catalog.ProviderOf(e.ID)
		if !open[model] {
			return true
		}
	}
	return false
}

// --- GET /v1/meta -----------------------------------------------------------

// apiVersion is the major the URL prefixes carry. It moves only when a change
// removes something or alters what it means; everything additive ships inside
// it (GW-9).
const apiVersion = "v1"

// metaResponse tells a client what this deployment actually supports, so a
// caller can discover the surface rather than infer it from documentation that
// may describe a different version (GW-9).
//
// The first six fields are the shape GW-9 documents, and are what a client
// feature-detects against. The rest are this implementation's own: a tolerant
// reader ignores them, and nothing in the contract depends on them.
type metaResponse struct {
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	APIVersion   string     `json:"api_version"`
	Capabilities []string   `json:"capabilities"`
	Endpoints    []string   `json:"endpoints"`
	Limits       metaLimits `json:"limits"`

	Object   string            `json:"object"`
	Mode     string            `json:"mode"` // dev | server
	Store    string            `json:"store"`
	Planes   map[string]string `json:"planes"`
	Features map[string]bool   `json:"features"`
}

type metaLimits struct {
	MaxRequestBytes     int64 `json:"max_request_bytes"`
	MaxResponseBytes    int64 `json:"max_response_bytes"`
	RequestTimeoutSec   int   `json:"request_timeout_seconds"`
	StreamIdleTimeout   int   `json:"stream_idle_timeout_seconds"`
	MaxConcurrentPerKey int   `json:"max_concurrent_per_key"`
	MaxFallbackDepth    int   `json:"max_fallback_depth"`
	// The rate limit GW-13 pairs with the concurrency cap. Published for the
	// same reason as the rest: a client that has to discover its own ceiling by
	// being refused will discover it in production.
	RequestsPerSecond int `json:"requests_per_second"`
	BurstCapacity     int `json:"burst_capacity"`
}

// metaLimits publishes the limits this caller is actually held to, which is what
// GW-13.AC-6 checks against the behaviour the rest of GW-13 observes.
//
// max_response_bytes is the deployment's, because it is the deployment's: it
// bounds what the gateway will buffer from an upstream, which is a property of
// the process rather than of whoever asked.
func (s *Server) metaLimits(c *fiber.Ctx) metaLimits {
	lim := s.limits(c)
	return metaLimits{
		MaxRequestBytes:     lim.MaxRequestBytes,
		MaxResponseBytes:    s.Config.Limits.MaxResponseBytes,
		RequestTimeoutSec:   int(lim.RequestTimeout.Seconds()),
		StreamIdleTimeout:   int(lim.StreamIdleTimeout.Seconds()),
		MaxConcurrentPerKey: lim.MaxConcurrentPerKey,
		MaxFallbackDepth:    s.Config.Routing.MaxFallbackDepth,
		RequestsPerSecond:   lim.RequestsPerSecond,
		BurstCapacity:       lim.BurstCapacity,
	}
}

func (s *Server) handleMeta(c *fiber.Ctx) error {
	return c.JSON(s.meta(c))
}

// capabilities is the list of requirement ids this deployment implements and has
// enabled, in the gw-N spelling GW-9 fixes. It is the machine-readable half of
// the compatibility contract: a client feature-detects on it, and the
// conformance suite selects which sections to run from it.
//
// The bar for adding an id is that the requirement's conformance section passes
// against this build. GW-9 makes claiming an unimplemented capability a
// conformance failure in its own right, so an id is added in the commit that
// makes it true and not in the one that intends to.
//
// GW-10 and GW-11 are never listed, whatever this deployment does. Neither is a
// behaviour of the running gateway — one is the conformance suite itself, the
// other a deployment's resource footprint — so a process has nothing to claim
// about them, and the suite exempts them from capability gating rather than
// looking for them here.
func (s *Server) capabilities() []string {
	caps := []string{
		"gw-1", // dynamic model discovery
		"gw-2", // capability aliases
		"gw-3", // fallback chains
		"gw-4", // quota & budget API
		"gw-5", // health & honest degradation
		"gw-6", // admin/config API
		"gw-7", // client/SDK contract
	}
	// Observability is a capability a deployment can turn off, and one that is
	// half-absent when it does: the request log survives, but the metric names
	// GW-8 fixes are not exported at all. Half of a requirement is not a
	// capability, so the id goes rather than being qualified.
	if s.Config.Metrics.Enabled {
		caps = append(caps, "gw-8")
	}
	return append(caps,
		"gw-9",  // versioning & compatibility
		"gw-13", // size limits & time budgets
	)
}

// meta builds the document both planes serve. GW-9 requires /admin/v1/meta to
// answer the same thing /v1/meta does, so there is one builder and no second
// list of endpoints to fall out of step with the first.
//
// It is rebuilt per call rather than cached, and takes the request because the
// limits block is the caller's own: GW-13 lets a tenant be held to lower
// ceilings than the deployment, and a meta document that published the
// deployment's would tell that tenant the wrong numbers about its own traffic.
// Everything else in it reads a value fixed at startup — configuration, the
// build's version, the store's kind — so building it costs a few map
// allocations against a 50 ms budget.
func (s *Server) meta(c *fiber.Ctx) metaResponse {
	mode := "server"
	if s.Dev {
		mode = "dev"
	}
	return metaResponse{
		Name:         "cognigate",
		Version:      s.version(),
		APIVersion:   apiVersion,
		Capabilities: s.capabilities(),
		// Endpoint families, not routes. A client asking "can I request a
		// completion here" wants one answer, not one per verb and path
		// template — and the paths are already implied by api_version.
		// Advertised as implemented, and nothing else: anything absent
		// answers 404 not_supported rather than being quietly proxied.
		Endpoints: []string{
			"chat.completions",
			"models",
			"usage",
			"usage.breakdown",
			"health",
			"meta",
		},
		Limits: s.metaLimits(c),
		Object: "meta",
		Mode:   mode,
		Store:  s.Store.Kind(),
		Planes: map[string]string{
			"data":  store.DataKeyPrefix,
			"admin": store.AdminKeyPrefix,
		},
		// Finer grained than capabilities, and not a substitute for it: these
		// name sub-behaviours within a requirement — whether quotas reject or
		// only report, whether a webhook can be registered — which a gw-N id
		// is too coarse to express.
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
	}
}
