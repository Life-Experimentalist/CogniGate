// Package cache holds completed responses for deterministic requests (GW-12).
//
// It lives in the gateway process rather than in Redis. That is a deliberate
// departure from the spec's original sketch, and the reason is coherence: the
// gateway's tenants, keys, routes and quotas are already in-process, so a cache
// that outlived a restart would be keyed by tenant ids that no longer exist.
// An in-process cache dies with the tenants it belongs to.
//
// The trade is that a multi-replica deployment caches per replica: the hit rate
// is lower and a flush must reach every replica. Both are stated in the docs
// rather than hidden, because a cache that is wrong is worse than no cache, and
// GW-12 is optional precisely so a deployment can decline this trade.
package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// Entry is a stored response.
type Entry struct {
	// Body is the upstream's bytes, replayed verbatim on a hit. GW-12 requires
	// byte-identity, including the original completion id and usage block.
	Body []byte
	// Provider and Model are who produced Body. They are kept apart rather than
	// as one "<provider>/<model>" string because a hit has to write a usage row
	// as well as a header, and the row wants them in separate columns.
	Provider string
	Model    string
}

// ServedBy is the X-CogniGate-Served-By value for this entry, in the same shape
// routing.Candidate publishes.
func (e Entry) ServedBy() string { return e.Provider + "/" + e.Model }

// item is Entry plus the bookkeeping a bounded cache needs.
type item struct {
	key     string
	tenant  string
	entry   Entry
	expires time.Time
	size    int64
}

// Cache is a byte-bounded LRU with per-entry TTLs and tenant-scoped flush.
//
// Every method is safe on a nil receiver, so a deployment with caching disabled
// can leave the field unset instead of guarding each call site.
type Cache struct {
	mu       sync.Mutex
	items    map[string]*list.Element
	order    *list.List                 // front is most recently used
	byTenant map[string]map[string]bool // tenant -> its keys, for flush
	bytes    int64

	maxBytes      int64
	maxEntryBytes int64

	now func() time.Time
}

// New builds an empty cache. maxEntryBytes decides what may be stored, maxBytes
// how much of it is kept.
func New(maxBytes, maxEntryBytes int64) *Cache {
	return &Cache{
		items:         make(map[string]*list.Element),
		order:         list.New(),
		byTenant:      make(map[string]map[string]bool),
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		now:           time.Now,
	}
}

// Get returns the entry for key when it is present and unexpired, and promotes
// it. An expired entry is dropped rather than returned: a stale hit is the one
// failure mode a caller cannot detect for itself.
func (c *Cache) Get(key string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	it := el.Value.(*item)
	if !c.now().Before(it.expires) {
		c.removeElement(el)
		return Entry{}, false
	}
	c.order.MoveToFront(el)
	return it.entry, true
}

// Put stores e under key for ttl, evicting least-recently-used entries until
// the cache is back within its bound.
//
// An entry larger than maxEntryBytes is silently declined. That is not an
// error the caller can act on — the response was served either way — and GW-12
// makes the size limit a storage policy, not a request outcome.
func (c *Cache) Put(tenant, key string, e Entry, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	size := int64(len(e.Body) + len(e.Provider) + len(e.Model) + len(key))
	if size > c.maxEntryBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}

	it := &item{
		key:     key,
		tenant:  tenant,
		entry:   e,
		expires: c.now().Add(ttl),
		size:    size,
	}
	c.items[key] = c.order.PushFront(it)
	if c.byTenant[tenant] == nil {
		c.byTenant[tenant] = make(map[string]bool)
	}
	c.byTenant[tenant][key] = true
	c.bytes += size

	for c.bytes > c.maxBytes {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.removeElement(back)
	}
}

// Flush drops every entry belonging to one tenant. GW-12.AC-6 requires the next
// identical request to miss within ten seconds; doing it synchronously here
// makes that immediate for this replica.
func (c *Cache) Flush(tenant string) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := c.byTenant[tenant]
	n := 0
	for key := range keys {
		if el, ok := c.items[key]; ok {
			c.removeElement(el)
			n++
		}
	}
	return n
}

// removeElement drops one element from all three indexes. The caller holds mu.
func (c *Cache) removeElement(el *list.Element) {
	it := el.Value.(*item)
	c.order.Remove(el)
	delete(c.items, it.key)
	if keys := c.byTenant[it.tenant]; keys != nil {
		delete(keys, it.key)
		if len(keys) == 0 {
			delete(c.byTenant, it.tenant)
		}
	}
	c.bytes -= it.size
}

// Stats reports what the cache is holding, for /v1/health and for tests.
func (c *Cache) Stats() (entries int, bytes int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.bytes
}

// volatileFields are dropped before hashing: they vary between otherwise
// identical requests and do not change the completion. Everything else counts,
// including fields the gateway does not itself model — a provider extension
// that alters the answer must alter the key.
var volatileFields = []string{"user", "metadata", "stream_options"}

// Key derives the cache key from the tenant, the resolved "<provider>/<model>",
// and the request body.
//
// The tenant is inside the hash rather than beside it so that cross-tenant
// sharing is not merely policy but arithmetic: two tenants issuing byte-
// identical requests produce different keys.
//
// An unparseable body yields an error, which the caller treats as "not
// cacheable" rather than as a request failure — the dispatcher will reject it
// with a 400 a moment later, and that is the better place to say so.
func Key(tenantID, servedBy string, body []byte) (string, error) {
	canonical, err := canonicalize(body)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(tenantID))
	h.Write([]byte{0})
	h.Write([]byte(servedBy))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalize re-encodes the body with its keys sorted and the volatile fields
// removed, so that two requests differing only in key order or in a field that
// cannot affect the answer hash the same.
//
// Numbers are decoded as json.Number so a literal survives the round trip;
// decoding them as float64 would make 10000000000000001 and 10000000000000000
// the same request.
func canonicalize(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	for _, f := range volatileFields {
		delete(doc, f)
	}
	// encoding/json sorts map keys, and does so at every level, which is the
	// whole of the canonical form this needs.
	return json.Marshal(doc)
}
