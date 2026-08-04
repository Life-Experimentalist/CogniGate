package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/routing"
)

// captureEmitter records what a hook published. It is mutex-guarded because the
// hooks emit on their own goroutines — the test reads from a different one than
// the write happens on.
type captureEmitter struct {
	mu   sync.Mutex
	got  []captured
	hold chan struct{} // when non-nil, Emit blocks on it
}

type captured struct {
	tenant string
	typ    string
	data   map[string]any
}

func (c *captureEmitter) Emit(_ context.Context, tenantID, eventType string, data map[string]any) {
	if c.hold != nil {
		<-c.hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, captured{tenant: tenantID, typ: eventType, data: data})
}

func (c *captureEmitter) snapshot() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.got...)
}

func (c *captureEmitter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// tripBreaker drives one key past the error threshold.
func tripBreaker(b *routing.Breaker, key string, times int) {
	for i := 0; i < times; i++ {
		b.Allow(key)
		b.Failure(key)
	}
}

func TestBreakerHookPublishesTheOpenTransition(t *testing.T) {
	em := &captureEmitter{}
	b := routing.NewBreaker(2, time.Second, time.Minute, BreakerHook(em))

	tripBreaker(b, routing.Key("ten_a", "primary", "gpt-4o"), 2)

	if !waitFor(t, func() bool { return em.count() == 1 }) {
		t.Fatalf("expected exactly one event, got %d", em.count())
	}
	ev := em.snapshot()[0]

	if ev.typ != BreakerOpened {
		t.Errorf("event type = %q, want %q", ev.typ, BreakerOpened)
	}
	// The tenant is what makes the event deliverable at all: the dispatcher
	// looks up webhooks by it, so an event attributed to the wrong tenant is
	// delivered to the wrong customer.
	if ev.tenant != "ten_a" {
		t.Errorf("tenant = %q, want %q", ev.tenant, "ten_a")
	}
	if got := ev.data["provider"]; got != "primary" {
		t.Errorf("data.provider = %v, want %q", got, "primary")
	}
	if got := ev.data["model"]; got != "gpt-4o" {
		t.Errorf("data.model = %v, want %q", got, "gpt-4o")
	}
	if got := ev.data["state"]; got != "open" {
		t.Errorf("data.state = %v, want %q", got, "open")
	}
	if got := ev.data["previous_state"]; got != "closed" {
		t.Errorf("data.previous_state = %v, want %q", got, "closed")
	}
}

// A model id can itself contain a slash — the qualified "provider/model" form —
// so the key has three meaningful segments but more than two separators. Getting
// this wrong silently truncates the model name in every breaker event.
func TestBreakerHookHandlesAQualifiedModelID(t *testing.T) {
	em := &captureEmitter{}
	b := routing.NewBreaker(2, time.Second, time.Minute, BreakerHook(em))

	tripBreaker(b, routing.Key("ten_a", "primary", "openai/gpt-4o"), 2)

	if !waitFor(t, func() bool { return em.count() == 1 }) {
		t.Fatalf("expected one event, got %d", em.count())
	}
	if got := em.snapshot()[0].data["model"]; got != "openai/gpt-4o" {
		t.Errorf("data.model = %v, want %q", got, "openai/gpt-4o")
	}
}

func TestBreakerHookPublishesTheCloseTransition(t *testing.T) {
	em := &captureEmitter{}
	// A short open duration so the cool-off can elapse inside a test; the
	// breaker's clock is not injectable from outside its package.
	b := routing.NewBreaker(2, time.Second, 20*time.Millisecond, BreakerHook(em))
	key := routing.Key("ten_a", "primary", "gpt-4o")

	tripBreaker(b, key, 2)
	if !waitFor(t, func() bool { return em.count() == 1 }) {
		t.Fatalf("breaker did not open")
	}

	// Wait out the cool-off, take the half-open probe, and succeed on it.
	time.Sleep(40 * time.Millisecond)
	if !b.Allow(key) {
		t.Fatal("the cool-off elapsed but the breaker did not admit a probe")
	}
	b.Success(key)

	if !waitFor(t, func() bool { return em.count() == 2 }) {
		t.Fatalf("expected a close event, got %d events", em.count())
	}
	ev := em.snapshot()[1]
	if ev.typ != BreakerClosed {
		t.Errorf("event type = %q, want %q", ev.typ, BreakerClosed)
	}
	if ev.tenant != "ten_a" {
		t.Errorf("tenant = %q, want %q", ev.tenant, "ten_a")
	}
}

// Half-open is re-entered on every cool-off that elapses while a provider is
// still down. There is no event type for it, and inventing deliveries for it
// would emit one webhook per cool-off for the whole duration of an outage.
func TestBreakerHookIsSilentOnTheHalfOpenTransition(t *testing.T) {
	em := &captureEmitter{}
	b := routing.NewBreaker(2, time.Second, 20*time.Millisecond, BreakerHook(em))
	key := routing.Key("ten_a", "primary", "gpt-4o")

	tripBreaker(b, key, 2)
	if !waitFor(t, func() bool { return em.count() == 1 }) {
		t.Fatalf("breaker did not open")
	}

	// Two cool-offs elapse and the provider is still failing, so the breaker
	// goes half-open, fails its probe, opens, and half-opens again.
	for i := 0; i < 2; i++ {
		time.Sleep(40 * time.Millisecond)
		b.Allow(key)
		b.Failure(key)
	}

	// Give any spurious emit a chance to land before concluding there was none.
	time.Sleep(50 * time.Millisecond)
	for _, ev := range em.snapshot() {
		if ev.typ != BreakerOpened {
			t.Errorf("published %q; only open and close transitions are events", ev.typ)
		}
		if got := ev.data["state"]; got == "half_open" {
			t.Error("published a half-open transition as an event")
		}
	}
}

func TestBreakerHookDropsAnUnattributableKey(t *testing.T) {
	em := &captureEmitter{}
	b := routing.NewBreaker(2, time.Second, time.Minute, BreakerHook(em))

	// A key that predates tenant scoping, or was built by hand.
	tripBreaker(b, "primary/gpt-4o", 2)

	time.Sleep(50 * time.Millisecond)
	if n := em.count(); n != 0 {
		t.Errorf("emitted %d events for a key with no tenant; want 0", n)
	}
}

// The breaker calls onChange with its lock held, and Emit reads the tenant's
// webhooks from the store — an HTTP call once the store is the analytics
// service. If the hook did not hand that off, one slow store would freeze every
// request routing through any provider.
func TestBreakerHookDoesNotBlockTheBreaker(t *testing.T) {
	em := &captureEmitter{hold: make(chan struct{})}
	b := routing.NewBreaker(2, time.Second, time.Minute, BreakerHook(em))
	key := routing.Key("ten_a", "primary", "gpt-4o")

	done := make(chan struct{})
	go func() {
		defer close(done)
		tripBreaker(b, key, 2)
		// The breaker must still be usable while the emit is stuck.
		b.State(key)
		b.Snapshot()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the breaker stalled while an emit was in flight; the hook is emitting under the lock")
	}
	close(em.hold)
}

func TestCatalogHookPublishesOneEventPerModel(t *testing.T) {
	em := &captureEmitter{}
	hook := CatalogHook(em)

	hook("ten_a", []string{"gpt-4o", "gpt-4o-mini"}, []string{"gpt-3.5-turbo"})

	if !waitFor(t, func() bool { return em.count() == 3 }) {
		t.Fatalf("expected 3 events, got %d", em.count())
	}

	added, removed := map[string]bool{}, map[string]bool{}
	for _, ev := range em.snapshot() {
		if ev.tenant != "ten_a" {
			t.Errorf("tenant = %q, want %q", ev.tenant, "ten_a")
		}
		model, _ := ev.data["model"].(string)
		switch ev.typ {
		case CatalogModelAdded:
			added[model] = true
		case CatalogModelRemoved:
			removed[model] = true
		default:
			t.Errorf("unexpected event type %q", ev.typ)
		}
	}

	for _, m := range []string{"gpt-4o", "gpt-4o-mini"} {
		if !added[m] {
			t.Errorf("no %s event for %q", CatalogModelAdded, m)
		}
	}
	if !removed["gpt-3.5-turbo"] {
		t.Errorf("no %s event for %q", CatalogModelRemoved, "gpt-3.5-turbo")
	}
}

// The hook emits on a goroutine, so it must not read the caller's slices after
// returning: the catalog is free to reuse them the moment OnChange comes back.
func TestCatalogHookCopiesTheCallersSlices(t *testing.T) {
	em := &captureEmitter{}
	hook := CatalogHook(em)

	added := []string{"gpt-4o"}
	hook("ten_a", added, nil)
	added[0] = "overwritten"

	if !waitFor(t, func() bool { return em.count() == 1 }) {
		t.Fatalf("expected one event, got %d", em.count())
	}
	if got := em.snapshot()[0].data["model"]; got != "gpt-4o" {
		t.Errorf("data.model = %v, want %q; the hook read the caller's slice after returning", got, "gpt-4o")
	}
}

func TestCatalogHookIgnoresAnEmptyTenant(t *testing.T) {
	em := &captureEmitter{}
	CatalogHook(em)("", []string{"gpt-4o"}, nil)

	time.Sleep(50 * time.Millisecond)
	if n := em.count(); n != 0 {
		t.Errorf("emitted %d events for an empty tenant; want 0", n)
	}
}

// A nil emitter is how --dev turns webhooks off. The hooks must return nil
// rather than a callback that panics on the first transition.
func TestHooksAreNilWithoutAnEmitter(t *testing.T) {
	if BreakerHook(nil) != nil {
		t.Error("BreakerHook(nil) should be nil so the breaker skips the callback entirely")
	}
	if CatalogHook(nil) != nil {
		t.Error("CatalogHook(nil) should be nil so the catalog skips the callback entirely")
	}
}
