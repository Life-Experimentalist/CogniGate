package cache

import (
	"encoding/json"
	"testing"
	"time"
)

func mustKey(t *testing.T, tenant, servedBy, body string) string {
	t.Helper()
	k, err := Key(tenant, servedBy, []byte(body))
	if err != nil {
		t.Fatalf("Key(%q): %v", body, err)
	}
	return k
}

func TestKeyIgnoresFieldOrder(t *testing.T) {
	a := mustKey(t, "t1", "mock/m", `{"model":"m","temperature":0}`)
	b := mustKey(t, "t1", "mock/m", `{"temperature":0,"model":"m"}`)
	if a != b {
		t.Errorf("field order changed the key; the same request must hash the same however the client serialised it")
	}
}

func TestKeyIgnoresVolatileFields(t *testing.T) {
	base := mustKey(t, "t1", "mock/m", `{"model":"m"}`)
	for _, body := range []string{
		`{"model":"m","user":"alice"}`,
		`{"model":"m","metadata":{"trace":"abc"}}`,
		`{"model":"m","stream_options":{"include_usage":true}}`,
	} {
		if got := mustKey(t, "t1", "mock/m", body); got != base {
			t.Errorf("%s changed the key; it cannot change the completion", body)
		}
	}
}

func TestKeySeparatesTenantsAndModels(t *testing.T) {
	body := `{"model":"m","temperature":0}`
	base := mustKey(t, "t1", "mock/m", body)

	if mustKey(t, "t2", "mock/m", body) == base {
		t.Error("two tenants share a key: cross-tenant sharing must be arithmetic, not policy")
	}
	if mustKey(t, "t1", "mock/other", body) == base {
		t.Error("a different resolved model shares a key: an alias repin would serve the old model's answer")
	}
}

// A field that does affect the answer must reach the hash even though the
// gateway does not model it.
func TestKeyHonoursUnmodelledFields(t *testing.T) {
	a := mustKey(t, "t1", "mock/m", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	b := mustKey(t, "t1", "mock/m", `{"model":"m","messages":[{"role":"user","content":"bye"}]}`)
	if a == b {
		t.Error("two different prompts hash the same")
	}
}

func TestKeyPreservesIntegerPrecision(t *testing.T) {
	a := mustKey(t, "t1", "mock/m", `{"model":"m","seed":10000000000000001}`)
	b := mustKey(t, "t1", "mock/m", `{"model":"m","seed":10000000000000000}`)
	if a == b {
		t.Error("two seeds a float64 cannot tell apart hash the same")
	}
}

func TestKeyRejectsUnparseableBody(t *testing.T) {
	if _, err := Key("t1", "mock/m", []byte(`{"model":`)); err == nil {
		t.Error("an unparseable body produced a key")
	}
}

func TestRoundTrip(t *testing.T) {
	c := New(1<<20, 1<<16)
	c.Put("t1", "k", Entry{Body: []byte(`{"id":"x"}`), Provider: "mock", Model: "m"}, time.Minute)

	got, ok := c.Get("k")
	if !ok {
		t.Fatal("the entry just stored was not found")
	}
	if string(got.Body) != `{"id":"x"}` || got.ServedBy() != "mock/m" {
		t.Errorf("stored and returned entries differ: %+v", got)
	}
}

func TestExpiredEntryIsNotServed(t *testing.T) {
	c := New(1<<20, 1<<16)
	now := time.Now()
	c.now = func() time.Time { return now }

	c.Put("t1", "k", Entry{Body: []byte("{}")}, 5*time.Second)
	now = now.Add(5 * time.Second)

	if _, ok := c.Get("k"); ok {
		t.Error("an entry served at exactly its expiry; a TTL that has run out is not a hit")
	}
	if n, _ := c.Stats(); n != 0 {
		t.Errorf("the expired entry still occupies the cache: %d entries", n)
	}
}

func TestOversizeEntryIsDeclined(t *testing.T) {
	c := New(1<<20, 64)
	c.Put("t1", "k", Entry{Body: make([]byte, 128)}, time.Minute)

	if _, ok := c.Get("k"); ok {
		t.Error("an entry over max_entry_bytes was stored")
	}
}

func TestLeastRecentlyUsedIsEvictedFirst(t *testing.T) {
	// Three entries of ~100 bytes into a cache that holds two of them.
	c := New(250, 200)
	body := make([]byte, 100)

	c.Put("t1", "a", Entry{Body: body}, time.Minute)
	c.Put("t1", "b", Entry{Body: body}, time.Minute)
	// Touching "a" makes "b" the least recently used.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a was evicted before the cache was full")
	}
	c.Put("t1", "c", Entry{Body: body}, time.Minute)

	if _, ok := c.Get("b"); ok {
		t.Error("b survived; the least recently used entry must go first")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was evicted despite being used more recently than b")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("the entry that caused the eviction was itself evicted")
	}
}

func TestFlushIsTenantScoped(t *testing.T) {
	c := New(1<<20, 1<<16)
	c.Put("t1", "a", Entry{Body: []byte("{}")}, time.Minute)
	c.Put("t2", "b", Entry{Body: []byte("{}")}, time.Minute)

	if n := c.Flush("t1"); n != 1 {
		t.Errorf("flush reported %d entries, want 1", n)
	}
	if _, ok := c.Get("a"); ok {
		t.Error("the flushed tenant's entry survived")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("another tenant's entry was flushed; a flush is one tenant's to ask for")
	}
}

// Bytes must return to zero, or a long-running cache leaks its own bound and
// eventually evicts everything on every write.
func TestAccountingReturnsToZero(t *testing.T) {
	c := New(1<<20, 1<<16)
	c.Put("t1", "a", Entry{Body: []byte("hello")}, time.Minute)
	c.Put("t1", "a", Entry{Body: []byte("a much longer body than before")}, time.Minute)
	c.Flush("t1")

	if n, b := c.Stats(); n != 0 || b != 0 {
		t.Errorf("after flushing everything the cache holds %d entries and %d bytes", n, b)
	}
}

func TestNilCacheIsUsable(t *testing.T) {
	var c *Cache
	c.Put("t1", "k", Entry{Body: []byte("{}")}, time.Minute)
	if _, ok := c.Get("k"); ok {
		t.Error("a nil cache returned a hit")
	}
	if n := c.Flush("t1"); n != 0 {
		t.Errorf("a nil cache flushed %d entries", n)
	}
	if n, b := c.Stats(); n != 0 || b != 0 {
		t.Errorf("a nil cache reports %d entries and %d bytes", n, b)
	}
}

// The canonical form must stay valid JSON: it is hashed, never served, but a
// malformed intermediate would make the hash depend on Go's error handling.
func TestCanonicalFormIsValidJSON(t *testing.T) {
	out, err := canonicalize([]byte(`{"b":1,"a":{"z":[1,2],"y":null},"user":"x"}`))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("canonical form is not JSON: %v (%s)", err, out)
	}
	if _, ok := doc["user"]; ok {
		t.Error("user survived canonicalization")
	}
	if string(out) != `{"a":{"y":null,"z":[1,2]},"b":1}` {
		t.Errorf("canonical form is not sorted at every level: %s", out)
	}
}
