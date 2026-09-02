// Package catalog implements GW-1: a live, per-tenant view of every model the
// tenant's registered providers can actually serve.
//
// The catalog is what makes routing honest. Without it the gateway would accept
// a request for a model that no configured provider offers and only discover
// the mistake after paying for a round trip; with it, an unknown model is a
// 404 before any upstream call.
package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// Entry is one model plus the provider that serves it.
type Entry struct {
	store.Model
	// ProviderID is the registered provider record, so routing can find the
	// credential pool without a second lookup by name.
	ProviderID string `json:"-"`
}

// Snapshot is an immutable view handed to callers. Returning a snapshot rather
// than the live map means a request that is mid-resolution is never disturbed
// by a concurrent refresh.
type Snapshot struct {
	Models    []Entry
	byID      map[string]Entry
	FetchedAt time.Time
	// Stale is true when the last refresh failed and this is the previous
	// good data. GW-1 requires serving stale over failing: a provider's
	// /models endpoint being down is not a reason to stop routing traffic that
	// would otherwise succeed.
	Stale bool
	// Errors records per-provider refresh failures, surfaced by /v1/health.
	Errors map[string]string
}

// Lookup finds a model by exact id.
func (s *Snapshot) Lookup(id string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	e, ok := s.byID[id]
	return e, ok
}

// Age is how long ago the underlying data was fetched. GW-5 and the
// cognigate_catalog_age_seconds metric both read this.
func (s *Snapshot) Age(now time.Time) time.Duration {
	if s == nil || s.FetchedAt.IsZero() {
		return 0
	}
	return now.Sub(s.FetchedAt)
}

// Options configures a Catalog.
type Options struct {
	TTL             time.Duration
	StaleWarnAfter  time.Duration
	ProviderTimeout time.Duration
	// OnChange is called when a refresh adds or removes models, so the caller
	// can emit catalog.model_added / catalog.model_removed events. It runs on
	// the refresh goroutine and must not block.
	OnChange func(tenantID string, added, removed []string)
}

// Catalog caches provider model lists per tenant.
type Catalog struct {
	store    store.Store
	registry *provider.Registry
	opts     Options

	mu sync.Mutex
	// perTenant guards one refresh per tenant, so a burst of concurrent
	// requests for a cold tenant produces one upstream fetch rather than one
	// per request.
	perTenant map[string]*tenantState

	now func() time.Time
}

type tenantState struct {
	mu       sync.Mutex
	snapshot *Snapshot
}

func New(s store.Store, registry *provider.Registry, opts Options) *Catalog {
	if opts.TTL <= 0 {
		opts.TTL = time.Hour
	}
	if opts.ProviderTimeout <= 0 {
		opts.ProviderTimeout = 10 * time.Second
	}
	return &Catalog{
		store:     s,
		registry:  registry,
		opts:      opts,
		perTenant: map[string]*tenantState{},
		now:       time.Now,
	}
}

func (c *Catalog) state(tenantID string) *tenantState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.perTenant[tenantID]
	if !ok {
		st = &tenantState{}
		c.perTenant[tenantID] = st
	}
	return st
}

// Get returns the tenant's catalog, refreshing it if the TTL has elapsed.
//
// A refresh that fails does not fail the call: the previous snapshot is
// returned with Stale set. Only a cold tenant — no snapshot at all — can
// surface the error, because there is nothing to serve instead.
func (c *Catalog) Get(ctx context.Context, tenantID string) (*Snapshot, error) {
	st := c.state(tenantID)

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.snapshot != nil && st.snapshot.Age(c.now()) < c.opts.TTL && !st.snapshot.Stale {
		return st.snapshot, nil
	}
	return c.reload(ctx, tenantID, st)
}

// Refresh polls every provider now, whatever the TTL says. It is the on-demand
// refresh GW-1 requires the admin plane to expose, and it exists as a separate
// method from Invalidate because the two mean different things: Invalidate
// discards what is known, which is right when the tenant's configuration has
// changed, while Refresh re-reads it and keeps the old snapshot if the read
// fails. Invalidating in order to refresh would turn one unreachable provider
// into a cold tenant and a 503.
//
// Only one refresh per tenant runs at a time, since the tenant's state lock is
// held across the poll — and because providers are tenant-scoped, that is also
// the "at most one in-flight refresh per provider" the spec asks for.
func (c *Catalog) Refresh(ctx context.Context, tenantID string) (*Snapshot, error) {
	st := c.state(tenantID)

	st.mu.Lock()
	defer st.mu.Unlock()

	return c.reload(ctx, tenantID, st)
}

