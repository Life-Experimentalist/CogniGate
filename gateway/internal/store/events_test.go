package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The event history is the floor under GW-8's at-least-once delivery: a tenant
// with no webhook registered, or one whose endpoint refused all five attempts,
// reads what happened here instead. That makes three properties load-bearing —
// the order it comes back in, the depth it keeps, and the fact that a stored
// event is a copy — and none of them is visible from the HTTP surface the
// conformance suite drives.

func newEvent(tenantID, id string, at time.Time) *Event {
	return &Event{
		ID:       id,
		Type:     "breaker.opened",
		Created:  at,
		TenantID: tenantID,
		Data:     map[string]any{"provider": "mock"},
	}
}

func TestListEventsReturnsNewestFirst(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if err := m.RecordEvent(ctx, newEvent("ten_a", fmt.Sprintf("ev_%d", i), base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}

	got, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// Newest first, because a poller that has fallen behind wants the recent
	// history and reads until it recognises something, not from the beginning
	// of a thousand-entry list.
	want := []string{"ev_2", "ev_1", "ev_0"}
	if len(got) != len(want) {
		t.Fatalf("ListEvents returned %d events, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("event %d is %s, want %s", i, got[i].ID, id)
		}
	}
}

func TestListEventsIsScopedToOneTenant(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := m.RecordEvent(ctx, newEvent("ten_a", "ev_a", now)); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := m.RecordEvent(ctx, newEvent("ten_b", "ev_b", now)); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	got, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ev_a" {
		t.Fatalf("ten_a sees %d events %v, want exactly ev_a", len(got), ids(got))
	}

	// An unknown tenant is empty rather than an error: the caller that asks is
	// the admin handler, which has already established the tenant exists.
	none, err := m.ListEvents(ctx, "ten_missing")
	if err != nil {
		t.Fatalf("ListEvents for an unknown tenant: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("an unknown tenant has %d events, want none", len(none))
	}
}

func TestRecordEventKeepsTheNewestThousand(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const extra = 50
	for i := 0; i < MaxTenantEvents+extra; i++ {
		if err := m.RecordEvent(ctx, newEvent("ten_a", fmt.Sprintf("ev_%04d", i), base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}

	got, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != MaxTenantEvents {
		t.Fatalf("the history holds %d events, want the documented bound of %d", len(got), MaxTenantEvents)
	}
	// The trim has to drop from the old end. Dropping from the new one would
	// leave a tenant reading a history that stopped when it got busy, which is
	// exactly when it needs to be read.
	newest := fmt.Sprintf("ev_%04d", MaxTenantEvents+extra-1)
	if got[0].ID != newest {
		t.Errorf("the first event is %s, want the newest %s", got[0].ID, newest)
	}
	oldestKept := fmt.Sprintf("ev_%04d", extra)
	if got[len(got)-1].ID != oldestKept {
		t.Errorf("the last event is %s, want %s — the trim dropped the wrong end", got[len(got)-1].ID, oldestKept)
	}
}

func TestRecordEventCopiesThePayload(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()

	data := map[string]any{"provider": "mock"}
	e := &Event{ID: "ev_1", Type: "breaker.opened", Created: time.Now().UTC(), TenantID: "ten_a", Data: data}
	if err := m.RecordEvent(ctx, e); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	// The caller keeps its map. A store that aliased it would let the emitter
	// rewrite history after the fact.
	data["provider"] = "rewritten"
	data["injected"] = true

	got, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEvents returned %d events, want 1", len(got))
	}
	if got[0].Data["provider"] != "mock" {
		t.Errorf("the stored payload followed the caller's map: provider is %v, want mock", got[0].Data["provider"])
	}
	if _, ok := got[0].Data["injected"]; ok {
		t.Error("a key added to the caller's map after the write reached the stored event")
	}
}

func TestListEventsCopiesThePayload(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()

	if err := m.RecordEvent(ctx, newEvent("ten_a", "ev_1", time.Now().UTC())); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	first, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	// The other direction: a handler that serialises the result is free to
	// touch it, and two readers must not see each other's edits.
	first[0].Data["provider"] = "rewritten"
	first[0].ID = "ev_rewritten"

	second, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if second[0].ID != "ev_1" {
		t.Errorf("a caller's edit reached the store: id is %s, want ev_1", second[0].ID)
	}
	if second[0].Data["provider"] != "mock" {
		t.Errorf("a caller's edit reached the stored payload: provider is %v, want mock", second[0].Data["provider"])
	}
}

func TestRecordEventAcceptsAnEmptyPayload(t *testing.T) {
	m := NewMemory(true)
	ctx := context.Background()

	// Not every event type carries data, and cloneData has a nil branch that
	// nothing else exercises.
	e := &Event{ID: "ev_1", Type: "catalog.stale", Created: time.Now().UTC(), TenantID: "ten_a"}
	if err := m.RecordEvent(ctx, e); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	got, err := m.ListEvents(ctx, "ten_a")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListEvents returned %d events, want 1", len(got))
	}
	if got[0].Data != nil {
		t.Errorf("an event stored with no payload reads back as %v, want nil", got[0].Data)
	}
}

func ids(events []*Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}
