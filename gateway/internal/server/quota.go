package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/events"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// quotaCacheTTL is how long a computed quota position is reused.
//
// Usage rows are written asynchronously, so recomputing per request would cost a
// full aggregation to read data that has not moved. Five seconds bounds the
// overshoot past a hard cap to whatever one holder can spend in five seconds,
// which is the trade GW-4 asks for: quotas are a spend control, not a
// transactional ledger. It also keeps an admin change inside GW-4's ten-second
// bound even if the invalidation on the write path were ever missed.
const quotaCacheTTL = 5 * time.Second

// The two units a quota is measured in, and the two windows it is measured over.
const (
	unitTokens = "tokens"
	unitCost   = "cost"

	windowDay   = "day"
	windowMonth = "month"
)

// quotaVerdict is the outcome of one quota evaluation.
type quotaVerdict struct {
	// State is the value for X-CogniGate-Quota-State. It is reported even when
	// enforcement is off, which is what makes "observe" mode useful: an operator
	// can see who would have been rejected before making rejection real.
	State string
	// Reject is non-nil only when enforcement is on and a hard cap is reached.
	Reject *apierr.Error
}

// quotaScope names whose consumption is being measured. A key-level quota
// further constrains the tenant's, so both are evaluated together and the
// stricter answer wins — which is what makes it impossible for a key quota to
// widen what the tenant is allowed.
type quotaScope struct {
	tenantID  string
	keyID     string
	keyPrefix string
}

func (q quotaScope) cacheKey() string { return q.tenantID + "\x00" + q.keyID }

// requestScope names whose quota governs this request: the tenant, and the key
// that authenticated it, so a key-level cap is evaluated alongside its
// tenant's.
func requestScope(c *fiber.Ctx) quotaScope {
	scope := quotaScope{tenantID: httpx.TenantID(c)}
	if k := httpx.Key(c); k != nil {
		scope.keyID, scope.keyPrefix = k.ID, k.Prefix
	}
	return scope
}

// evaluateQuota decides whether this caller may spend more (GW-4).
func (s *Server) evaluateQuota(ctx context.Context, scope quotaScope) (quotaVerdict, error) {
	ok := quotaVerdict{State: httpx.QuotaOK}
	if scope.tenantID == "" {
		return ok, nil
	}

	entry, err := s.quotas.get(ctx, scope, s)
	if err != nil {
		// A quota lookup failure must not become a data-plane outage. Failing
		// open is the deliberate choice: the alternative is that a store blip
		// stops every tenant's traffic to protect a spend limit that is
		// re-checked seconds later anyway.
		s.Logger.Warn("quota evaluation failed; allowing request",
			slog.String("tenant", scope.tenantID),
			slog.String("error", err.Error()))
		return ok, nil
	}

	verdict := quotaVerdict{State: entry.state}
	if entry.state != httpx.QuotaHardExceeded || s.Config.Quotas.Enforcement != "on" {
		return verdict, nil
	}

	// The binding slot decides both which failure this is and how long the
	// caller has to wait, so the two can never disagree.
	if entry.binding.unit == unitCost {
		verdict.Reject = apierr.BudgetExceeded()
	} else {
		verdict.Reject = apierr.QuotaExceeded()
	}
	verdict.Reject = verdict.Reject.WithRetryAfter(time.Until(entry.binding.resetsAt))
	return verdict, nil
}

// --- the cache --------------------------------------------------------------

// slotPosition is one evaluated cap slot.
type slotPosition struct {
	window   string
	unit     string
	state    string
	used     float64
	cap      float64
	softPct  int
	since    time.Time
	resetsAt time.Time
	// keyLevel distinguishes a slot that came from the key's own quota, so an
	// operator reading an event can tell which of the two rejected.
	keyLevel bool
}

// limited reports whether this slot constrains anything at all.
func (p slotPosition) limited() bool { return p.cap > 0 }

type quotaEntry struct {
	state string
	// binding is the slot responsible for the entry's state. For a rejection it
	// is the slot the caller has to wait out.
	binding slotPosition
	expires time.Time
}

type quotaCache struct {
	mu      sync.Mutex
	entries map[string]*quotaEntry
	// inflight collapses a burst of concurrent requests for the same scope onto
	// one aggregation, so a cold cache under load does not become a thundering
	// herd against the store.
	inflight map[string]*sync.Mutex
	// announced remembers the last state an event was emitted for, per slot and
	// per window. It deliberately survives invalidate() and a cold entry: GW-4
	// asks for one event per window crossing, and a memory that lived on the
	// cached position would re-announce every time an admin touched the quota.
	announced map[string]announcement
	ttl       time.Duration
}

// announcement is the last state announced for one slot, together with the
// window it was announced for. A new window resets the memory, which is what
// makes the next period's crossing a fresh event rather than a duplicate.
type announcement struct {
	since time.Time
	state string
}

func newQuotaCache(ttl time.Duration) *quotaCache {
	if ttl <= 0 {
		ttl = quotaCacheTTL
	}
	return &quotaCache{
		entries:   map[string]*quotaEntry{},
		inflight:  map[string]*sync.Mutex{},
		announced: map[string]announcement{},
		ttl:       ttl,
	}
}

