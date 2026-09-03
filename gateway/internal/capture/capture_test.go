package capture

import (
	"testing"
	"time"
)

func entry(id string, at time.Time, ttl time.Duration, body string) Entry {
	return Entry{
		ID:        id,
		At:        at,
		ExpiresAt: at.Add(ttl),
		Status:    200,
		Request:   []byte(body),
		Response:  []byte(body),
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	s := New(1 << 20)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_1", now, time.Hour, "one"))
	s.Put("ten_a", entry("cap_2", now.Add(time.Second), time.Hour, "two"))

	got := s.List("ten_a", now.Add(2*time.Second))
	if len(got) != 2 {
		t.Fatalf("got %d captures, want 2", len(got))
	}
	if got[0].ID != "cap_2" || got[1].ID != "cap_1" {
		t.Errorf("order is %s,%s; want newest first (cap_2,cap_1)", got[0].ID, got[1].ID)
	}
}

// Tenant scoping is the property GW-14 leans on hardest: a capture is the one
// place content is readable, and reading someone else's would make the rest of
// the content ban beside the point.
func TestCapturesAreTenantScoped(t *testing.T) {
	s := New(1 << 20)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_a", now, time.Hour, "tenant a prompt"))

	if got := s.List("ten_b", now); len(got) != 0 {
		t.Fatalf("tenant b sees %d of tenant a's captures, want 0", len(got))
	}
}

// The TTL is a deletion, not a display rule: an expired capture is absent from
// a read that happens before any sweep, and the sweep frees it for real.
func TestExpiredCapturesAreUnreadableThenSwept(t *testing.T) {
	s := New(1 << 20)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_1", now, time.Minute, "prompt"))
	after := now.Add(2 * time.Minute)

	if got := s.List("ten_a", after); len(got) != 0 {
		t.Fatalf("expired capture still listed: %d entries", len(got))
	}
	if removed := s.Sweep(after); removed != 1 {
		t.Fatalf("Sweep removed %d, want 1", removed)
	}
	if removed := s.Sweep(after); removed != 0 {
		t.Errorf("second Sweep removed %d, want 0", removed)
	}
}

func TestSweepLeavesLiveCaptures(t *testing.T) {
	s := New(1 << 20)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_old", now, time.Minute, "old"))
	s.Put("ten_a", entry("cap_new", now, time.Hour, "new"))

	if removed := s.Sweep(now.Add(2 * time.Minute)); removed != 1 {
		t.Fatalf("Sweep removed %d, want 1", removed)
	}
	got := s.List("ten_a", now.Add(2*time.Minute))
	if len(got) != 1 || got[0].ID != "cap_new" {
		t.Fatalf("survivors = %+v, want only cap_new", got)
	}
}

// The budget is per tenant, so one tenant filling it must not cost another
// anything.
func TestBudgetEvictsOldestWithinOneTenantOnly(t *testing.T) {
	// Room for two entries of this size and not three.
	const body = "0123456789"
	size := entry("x", time.Now(), time.Hour, body).size()
	s := New(2 * size)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_1", now, time.Hour, body))
	s.Put("ten_a", entry("cap_2", now, time.Hour, body))
	s.Put("ten_b", entry("cap_b", now, time.Hour, body))
	s.Put("ten_a", entry("cap_3", now, time.Hour, body))

	got := s.List("ten_a", now)
	if len(got) != 2 {
		t.Fatalf("tenant a holds %d captures, want 2", len(got))
	}
	if got[0].ID != "cap_3" || got[1].ID != "cap_2" {
		t.Errorf("kept %s,%s; want the two newest (cap_3,cap_2)", got[0].ID, got[1].ID)
	}
	if b := s.List("ten_b", now); len(b) != 1 {
		t.Errorf("tenant b holds %d captures, want 1 — its budget is its own", len(b))
	}
}

// An entry too large for the whole budget is refused rather than allowed to
// empty the bucket for itself.
func TestOversizedCaptureIsRefusedAndKeepsWhatIsThere(t *testing.T) {
	s := New(512)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_small", now, time.Hour, "small"))
	if s.Put("ten_a", entry("cap_huge", now, time.Hour, string(make([]byte, 4096)))) {
		t.Fatal("an entry larger than the budget was accepted")
	}
	got := s.List("ten_a", now)
	if len(got) != 1 || got[0].ID != "cap_small" {
		t.Fatalf("survivors = %+v, want only cap_small", got)
	}
}

func TestFlushDropsOneTenant(t *testing.T) {
	s := New(1 << 20)
	now := time.Now().UTC()

	s.Put("ten_a", entry("cap_a", now, time.Hour, "a"))
	s.Put("ten_b", entry("cap_b", now, time.Hour, "b"))

	if n := s.Flush("ten_a"); n != 1 {
		t.Fatalf("Flush dropped %d, want 1", n)
	}
	if got := s.List("ten_a", now); len(got) != 0 {
		t.Errorf("tenant a still holds %d captures after a flush", len(got))
	}
	if got := s.List("ten_b", now); len(got) != 1 {
		t.Errorf("tenant b lost captures to tenant a's flush")
	}
}

// A nil store is the shape a caller gets when nothing is configured, and it has
// to behave rather than panic — the middleware calls into it on every request.
func TestNilStoreKeepsNothingAndDoesNotPanic(t *testing.T) {
	var s *Store
	if s.Put("ten_a", entry("cap_1", time.Now(), time.Hour, "x")) {
		t.Error("a nil store reported that it kept an entry")
	}
	if got := s.List("ten_a", time.Now()); got != nil {
		t.Errorf("a nil store listed %v", got)
	}
	if s.Sweep(time.Now()) != 0 || s.Flush("ten_a") != 0 {
		t.Error("a nil store reported work it could not have done")
	}
}

// Content must not be reachable through a tenant id nobody holds. Put with an
// empty tenant is the accident that would create exactly that.
func TestPutRefusesAnEmptyTenant(t *testing.T) {
	s := New(1 << 20)
	if s.Put("", entry("cap_1", time.Now(), time.Hour, "orphan")) {
		t.Fatal("a capture was stored under no tenant")
	}
}
