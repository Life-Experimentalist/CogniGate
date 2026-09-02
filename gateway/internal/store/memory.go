package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxMemoryUsageRecords bounds the in-memory usage log. `cognigate --dev` is a
// long-lived process on a laptop; without a bound, a load test against it would
// exhaust memory. Oldest records are dropped first, which is the right trade
// for a store that makes no durability promise anyway.
const maxMemoryUsageRecords = 50_000

// Memory is the dependency-free Store. It backs `cognigate --dev` (GW-11) and
// the conformance suite's embedded mode, so the full admin plane works with no
// Postgres and no Redis.
//
// Nothing here is durable: a restart is a clean slate. That is deliberate and
// stated in the docs, not an omission to be fixed later.
type Memory struct {
	mu sync.RWMutex

	tenants    map[string]*Tenant
	keys       map[string]*APIKey // by ID
	keysByHash map[string]*APIKey
	providers  map[string][]*Provider // by tenant
	aliases    map[string][]*Alias
	routes     map[string][]*Route
	quotas     map[string]*Quota
	webhooks   map[string][]*Webhook
	usage      map[string][]*UsageRecord

	// dev marks keys minted here with a visible dev- infix.
	dev bool
	now func() time.Time
}

// NewMemory builds an empty store. `dev` should be true for `--dev`, so that
// every credential it mints is self-identifying as throwaway.
func NewMemory(dev bool) *Memory {
	return &Memory{
		tenants:    map[string]*Tenant{},
		keys:       map[string]*APIKey{},
		keysByHash: map[string]*APIKey{},
		providers:  map[string][]*Provider{},
		aliases:    map[string][]*Alias{},
		routes:     map[string][]*Route{},
		quotas:     map[string]*Quota{},
		webhooks:   map[string][]*Webhook{},
		usage:      map[string][]*UsageRecord{},
		dev:        dev,
		now:        time.Now,
	}
}

var _ Store = (*Memory)(nil)

func (m *Memory) Kind() string {
	if m.dev {
		return "memory-dev"
	}
	return "memory"
}

func (m *Memory) Ping(context.Context) error { return nil }

// --- auth ------------------------------------------------------------------

func (m *Memory) ResolveKey(_ context.Context, plaintext string) (*APIKey, *Tenant, error) {
	hash := HashAPIKey(plaintext)

	m.mu.RLock()
	defer m.mu.RUnlock()

	key, ok := m.keysByHash[hash]
	if !ok || !key.Active(m.now()) {
		return nil, nil, ErrNotFound
	}
	// A root admin key belongs to no tenant; that is not an error, it is the
	// whole point of root scope.
	var tenant *Tenant
	if key.TenantID != "" {
		tenant = m.tenants[key.TenantID]
		if tenant == nil {
			return nil, nil, ErrNotFound
		}
	}
	return cloneKey(key), cloneTenant(tenant), nil
}

// --- tenants ---------------------------------------------------------------

func (m *Memory) CreateTenant(_ context.Context, name string) (*Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: tenant name is required", ErrConflict)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.tenants {
		if strings.EqualFold(t.Name, name) {
			return nil, fmt.Errorf("%w: a tenant named %q already exists", ErrConflict, name)
		}
	}
	t := &Tenant{ID: NewID(IDTenant), Name: name, Status: "active", CreatedAt: m.now().UTC()}
	m.tenants[t.ID] = t
	return cloneTenant(t), nil
}

func (m *Memory) GetTenant(_ context.Context, id string) (*Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneTenant(t), nil
}