// reload polls and installs the result. The caller holds st.mu.
//
// A failed poll marks the existing snapshot stale rather than evicting it:
// GW-1 requires serving a known-old catalog over serving none, because a
// provider's listing endpoint being down is not a reason to stop routing
// traffic that would otherwise succeed.
func (c *Catalog) reload(ctx context.Context, tenantID string, st *tenantState) (*Snapshot, error) {
	fresh, err := c.refresh(ctx, tenantID)
	if err != nil {
		if st.snapshot != nil {
			stale := *st.snapshot
			stale.Stale = true
			st.snapshot = &stale
			return st.snapshot, nil
		}
		return nil, err
	}

	if st.snapshot != nil && c.opts.OnChange != nil {
		added, removed := diff(st.snapshot, fresh)
		if len(added) > 0 || len(removed) > 0 {
			c.opts.OnChange(tenantID, added, removed)
		}
	}
	st.snapshot = fresh
	return fresh, nil
}

// Invalidate drops a tenant's cached catalog, so the next read refetches. Called
// when a provider is added or removed: waiting out the TTL after registering a
// provider would make the admin API feel broken.
func (c *Catalog) Invalidate(tenantID string) {
	st := c.state(tenantID)
	st.mu.Lock()
	st.snapshot = nil
	st.mu.Unlock()
}

// refresh fetches every enabled provider's model list and merges them.
func (c *Catalog) refresh(ctx context.Context, tenantID string) (*Snapshot, error) {
	providers, err := c.store.ListProviders(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("catalog: listing providers: %w", err)
	}

	snap := &Snapshot{
		byID:      map[string]Entry{},
		FetchedAt: c.now(),
		Errors:    map[string]string{},
	}

	// Providers are polled sequentially rather than in parallel. A tenant has a
	// handful of providers, the result is cached for an hour, and sequential
	// polling keeps one slow provider from being masked by the others' speed
	// in the health report.
	for _, p := range providers {
		if !p.Enabled || len(p.Keys) == 0 {
			continue
		}
		adapter, ok := c.registry.Get(p.Kind)
		if !ok {
			snap.Errors[p.Name] = fmt.Sprintf("no adapter for kind %q", p.Kind)
			continue
		}

		fetchCtx, cancel := context.WithTimeout(ctx, c.opts.ProviderTimeout)
		models, err := adapter.ListModels(fetchCtx, provider.Credential{
			BaseURL: p.BaseURL,
			APIKey:  p.Keys[0],
		})
		cancel()
		if err != nil {
			snap.Errors[p.Name] = err.Error()
			continue
		}

		for _, m := range models {
			m.Provider = p.Name
			entry := Entry{Model: m, ProviderID: p.ID}
			// First provider to claim an id wins. Registration order is the
			// tenant's stated preference, so honouring it here means a tenant
			// can pick which account serves a model both of theirs offer.
			if _, taken := snap.byID[m.ID]; !taken {
				snap.byID[m.ID] = entry
				snap.Models = append(snap.Models, entry)
			}
			// The qualified form is always addressable, so a caller can pin a
			// specific provider's copy of a shared model id.
			qualified := p.Name + "/" + m.ID
			if _, taken := snap.byID[qualified]; !taken {
				q := entry
				q.ID = qualified
				snap.byID[qualified] = q
			}
		}
	}

	// Every provider failed and none had cached data: that is a hard failure,
	// not an empty catalog. Reporting zero models would turn a transient
	// outage into a stream of 404s that look like a configuration error.
	if len(snap.Models) == 0 && len(snap.Errors) > 0 {
		return nil, fmt.Errorf("catalog: every provider failed to refresh")
	}

	sort.Slice(snap.Models, func(i, j int) bool {
		if snap.Models[i].Provider != snap.Models[j].Provider {
			return snap.Models[i].Provider < snap.Models[j].Provider
		}
		return snap.Models[i].ID < snap.Models[j].ID
	})
	return snap, nil
}

// diff reports model ids added and removed between two snapshots, for the
// catalog.model_added / catalog.model_removed events.
func diff(old, fresh *Snapshot) (added, removed []string) {
	oldIDs := map[string]bool{}
	for _, e := range old.Models {
		oldIDs[e.ID] = true
	}
	freshIDs := map[string]bool{}
	for _, e := range fresh.Models {
		freshIDs[e.ID] = true
		if !oldIDs[e.ID] {
			added = append(added, e.ID)
		}
	}
	for id := range oldIDs {
		if !freshIDs[id] {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// ProviderOf splits a qualified "provider/model" reference. An unqualified name
// yields an empty provider, meaning "whichever provider serves it".
func ProviderOf(ref string) (providerName, modelID string) {
	if i := strings.Index(ref, "/"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}
