package routing

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cognigate/gateway/internal/store"
)

func newTestDispatcher() *Dispatcher {
	return &Dispatcher{cursors: make(map[string]uint64)}
}

func pool(strategy string, keys ...string) *store.Provider {
	return &store.Provider{ID: "prov_test", KeyStrategy: strategy, Keys: keys}
}

// The property GW-3.AC-3 depends on: whichever key a request starts at, every
// key in the pool is still offered before the caller gives up on the provider
// and cascades. A strategy that reordered the pool but dropped or repeated a
// key would let a 429 cascade with a working credential left untried.
func TestKeyOrderAlwaysCoversTheWholePool(t *testing.T) {
	d := newTestDispatcher()
	p := pool(store.KeyStrategyRoundRobin, "a", "b", "c", "d")

	for i := 0; i < 9; i++ {
		got := d.keyOrder(p)
		if len(got) != len(p.Keys) {
			t.Fatalf("request %d: got %d keys, want %d: %v", i, len(got), len(p.Keys), got)
		}
		seen := map[string]bool{}
		for _, k := range got {
			if seen[k] {
				t.Fatalf("request %d: key %q offered twice: %v", i, k, got)
			}
			seen[k] = true
		}
		if len(seen) != len(p.Keys) {
			t.Fatalf("request %d: pool not fully covered: %v", i, got)
		}
	}
}

// Round robin means the starting key advances by one per request and wraps.
// This is the whole of what the operator asked for when they pooled four keys:
// a steady load lands on each of them in turn rather than piling onto the first
// until it is throttled.
func TestRoundRobinAdvancesAndWraps(t *testing.T) {
	d := newTestDispatcher()
	p := pool(store.KeyStrategyRoundRobin, "a", "b", "c")

	want := [][]string{
		{"a", "b", "c"},
		{"b", "c", "a"},
		{"c", "a", "b"},
		{"a", "b", "c"}, // wrapped
	}
	for i, w := range want {
		got := d.keyOrder(p)
		if fmt.Sprint(got) != fmt.Sprint(w) {
			t.Errorf("request %d: got %v, want %v", i, got, w)
		}
	}
}

// Failover is the old behaviour and stays reachable: the first key serves every
// request, and the rest exist only for when it starts refusing. Someone whose
// pool is a paid key backed by a free one is relying on this.
func TestFailoverAlwaysStartsAtTheFirstKey(t *testing.T) {
	d := newTestDispatcher()
	p := pool(store.KeyStrategyFailover, "a", "b", "c")

	for i := 0; i < 5; i++ {
		got := d.keyOrder(p)
		if got[0] != "a" {
			t.Fatalf("request %d: started at %q, want the pool's first key", i, got[0])
		}
	}
}

// An unset strategy is round robin. Providers registered before the field
// existed carry an empty string, and they should spread load rather than sit on
// key one. The check that matters here is that the empty string is treated
// as a strategy at all, not as an unknown value to fall over on.
func TestEmptyStrategyRotates(t *testing.T) {
	d := newTestDispatcher()
	p := pool("", "a", "b")

	if got := d.keyOrder(p); got[0] != "a" {
		t.Fatalf("first request started at %q, want a", got[0])
	}
	if got := d.keyOrder(p); got[0] != "b" {
		t.Fatalf("second request started at %q, want b, an unset strategy should rotate", got[0])
	}
}

// A single key has no rotation to do, and a provider is not required to have
// more than one. The cursor must not be advanced for these, or a pool that
// later grows would start from an arbitrary offset.
func TestSingleKeyPoolIsReturnedUntouched(t *testing.T) {
	d := newTestDispatcher()
	p := pool(store.KeyStrategyRoundRobin, "only")

	for i := 0; i < 3; i++ {
		got := d.keyOrder(p)
		if len(got) != 1 || got[0] != "only" {
			t.Fatalf("request %d: got %v, want [only]", i, got)
		}
	}
	if _, advanced := d.cursors["prov_test"]; advanced {
		t.Error("a single-key pool advanced the cursor")
	}
}

// Two tenants' providers rotate independently, because the cursor is keyed by
// provider id. Sharing one counter would make each provider's rotation depend
// on how much traffic the others were taking.
func TestCursorsArePerProvider(t *testing.T) {
	d := newTestDispatcher()
	first := &store.Provider{ID: "prov_a", KeyStrategy: store.KeyStrategyRoundRobin, Keys: []string{"a1", "a2"}}
	second := &store.Provider{ID: "prov_b", KeyStrategy: store.KeyStrategyRoundRobin, Keys: []string{"b1", "b2"}}

	d.keyOrder(first)
	d.keyOrder(first)
	d.keyOrder(first) // prov_a has advanced three times

	if got := d.keyOrder(second); got[0] != "b1" {
		t.Errorf("second provider started at %q, want b1, cursors are shared", got[0])
	}
}

// The cursor is read and incremented under a lock because concurrent requests
// against one provider are the normal case, not an edge one. Run with -race to
// make this test mean what it says; without it, it still asserts that every
// concurrent caller got a complete pool.
func TestKeyOrderIsSafeUnderConcurrency(t *testing.T) {
	d := newTestDispatcher()
	p := pool(store.KeyStrategyRoundRobin, "a", "b", "c", "d")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if got := d.keyOrder(p); len(got) != 4 {
					t.Errorf("got %d keys, want 4", len(got))
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := d.cursors["prov_test"]; got != 1000 {
		t.Errorf("cursor is %d after 1000 requests, want 1000, an increment was lost", got)
	}
}
