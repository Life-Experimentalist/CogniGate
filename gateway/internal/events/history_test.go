package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cognigate/gateway/internal/store"
)

// GW-8 makes the stored history independent of delivery: a tenant with no
// webhook registered, and one whose endpoint refused every attempt, must both be
// able to find out what happened. That makes recording unconditional — it runs
// before the webhook list is even read — and these tests are what keep it that
// way, because on the happy path the store is invisible.

func storedEvents(t *testing.T, st *store.Memory, tenantID string) []*store.Event {
	t.Helper()
	got, err := st.ListEvents(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return got
}

func TestEmitRecordsAnEventForATenantWithNoWebhooks(t *testing.T) {
	st := store.NewMemory(false)
	tenant, err := st.CreateTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	d := newDispatcher(t, st)
	d.Emit(context.Background(), tenant.ID, BreakerOpened, map[string]any{"provider": "openai"})

	got := storedEvents(t, st, tenant.ID)
	if len(got) != 1 {
		t.Fatalf("the history holds %d events, want 1 — polling is the floor under delivery, so a tenant with no webhook still has a record", len(got))
	}
	if got[0].Type != BreakerOpened {
		t.Errorf("the stored event is %q, want %q", got[0].Type, BreakerOpened)
	}
	if got[0].TenantID != tenant.ID {
		t.Errorf("the stored event is attributed to %q, want %q", got[0].TenantID, tenant.ID)
	}
	if got[0].Data["provider"] != "openai" {
		t.Errorf("the stored payload is %v, want the emitted one", got[0].Data)
	}
	if got[0].ID == "" || got[0].Created.IsZero() {
		t.Errorf("the stored event has no identity or timestamp: %+v", got[0])
	}
}

func TestTheStoredEventCarriesTheIDThatWasDelivered(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, BreakerOpened)

	d := newDispatcher(t, st)
	d.Emit(context.Background(), tenantID, BreakerOpened, nil)

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 1 }) {
		t.Fatalf("the endpoint saw %d deliveries, want 1", len(rec.deliveries()))
	}

	got := storedEvents(t, st, tenantID)
	if len(got) != 1 {
		t.Fatalf("the history holds %d events, want 1", len(got))
	}
	// One id shared by the stored copy and every delivery is what makes a poll
	// reconcilable against a webhook: a receiver that saw the delivery can match
	// it, and one that missed it can tell it is the same occurrence.
	if got[0].ID != rec.deliveries()[0].eventID {
		t.Errorf("the stored id is %q and the delivered one %q; they must be the same event", got[0].ID, rec.deliveries()[0].eventID)
	}
}

func TestAnUndeliverableEventIsStillRecorded(t *testing.T) {
	// The endpoint refuses everything, so every attempt is spent. This is the
	// case the history exists for.
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, BreakerOpened)

	d := newDispatcher(t, st, func(o *Options) { o.MaxAttempts = 2 })
	d.Emit(context.Background(), tenantID, BreakerOpened, nil)

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 2 }) {
		t.Fatalf("the endpoint saw %d attempts, want the 2 it was configured for", len(rec.deliveries()))
	}

	if got := storedEvents(t, st, tenantID); len(got) != 1 {
		t.Fatalf("the history holds %d events after every delivery failed, want 1", len(got))
	}
}
