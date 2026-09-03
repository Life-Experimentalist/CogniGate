// Package capture holds GW-14's debug captures: the request and response
// bodies of a sampled fraction of one tenant's traffic, kept for as long as
// that tenant's policy says and no longer.
//
// It lives in the gateway process and nowhere else. GW-14 exists to say that
// prompt content reaches no store the operator did not ask for, and the
// cheapest way to keep that promise for the one feature that deliberately
// retains content is to give the content nowhere to go: no database, no file,
// no telemetry hop, no serialisation except the single admin endpoint the
// feature is for. That is the same discipline the gateway already applies to
// provider credentials, which are likewise held in memory and never written
// down; the difference is that captures have exactly one outlet, because a
// capture nobody can read would not be a debugging feature.
//
// The consequences are stated rather than hidden. Captures do not survive a
// restart, and in a multi-replica deployment a capture is readable only from
// the replica that took it — the same trade GW-12's cache makes, and acceptable
// for the same reason: debug capture is a short, deliberate, per-tenant window,
// not an archive.
package capture

import (
	"sync"
	"time"
)

// Entry is one recorded exchange.
//
// Request and Response are the bytes as they crossed the wire, which is the
// only form worth keeping: a capture that had been re-serialised would answer
// "what did the gateway think you sent" rather than "what did you send", and
// the second question is the one that gets asked when a request is behaving
// oddly.
type Entry struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id,omitempty"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expires_at"`
	Model     string    `json:"model,omitempty"`
	Status    int       `json:"status"`
	Request   []byte    `json:"request"`
	Response  []byte    `json:"response"`
}

// size is what this entry costs against a tenant's budget. The constant covers
// the identifiers and timestamps, which are small but not free, and stops a
// flood of tiny captures from being accounted as costing nothing.
func (e Entry) size() int64 { return int64(len(e.Request)+len(e.Response)) + 256 }

// Store holds captures per tenant, under a byte budget per tenant.
//
// The budget is per tenant rather than global on purpose. A global budget makes
// one tenant's capture volume decide how much of another tenant's is kept,
// which is the sort of cross-tenant coupling GW-14 spends the rest of its time
// forbidding. Per tenant, the worst case is bounded by the budget times the
// number of tenants an operator has explicitly enabled capture for — and
// enabling it is an audited admin action, so that number is one the operator
// chose.
//
// A nil *Store is a working store that keeps nothing, so no call site has to
// ask whether capture is configured.
type Store struct {
	maxBytesPerTenant int64

	mu       sync.Mutex
	byTenant map[string]*bucket
}

type bucket struct {
	bytes   int64
	entries []Entry // oldest first
}

// New builds a store bounding each tenant to maxBytesPerTenant.
func New(maxBytesPerTenant int64) *Store {
	return &Store{
		maxBytesPerTenant: maxBytesPerTenant,
		byTenant:          make(map[string]*bucket),
	}
}

// Put records one exchange, evicting this tenant's oldest captures if the new
// one does not otherwise fit. It reports whether the entry was kept.
//
// An entry larger than the whole budget is refused rather than allowed to empty
// the bucket for itself: the operator asked for a window into recent traffic,
// and one enormous request that discarded everything around it would be the
// least useful thing to keep.
func (s *Store) Put(tenantID string, e Entry) bool {
	if s == nil || tenantID == "" {
		return false
	}
	size := e.size()
	if size > s.maxBytesPerTenant {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.byTenant[tenantID]
	if b == nil {
		b = &bucket{}
		s.byTenant[tenantID] = b
	}
	// Expired entries are dropped before eviction is considered, so a tenant
	// never loses a live capture to make room for one that only appeared to be
	// needed because nothing had swept yet.
	b.expire(e.At)
	for b.bytes+size > s.maxBytesPerTenant && len(b.entries) > 0 {
		b.bytes -= b.entries[0].size()
		b.entries[0] = Entry{}
		b.entries = b.entries[1:]
	}
	b.entries = append(b.entries, e)
	b.bytes += size
	return true
}

// List returns a tenant's live captures, newest first, without the expired
// ones — a capture past its TTL is gone from the moment it expires, whether or
// not the sweeper has reached it yet.
func (s *Store) List(tenantID string, now time.Time) []Entry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.byTenant[tenantID]
	if b == nil {
		return nil
	}
	out := make([]Entry, 0, len(b.entries))
	for i := len(b.entries) - 1; i >= 0; i-- {
		if !now.Before(b.entries[i].ExpiresAt) {
			continue
		}
		out = append(out, b.entries[i])
	}
	return out
}

// Sweep hard-deletes every expired capture and reports how many went.
//
// Filtering on read is not enough to satisfy GW-14's hard-delete requirement:
// an operator who enables capture, sends traffic and turns it off again would
// otherwise leave that content in memory until the process ended, because
// nothing would ever read it. The sweeper is what makes the TTL a deletion
// rather than a display rule.
func (s *Store) Sweep(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for tenantID, b := range s.byTenant {
		removed += b.expire(now)
		if len(b.entries) == 0 {
			delete(s.byTenant, tenantID)
		}
	}
	return removed
}

// Flush drops everything held for one tenant and reports how many entries went.
// It is what a deleted tenant's captures go through: content that outlived the
// tenant it belonged to would be retention nobody had asked for and nobody
// could any longer read.
func (s *Store) Flush(tenantID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.byTenant[tenantID]
	if b == nil {
		return 0
	}
	n := len(b.entries)
	delete(s.byTenant, tenantID)
	return n
}

// expire drops this bucket's expired entries. The caller holds the lock.
//
// Entries are appended in time order and every TTL comes from the same
// per-tenant policy, so expiry order matches insertion order and the scan can
// stop at the first live entry. A policy shortened mid-flight is the exception:
// then a later entry can expire before an earlier one, and it simply waits for
// the earlier one to go. It is bounded by the TTL either way, which is the
// promise that matters.
func (b *bucket) expire(now time.Time) int {
	cut := 0
	for cut < len(b.entries) && !now.Before(b.entries[cut].ExpiresAt) {
		b.bytes -= b.entries[cut].size()
		b.entries[cut] = Entry{}
		cut++
	}
	if cut > 0 {
		b.entries = b.entries[cut:]
	}
	return cut
}