func (q *quotaCache) get(ctx context.Context, scope quotaScope, s *Server) (*quotaEntry, error) {
	key := scope.cacheKey()
	now := time.Now()

	q.mu.Lock()
	if e, ok := q.entries[key]; ok && now.Before(e.expires) {
		q.mu.Unlock()
		return e, nil
	}
	lock, ok := q.inflight[key]
	if !ok {
		lock = &sync.Mutex{}
		q.inflight[key] = lock
	}
	q.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	// Another goroutine may have filled it while this one waited.
	q.mu.Lock()
	if e, ok := q.entries[key]; ok && time.Now().Before(e.expires) {
		q.mu.Unlock()
		return e, nil
	}
	q.mu.Unlock()

	entry, err := s.computeQuota(ctx, scope)
	if err != nil {
		return nil, err
	}
	entry.expires = time.Now().Add(q.ttl)

	q.mu.Lock()
	q.entries[key] = entry
	q.mu.Unlock()
	return entry, nil
}

// invalidate drops a tenant's cached positions, so an admin change to a quota
// takes effect on the next request rather than after the TTL. Every key scoped
// to the tenant goes with it: raising the tenant's cap has to unblock the keys
// that were rejected under the old one.
//
// What survives is the announcement memory, so re-evaluating after an admin
// write does not re-announce a crossing the tenant has already been told about.
func (q *quotaCache) invalidate(tenantID string) {
	prefix := tenantID + "\x00"
	q.mu.Lock()
	for k := range q.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(q.entries, k)
		}
	}
	q.mu.Unlock()
}

// shouldAnnounce reports whether a slot's state is a rise this window has not
// been told about yet, and records it when it is.
func (q *quotaCache) shouldAnnounce(scopeKey string, p slotPosition) bool {
	if quotaGauge(p.state) == 0 {
		return false
	}
	key := scopeKey + "\x00" + p.window + "\x00" + p.unit

	q.mu.Lock()
	defer q.mu.Unlock()

	prev, ok := q.announced[key]
	// A different window is a different crossing, whatever was announced for
	// the last one.
	if ok && prev.since.Equal(p.since) && quotaGauge(p.state) <= quotaGauge(prev.state) {
		return false
	}
	q.announced[key] = announcement{since: p.since, state: p.state}
	return true
}

// --- evaluation -------------------------------------------------------------

// computeQuota positions a caller against every slot that applies to it, and
// announces any crossing.
func (s *Server) computeQuota(ctx context.Context, scope quotaScope) (*quotaEntry, error) {
	slots, err := s.quotaSlots(ctx, scope)
	if err != nil {
		return nil, err
	}

	entry := &quotaEntry{state: httpx.QuotaOK}
	for _, p := range slots {
		if quotaGauge(p.state) > quotaGauge(entry.state) {
			entry.state, entry.binding = p.state, p
			continue
		}
		// Among slots at the same state, the binding one is whichever holds the
		// caller longest: retrying before then is certain to fail again.
		if p.state == entry.state && quotaGauge(p.state) > 0 &&
			p.resetsAt.After(entry.binding.resetsAt) {
			entry.binding = p
		}
	}

	if s.Metrics != nil {
		s.Metrics.QuotaState.WithLabelValues(scope.tenantID).Set(float64(quotaGauge(entry.state)))
	}

	for _, p := range slots {
		if s.quotas.shouldAnnounce(scope.cacheKey(), p) {
			s.emitQuotaCrossing(ctx, scope, p)
		}
	}
	return entry, nil
}

// quotaSlots evaluates every cap that applies to a scope: the tenant's own, and
// the key's when the key has one of its own.
func (s *Server) quotaSlots(ctx context.Context, scope quotaScope) ([]slotPosition, error) {
	now := time.Now().UTC()

	tenantQuota, err := s.loadQuota(ctx, scope.tenantID, "")
	if err != nil {
		return nil, err
	}
	var slots []slotPosition
	if tenantQuota != nil {
		totals, err := s.usageFor(ctx, scope.tenantID, "", now, tenantQuota)
		if err != nil {
			return nil, err
		}
		slots = append(slots, s.positions(tenantQuota, totals, now, false)...)
	}

	if scope.keyID != "" {
		keyQuota, err := s.loadQuota(ctx, scope.tenantID, scope.keyID)
		if err != nil {
			return nil, err
		}
		if keyQuota != nil {
			totals, err := s.usageFor(ctx, scope.tenantID, scope.keyPrefix, now, keyQuota)
			if err != nil {
				return nil, err
			}
			slots = append(slots, s.positions(keyQuota, totals, now, true)...)
		}
	}
	return slots, nil
}

