package analytics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/store"
)

// composed builds the store the gateway actually runs with when analytics is
// configured, and reports every path the analytics half was asked for.
func composed(t *testing.T) (*Store, *[]string) {
	t.Helper()

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/breakdown") {
			_, _ = w.Write([]byte(`[{"key":"gpt-4o-mini","requests":1,"total_tokens":7,"cost_usd":0.5}]`))
			return
		}
		_, _ = w.Write([]byte(`{"requests":2,"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"cost_usd":0.5}`))
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(config.Analytics{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return NewStore(store.NewMemory(false), client), &seen
}

func TestComposedStoreSatisfiesTheStoreInterface(t *testing.T) {
	// The gateway hands this to server.New in place of the memory store, so the
	// embedded interface must genuinely cover everything the four overrides
	// leave alone.
	s, _ := composed(t)
	var _ store.Store = s
}

func TestComposedStoreKindNamesBothHalves(t *testing.T) {
	s, _ := composed(t)
	// "analytics" alone would tell an operator the deployment is durable when a
	// restart still loses every tenant and key.
	if got := s.Kind(); got != "memory+analytics" {
		t.Fatalf("Kind() = %q", got)
	}
}

func TestUsageGoesToAnalytics(t *testing.T) {
	s, seen := composed(t)
	ctx := context.Background()

	if err := s.RecordUsage(ctx, &store.UsageRecord{RequestID: "r1", TenantID: "t"}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	totals, err := s.Usage(ctx, "t", since, until)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if _, err := s.KeyUsage(ctx, "t", "cg-abc", since, until); err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	rows, err := s.UsageBreakdown(ctx, "t", since, until, "model")
	if err != nil {
		t.Fatalf("UsageBreakdown: %v", err)
	}

	want := []string{
		"/api/v1/usage",
		"/api/v1/usage/totals",
		"/api/v1/usage/totals",
		"/api/v1/usage/breakdown",
	}
	if got := *seen; !equal(got, want) {
		t.Fatalf("analytics saw %v, want %v", got, want)
	}
	// The answers are the service's, not the memory store's — which has never
	// been written to and would report nothing at all.
	if totals.TotalTokens != 7 {
		t.Fatalf("totals = %+v, want the service's numbers", totals)
	}
	if len(rows) != 1 || rows[0].Key != "gpt-4o-mini" {
		t.Fatalf("breakdown = %+v", rows)
	}
}

func TestARecordedRequestIsNotAlsoHeldInMemory(t *testing.T) {
	s, _ := composed(t)
	ctx := context.Background()

	if err := s.RecordUsage(ctx, &store.UsageRecord{RequestID: "r1", TenantID: "t", TotalTokens: 99}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	// Writing to both halves would double-count nothing today but would make the
	// memory copy a second source of truth that silently diverges after the
	// first analytics outage.
	inner, err := s.Store.Usage(ctx, "t", since, until)
	if err != nil {
		t.Fatalf("inner Usage: %v", err)
	}
	if inner.Requests != 0 {
		t.Fatalf("the wrapped store recorded %d requests, want none", inner.Requests)
	}
}

func TestEverythingElseStaysInMemory(t *testing.T) {
	s, seen := composed(t)
	ctx := context.Background()

	tenant, err := s.CreateTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := s.GetTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if _, _, err := s.CreateAPIKey(ctx, tenant.ID, store.PlaneData, "default", "", nil); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if _, err := s.ListRoutes(ctx, tenant.ID); err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if _, err := s.GetQuota(ctx, tenant.ID, ""); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetQuota: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Authentication and routing must not depend on a second process being up.
	if got := *seen; len(got) != 0 {
		t.Fatalf("analytics was called for %v", got)
	}
}

func TestAKeyMintedInMemoryStillResolves(t *testing.T) {
	s, _ := composed(t)
	ctx := context.Background()

	tenant, err := s.CreateTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, plaintext, err := s.CreateAPIKey(ctx, tenant.ID, store.PlaneData, "default", "", nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	// Composition must not have broken the request path it deliberately leaves
	// alone: the embedded store still holds the hash it just minted.
	key, owner, err := s.ResolveKey(ctx, plaintext)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if owner.ID != tenant.ID || key.TenantID != tenant.ID {
		t.Fatalf("resolved to tenant %q, want %q", owner.ID, tenant.ID)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
