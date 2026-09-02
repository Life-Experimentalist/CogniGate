package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// quotaCacheTTL is how long a computed quota position is reused.
//
// Usage rows are written asynchronously, so recomputing per request would cost a
// full aggregation to read data that has not moved. Five seconds bounds the
// overshoot past a hard cap to whatever one tenant can spend in five seconds,
// which is the trade GW-4 asks for: quotas are a spend control, not a
// transactional ledger.
const quotaCacheTTL = 5 * time.Second

// quotaVerdict is the outcome of one quota evaluation.
type quotaVerdict struct {
	// State is the value for X-CogniGate-Quota-State. It is reported even when
	// enforcement is off, which is what makes "observe" mode useful: an operator
	// can see who would have been rejected before making rejection real.
	State string
	// Reject is non-nil only when enforcement is on and a hard cap is reached.
	Reject *apierr.Error
}

// evaluateQuota decides whether this tenant may spend more (GW-4).
func (s *Server) evaluateQuota(ctx context.Context, tenantID string) (quotaVerdict, error) {
	ok := quotaVerdict{State: httpx.QuotaOK}
	if tenantID == "" {
		return ok, nil
	}

	entry, err := s.quotas.get(ctx, tenantID, s)
	if err != nil {
		// A quota lookup failure must not become a data-plane outage. Failing
		// open is the deliberate choice: the alternative is that a store blip
		// stops every tenant's traffic to protect a spend limit that is
		// re-checked seconds later anyway.
		s.Logger.Warn("quota evaluation failed; allowing request",
			slog.String("tenant", tenantID),
			slog.String("error", err.Error()))
		return ok, nil
	}

	verdict := quotaVerdict{State: entry.state}
	if entry.state != httpx.QuotaHardExceeded || s.Config.Quotas.Enforcement != "on" {
		return verdict, nil
	}
	if entry.spendExceeded {
		verdict.Reject = apierr.BudgetExceeded()
	} else {
		verdict.Reject = apierr.QuotaExceeded()
	}
	return verdict, nil
}

// --- the cache --------------------------------------------------------------

type quotaEntry struct {
	state         string
	spendExceeded bool
	expires       time.Time
}

type quotaCache struct {
	mu      sync.Mutex
	entries map[string]*quotaEntry
	// inflight collapses a burst of concurrent requests for the same tenant onto
	// one aggregation, so a cold cache under load does not become a thundering
	// herd against the store.
	inflight map[string]*sync.Mutex
	ttl      time.Duration
}

func newQuotaCache(ttl time.Duration) *quotaCache {
	if ttl <= 0 {
		ttl = quotaCacheTTL
	}
	return &quotaCache{
		entries:  map[string]*quotaEntry{},
		inflight: map[string]*sync.Mutex{},
		ttl:      ttl,
	}
}

func (q *quotaCache) get(ctx context.Context, tenantID string, s *Server) (*quotaEntry, error) {
	now := time.Now()

	q.mu.Lock()
	if e, ok := q.entries[tenantID]; ok && now.Before(e.expires) {
		q.mu.Unlock()
		return e, nil
	}
	lock, ok := q.inflight[tenantID]
	if !ok {
		lock = &sync.Mutex{}
		q.inflight[tenantID] = lock
	}
	q.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	// Another goroutine may have filled it while this one waited.
	q.mu.Lock()
	if e, ok := q.entries[tenantID]; ok && time.Now().Before(e.expires) {
		q.mu.Unlock()
		return e, nil
	}
	previous := q.entries[tenantID]
	q.mu.Unlock()

	entry, err := s.computeQuota(ctx, tenantID, previous)
	if err != nil {
		return nil, err
	}
	entry.expires = time.Now().Add(q.ttl)

	q.mu.Lock()
	q.entries[tenantID] = entry
	q.mu.Unlock()
	return entry, nil
}

// invalidate drops a tenant's cached position, so an admin change to the quota
// takes effect on the next request rather than after the TTL.
func (q *quotaCache) invalidate(tenantID string) {
	q.mu.Lock()
	delete(q.entries, tenantID)
	q.mu.Unlock()
}

// --- evaluation -------------------------------------------------------------

// computeQuota aggregates the current period and positions the tenant against
// its limits, emitting the GW-4 events on a transition.
func (s *Server) computeQuota(ctx context.Context, tenantID string, previous *quotaEntry) (*quotaEntry, error) {
	quota, err := s.Store.GetQuota(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// No quota configured is not the same as a quota of zero.
			return &quotaEntry{state: httpx.QuotaOK}, nil
		}
		return nil, err
	}
	if quota == nil || (quota.TokenLimit == 0 && quota.SpendLimitUSD == 0) {
		return &quotaEntry{state: httpx.QuotaOK}, nil
	}

	since, until := periodWindow(quota.Period, time.Now().UTC())
	totals, err := s.Store.Usage(ctx, tenantID, since, until)
	if err != nil {
		return nil, err
	}

	soft := quota.SoftThresholdPct
	if soft < 1 || soft > 100 {
		soft = s.Config.Quotas.DefaultSoftThresholdPct
	}

	entry := &quotaEntry{state: httpx.QuotaOK}

	tokenState := position(float64(totals.TotalTokens), float64(quota.TokenLimit), soft)
	spendState := position(totals.CostUSD, quota.SpendLimitUSD, soft)

	// The tenant's position is the worse of the two dimensions: being under
	// budget does not license spending past a token cap.
	entry.state = worse(tokenState, spendState)
	entry.spendExceeded = spendState == httpx.QuotaHardExceeded

	if s.Metrics != nil {
		s.Metrics.QuotaState.WithLabelValues(tenantID).Set(float64(quotaGauge(entry.state)))
	}

	s.emitQuotaTransition(ctx, tenantID, quota, totals, previous, entry)
	return entry, nil
}

// position places one dimension against its limit. A zero limit means that
// dimension is unlimited, so a tenant can be capped on spend without also being
// capped on tokens.
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

// periodWindow returns the half-open [since, until) of the current quota period.
// UTC throughout: a period boundary that moved with the server's timezone would
// make two deployments of the same configuration bill differently.
func periodWindow(period string, now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	if period == "month" {
		since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return since, since.AddDate(0, 1, 0)
	}
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return since, since.AddDate(0, 0, 1)
}

// emitQuotaTransition fires the GW-4 events, and only on a change of state.
//
// Emitting on every request past the threshold would deliver one webhook per
// request for the rest of the period — which is how an alerting integration gets
// muted by the people it was meant to warn.
func (s *Server) emitQuotaTransition(
	ctx context.Context,
	tenantID string,
	quota *store.Quota,
	totals store.UsageTotals,
	previous, current *quotaEntry,
) {
	if s.Events == nil || previous == nil || previous.state == current.state {
		return
	}
	if quotaGauge(current.state) <= quotaGauge(previous.state) {
		// Recovery into a new period is not an alert.
		return
	}

	data := map[string]any{
		"period":             quota.Period,
		"state":              current.state,
		"previous_state":     previous.state,
		"total_tokens":       totals.TotalTokens,
		"cost_usd":           totals.CostUSD,
		"token_limit":        quota.TokenLimit,
		"spend_limit_usd":    quota.SpendLimitUSD,
		"enforcement":        s.Config.Quotas.Enforcement,
		"soft_threshold_pct": quota.SoftThresholdPct,
	}

	event := "quota.threshold_crossed"
	if current.state == httpx.QuotaHardExceeded {
		event = "quota.hard_cap_reached"
	}
	s.Events.Emit(ctx, tenantID, event, data)
}