func (m *Memory) ListTenants(context.Context) ([]*Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		out = append(out, cloneTenant(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// DeleteTenant removes the tenant and everything scoped to it. Leaving orphaned
// keys behind would leave working credentials for a tenant that no longer
// exists.
func (m *Memory) DeleteTenant(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tenants[id]; !ok {
		return ErrNotFound
	}
	delete(m.tenants, id)
	for keyID, k := range m.keys {
		if k.TenantID == id {
			delete(m.keysByHash, k.Hash)
			delete(m.keys, keyID)
		}
	}
	delete(m.providers, id)
	delete(m.aliases, id)
	delete(m.routes, id)
	delete(m.quotas, id)
	delete(m.webhooks, id)
	delete(m.usage, id)
	return nil
}

// --- api keys --------------------------------------------------------------

func (m *Memory) CreateAPIKey(_ context.Context, tenantID string, plane Plane, name, scope string, expiresAt *time.Time) (*APIKey, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// A root admin key is the one credential with no tenant behind it.
	if tenantID != "" {
		if _, ok := m.tenants[tenantID]; !ok {
			return nil, "", ErrNotFound
		}
	} else if plane != PlaneAdmin || scope != ScopeRoot {
		return nil, "", fmt.Errorf("%w: only root admin keys may omit a tenant", ErrConflict)
	}

	plaintext, prefix, hash := GenerateAPIKey(plane, m.dev)
	k := &APIKey{
		ID:        NewID(IDKey),
		TenantID:  tenantID,
		Plane:     plane,
		Name:      name,
		Prefix:    prefix,
		Hash:      hash,
		Scope:     scope,
		CreatedAt: m.now().UTC(),
		ExpiresAt: expiresAt,
	}
	m.keys[k.ID] = k
	m.keysByHash[hash] = k
	return cloneKey(k), plaintext, nil
}

func (m *Memory) ListAPIKeys(_ context.Context, tenantID string) ([]*APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []*APIKey{}
	for _, k := range m.keys {
		if k.TenantID == tenantID {
			out = append(out, cloneKey(k))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) RevokeAPIKey(_ context.Context, tenantID, keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[keyID]
	if !ok || k.TenantID != tenantID {
		return ErrNotFound
	}
	if k.RevokedAt != nil {
		return nil // revocation is idempotent
	}
	t := m.now().UTC()
	k.RevokedAt = &t
	return nil
}

// --- providers -------------------------------------------------------------

func (m *Memory) CreateProvider(_ context.Context, p *Provider) (*Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tenants[p.TenantID]; !ok {
		return nil, ErrNotFound
	}
	for _, existing := range m.providers[p.TenantID] {
		if strings.EqualFold(existing.Name, p.Name) {
			return nil, fmt.Errorf("%w: provider %q already registered", ErrConflict, p.Name)
		}
	}

	saved := *p
	saved.ID = NewID(IDProvider)
	saved.CreatedAt = m.now().UTC()
	saved.KeyPrefixes = make([]string, 0, len(p.Keys))
	for _, raw := range p.Keys {
		saved.KeyPrefixes = append(saved.KeyPrefixes, maskProviderKey(raw))
	}
	saved.Keys = append([]string(nil), p.Keys...)

	m.providers[p.TenantID] = append(m.providers[p.TenantID], &saved)
	return cloneProvider(&saved), nil
}

func (m *Memory) ListProviders(_ context.Context, tenantID string) ([]*Provider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Provider, 0, len(m.providers[tenantID]))
	for _, p := range m.providers[tenantID] {
		out = append(out, cloneProvider(p))
	}
	return out, nil
}

func (m *Memory) GetProvider(_ context.Context, tenantID, id string) (*Provider, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.providers[tenantID] {
		if p.ID == id || strings.EqualFold(p.Name, id) {
			return cloneProvider(p), nil
		}
	}
	return nil, ErrNotFound
}

func (m *Memory) DeleteProvider(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.providers[tenantID]
	for i, p := range list {
		if p.ID == id || strings.EqualFold(p.Name, id) {
			m.providers[tenantID] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// --- aliases ---------------------------------------------------------------

func (m *Memory) UpsertAlias(_ context.Context, a *Alias) (*Alias, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tenants[a.TenantID]; !ok {
		return nil, ErrNotFound
	}
	for _, existing := range m.aliases[a.TenantID] {
		if existing.Name == a.Name {
			saved := *a
			saved.ID = existing.ID
			saved.CreatedAt = existing.CreatedAt
			*existing = saved
			return cloneAlias(existing), nil
		}
	}
	saved := *a
	saved.ID = NewID(IDAlias)
	saved.CreatedAt = m.now().UTC()
	m.aliases[a.TenantID] = append(m.aliases[a.TenantID], &saved)
	return cloneAlias(&saved), nil
}

func (m *Memory) ListAliases(_ context.Context, tenantID string) ([]*Alias, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Alias, 0, len(m.aliases[tenantID]))
	for _, a := range m.aliases[tenantID] {
		out = append(out, cloneAlias(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Memory) DeleteAlias(_ context.Context, tenantID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.aliases[tenantID]
	for i, a := range list {
		if a.Name == name || a.ID == name {
			m.aliases[tenantID] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// --- routes ----------------------------------------------------------------

func (m *Memory) UpsertRoute(_ context.Context, r *Route) (*Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tenants[r.TenantID]; !ok {
		return nil, ErrNotFound
	}
	for _, existing := range m.routes[r.TenantID] {
		if existing.Match == r.Match {
			saved := *r
			saved.ID = existing.ID
			saved.CreatedAt = existing.CreatedAt
			saved.Chain = append([]string(nil), r.Chain...)
			*existing = saved
			return cloneRoute(existing), nil
		}
	}
	saved := *r
	saved.ID = NewID(IDRoute)
	saved.CreatedAt = m.now().UTC()
	saved.Chain = append([]string(nil), r.Chain...)
	m.routes[r.TenantID] = append(m.routes[r.TenantID], &saved)
	return cloneRoute(&saved), nil
}

func (m *Memory) ListRoutes(_ context.Context, tenantID string) ([]*Route, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Route, 0, len(m.routes[tenantID]))
	for _, r := range m.routes[tenantID] {
		out = append(out, cloneRoute(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Match < out[j].Match })
	return out, nil
}

func (m *Memory) DeleteRoute(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.routes[tenantID]
	for i, r := range list {
		if r.ID == id || r.Match == id {
			m.routes[tenantID] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// --- quotas ----------------------------------------------------------------

// quotaKey addresses one quota. The NUL separator cannot occur in either id, so
// no tenant/key pair can collide with another.
func quotaKey(tenantID, keyID string) string { return tenantID + "\x00" + keyID }

func (m *Memory) SetQuota(_ context.Context, q *Quota) (*Quota, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[q.TenantID]; !ok {
		return nil, ErrNotFound
	}
	saved := *q
	saved.UpdatedAt = m.now().UTC()
	m.quotas[quotaKey(q.TenantID, q.KeyID)] = &saved
	out := saved
	return &out, nil
}

func (m *Memory) GetQuota(_ context.Context, tenantID, keyID string) (*Quota, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	q, ok := m.quotas[quotaKey(tenantID, keyID)]
	if !ok {
		return nil, ErrNotFound
	}
	out := *q
	return &out, nil
}

func (m *Memory) DeleteQuota(_ context.Context, tenantID, keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := quotaKey(tenantID, keyID)
	if _, ok := m.quotas[k]; !ok {
		return ErrNotFound
	}
	delete(m.quotas, k)
	return nil
}

// --- webhooks --------------------------------------------------------------

func (m *Memory) CreateWebhook(_ context.Context, w *Webhook) (*Webhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[w.TenantID]; !ok {
		return nil, ErrNotFound
	}
	saved := *w
	saved.ID = NewID(IDWebhook)
	saved.CreatedAt = m.now().UTC()
	saved.Events = append([]string(nil), w.Events...)
	m.webhooks[w.TenantID] = append(m.webhooks[w.TenantID], &saved)
	return cloneWebhook(&saved), nil
}

func (m *Memory) ListWebhooks(_ context.Context, tenantID string) ([]*Webhook, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Webhook, 0, len(m.webhooks[tenantID]))
	for _, w := range m.webhooks[tenantID] {
		out = append(out, cloneWebhook(w))
	}
	return out, nil
}

func (m *Memory) DeleteWebhook(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.webhooks[tenantID]
	for i, w := range list {
		if w.ID == id {
			m.webhooks[tenantID] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// --- usage -----------------------------------------------------------------

func (m *Memory) RecordUsage(_ context.Context, rec *UsageRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	saved := *rec
	list := append(m.usage[rec.TenantID], &saved)
	if len(list) > maxMemoryUsageRecords {
		list = list[len(list)-maxMemoryUsageRecords:]
	}
	m.usage[rec.TenantID] = list
	return nil
}

func (m *Memory) Usage(_ context.Context, tenantID string, since, until time.Time) (UsageTotals, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out UsageTotals
	for _, r := range m.usage[tenantID] {
		if !inWindow(r.RecordedAt, since, until) {
			continue
		}
		addUsage(&out, r)
	}
	return out, nil
}

func (m *Memory) KeyUsage(_ context.Context, tenantID, keyPrefix string, since, until time.Time) (UsageTotals, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out UsageTotals
	for _, r := range m.usage[tenantID] {
		if r.KeyPrefix != keyPrefix || !inWindow(r.RecordedAt, since, until) {
			continue
		}
		addUsage(&out, r)
	}
	return out, nil
}

func (m *Memory) UsageBreakdown(_ context.Context, tenantID string, since, until time.Time, groupBy string) ([]UsageBucket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	buckets := map[string]*UsageTotals{}
	for _, r := range m.usage[tenantID] {
		if !inWindow(r.RecordedAt, since, until) {
			continue
		}
		var key string
		switch groupBy {
		case "model":
			key = r.Model
		case "provider":
			key = r.Provider
		case "key":
			key = r.KeyPrefix
		default:
			return nil, fmt.Errorf("unsupported group_by %q", groupBy)
		}
		if buckets[key] == nil {
			buckets[key] = &UsageTotals{}
		}
		addUsage(buckets[key], r)
	}

	out := make([]UsageBucket, 0, len(buckets))
	for k, v := range buckets {
		out = append(out, UsageBucket{Key: k, UsageTotals: *v})
	}
	// Descending by spend: the rows an operator opened this endpoint to find
	// are the expensive ones.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func inWindow(t, since, until time.Time) bool {
	return !t.Before(since) && t.Before(until)
}

func addUsage(dst *UsageTotals, r *UsageRecord) {
	dst.Requests++
	dst.PromptTokens += int64(r.PromptTokens)
	dst.CompletionTokens += int64(r.CompletionToken)
	dst.TotalTokens += int64(r.TotalTokens)
	dst.CostUSD += r.CostUSD
}

// --- helpers ---------------------------------------------------------------
//
// Every accessor hands back a copy. Returning the stored pointer would let a
// handler mutate the store without holding the lock, which is the kind of
// data race that only shows up under production concurrency.

func cloneTenant(t *Tenant) *Tenant {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func cloneKey(k *APIKey) *APIKey {
	c := *k
	return &c
}

func cloneProvider(p *Provider) *Provider {
	c := *p
	c.Keys = append([]string(nil), p.Keys...)
	c.KeyPrefixes = append([]string(nil), p.KeyPrefixes...)
	return &c
}

func cloneAlias(a *Alias) *Alias {
	c := *a
	c.Capabilities = append([]string(nil), a.Capabilities...)
	c.ProviderPreference = append([]string(nil), a.ProviderPreference...)
	return &c
}

func cloneRoute(r *Route) *Route {
	c := *r
	c.Chain = append([]string(nil), r.Chain...)
	return &c
}

func cloneWebhook(w *Webhook) *Webhook {
	c := *w
	c.Events = append([]string(nil), w.Events...)
	return &c
}

// maskProviderKey renders a provider credential for display: enough leading and
// trailing characters to recognise which key is configured, never enough to use
// it.
func maskProviderKey(raw string) string {
	const head, tail = 6, 4
	if len(raw) <= head+tail {
		return strings.Repeat("*", len(raw))
	}
	return raw[:head] + "…" + raw[len(raw)-tail:]
}