// loadQuota reads one quota, treating both "no such quota" and "a quota that
// constrains nothing" as no quota. Neither is a cap of zero.
func (s *Server) loadQuota(ctx context.Context, tenantID, keyID string) (*store.Quota, error) {
	q, err := s.Store.GetQuota(ctx, tenantID, keyID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if q == nil || q.Empty() {
		return nil, nil
	}
	return q, nil
}

// windowTotals is the consumption behind one quota, aggregated once per window
// rather than once per slot: the two units of a window come from the same rows.
type windowTotals struct {
	day   store.UsageTotals
	month store.UsageTotals
}

// usageFor aggregates a scope's consumption over whichever windows the quota
// actually constrains. A quota with only monthly limits does not pay for a daily
// aggregation.
func (s *Server) usageFor(
	ctx context.Context, tenantID, keyPrefix string, now time.Time, q *store.Quota,
) (windowTotals, error) {
	var out windowTotals
	read := func(window string) (store.UsageTotals, error) {
		since, until := periodWindow(window, now)
		if keyPrefix == "" {
			return s.Store.Usage(ctx, tenantID, since, until)
		}
		return s.Store.KeyUsage(ctx, tenantID, keyPrefix, since, until)
	}

	if q.Day.Tokens != nil || q.Day.Cost != nil {
		totals, err := read(windowDay)
		if err != nil {
			return out, err
		}
		out.day = totals
	}
	if q.Month.Tokens != nil || q.Month.Cost != nil {
		totals, err := read(windowMonth)
		if err != nil {
			return out, err
		}
		out.month = totals
	}
	return out, nil
}

// positions turns a quota and its consumption into one entry per configured
// slot. Unset slots produce nothing: they are unlimited, not satisfied.
func (s *Server) positions(
	q *store.Quota, totals windowTotals, now time.Time, keyLevel bool,
) []slotPosition {
	var out []slotPosition
	add := func(window, unit string, limit *store.QuotaLimit, used float64) {
		if limit == nil {
			return
		}
		since, resets := periodWindow(window, now)
		soft := limit.SoftThresholdPct
		if soft < 1 || soft > 100 {
			soft = s.Config.Quotas.DefaultSoftThresholdPct
		}
		out = append(out, slotPosition{
			window:   window,
			unit:     unit,
			state:    position(used, limit.Cap, soft),
			used:     used,
			cap:      limit.Cap,
			softPct:  soft,
			since:    since,
			resetsAt: resets,
			keyLevel: keyLevel,
		})
	}

	add(windowDay, unitTokens, q.Day.Tokens, float64(totals.day.TotalTokens))
	add(windowDay, unitCost, q.Day.Cost, totals.day.CostUSD)
	add(windowMonth, unitTokens, q.Month.Tokens, float64(totals.month.TotalTokens))
	add(windowMonth, unitCost, q.Month.Cost, totals.month.CostUSD)
	return out
}

// position places one slot against its cap. A cap of zero or less means the slot
// is unlimited, so a holder can be capped on spend without also being capped on
// tokens.
func position(used, limit float64, softPct int) string {
	if limit <= 0 {
		return httpx.QuotaOK
	}
	if used >= limit {
		return httpx.QuotaHardExceeded
	}
	if used >= limit*float64(softPct)/100 {
		return httpx.QuotaSoftExceeded
	}
	return httpx.QuotaOK
}

func quotaGauge(state string) int {
	switch state {
	case httpx.QuotaSoftExceeded:
		return 1
	case httpx.QuotaHardExceeded:
		return 2
	default:
		return 0
	}
}

func worse(a, b string) string {
	if quotaGauge(a) >= quotaGauge(b) {
		return a
	}
	return b
}

// periodWindow returns the half-open [since, until) of the named window.
// UTC throughout: a boundary that moved with the server's timezone would make
// two deployments of the same configuration bill differently.
func periodWindow(period string, now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	if period == windowMonth {
		since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return since, since.AddDate(0, 1, 0)
	}
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return since, since.AddDate(0, 0, 1)
}

// emitQuotaCrossing fires the GW-4 events.
//
// Emitting on every request past the threshold would deliver one webhook per
// request for the rest of the window — which is how an alerting integration gets
// muted by the people it was meant to warn. The caller has already established
// that this is a state the window has not announced yet.
func (s *Server) emitQuotaCrossing(ctx context.Context, scope quotaScope, p slotPosition) {
	if s.Events == nil {
		return
	}

	data := map[string]any{
		"window":             p.window,
		"unit":               p.unit,
		"state":              p.state,
		"used":               p.used,
		"cap":                p.cap,
		"soft_threshold_pct": p.softPct,
		"resets_at":          p.resetsAt.Format(time.RFC3339),
		"scope":              "tenant",
		"enforcement":        s.Config.Quotas.Enforcement,
	}
	if p.keyLevel {
		data["scope"] = "key"
		// The prefix, never the key: GW-14 keeps credentials out of anything
		// that leaves the gateway.
		data["key_prefix"] = scope.keyPrefix
	}

	event := events.QuotaThresholdCrossed
	if p.state == httpx.QuotaHardExceeded {
		event = events.QuotaHardCapReached
	}
	s.Events.Emit(ctx, scope.tenantID, event, data)
}
