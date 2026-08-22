package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

const testSecret = "webhook-secret-0123456789"

// received is one delivery as the endpoint saw it.
type received struct {
	body      []byte
	eventID   string
	signature string
}

// recorder is a webhook endpoint. status is the answer it gives, and failFirst
// is how many attempts it rejects before switching to that answer, which is what
// makes the retry schedule observable.
type recorder struct {
	mu        sync.Mutex
	got       []received
	status    int
	failFirst int
}

func (r *recorder) handler(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	r.mu.Lock()
	r.got = append(r.got, received{
		body:      body,
		eventID:   req.Header.Get(httpx.HeaderEventID),
		signature: req.Header.Get(httpx.HeaderSignature),
	})
	n := len(r.got)
	status, failFirst := r.status, r.failFirst
	r.mu.Unlock()

	if n <= failFirst {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func (r *recorder) deliveries() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

// waitFor polls until cond holds or the deadline passes. Delivery is
// asynchronous by design, so a test that asserted immediately would be asserting
// on a race rather than on behaviour.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// newDispatcher builds a dispatcher over an in-memory store with the retry
// delays collapsed, so the schedule is exercised without the test taking the
// production 5s + 10s + 20s + 40s to run.
func newDispatcher(t *testing.T, st store.Store, mutate ...func(*Options)) *Dispatcher {
	t.Helper()

	opts := Options{
		BaseBackoff: time.Millisecond,
		Timeout:     2 * time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, m := range mutate {
		m(&opts)
	}
	d := New(st, opts)
	t.Cleanup(d.Close)
	return d
}

// tenantWithWebhook returns a tenant id and registers one webhook against url.
func tenantWithWebhook(t *testing.T, st *store.Memory, url string, subscribed ...string) string {
	t.Helper()

	tenant, err := st.CreateTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}
	if _, err := st.CreateWebhook(context.Background(), &store.Webhook{
		TenantID: tenant.ID,
		URL:      url,
		Secret:   testSecret,
		Events:   subscribed,
		Enabled:  true,
	}); err != nil {
		t.Fatalf("creating webhook: %v", err)
	}
	return tenant.ID
}

func TestDeliversTheEnvelopeWithIdentityAndSignature(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, BreakerOpened)

	d := newDispatcher(t, st)
	d.Emit(context.Background(), tenantID, BreakerOpened, map[string]any{
		"provider": "primary",
		"model":    "test-small",
	})

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 1 }) {
		t.Fatalf("deliveries = %d, want 1", len(rec.deliveries()))
	}
	got := rec.deliveries()[0]

	var envelope Envelope
	if err := json.Unmarshal(got.body, &envelope); err != nil {
		t.Fatalf("delivered body is not JSON: %v\nbody: %s", err, got.body)
	}
	if envelope.Type != BreakerOpened {
		t.Errorf("type = %q, want %q", envelope.Type, BreakerOpened)
	}
	if envelope.Tenant != tenantID {
		t.Errorf("tenant = %q, want %q", envelope.Tenant, tenantID)
	}
	if !strings.HasPrefix(envelope.ID, store.IDEvent+"_") {
		t.Errorf("event id = %q, want a %s-prefixed id", envelope.ID, store.IDEvent)
	}
	if envelope.Created.IsZero() {
		t.Error("envelope carries no created timestamp")
	}
	if envelope.Data["provider"] != "primary" {
		t.Errorf("data = %v, want the emitted payload", envelope.Data)
	}

	// The header must agree with the body's own id, or a receiver cannot
	// deduplicate without parsing the payload first.
	if got.eventID != envelope.ID {
		t.Errorf("%s = %q, want %q", httpx.HeaderEventID, got.eventID, envelope.ID)
	}

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(got.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got.signature != want {
		t.Errorf("%s = %q, want %q", httpx.HeaderSignature, got.signature, want)
	}
}

// The signature has to be over the exact bytes on the wire. Verifying against a
// re-encoding of the parsed JSON is the mistake every receiver makes once, and
// it fails on key order — so the contract is stated as a test.
func TestSignatureCoversTheDeliveredBytes(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"breaker.opened"}`)
	if Sign(testSecret, body) == Sign(testSecret, append(body, ' ')) {
		t.Error("the signature does not depend on the exact body")
	}
	if Sign(testSecret, body) == Sign(testSecret+"x", body) {
		t.Error("the signature does not depend on the secret")
	}
	if !strings.HasPrefix(Sign(testSecret, body), "sha256=") {
		t.Errorf("signature = %q, want a sha256= prefix", Sign(testSecret, body))
	}
}

func TestDeliversOnlyToSubscribedAndEnabledWebhooks(t *testing.T) {
	wanted := &recorder{}
	wantedSrv := httptest.NewServer(http.HandlerFunc(wanted.handler))
	defer wantedSrv.Close()

	unwanted := &recorder{}
	unwantedSrv := httptest.NewServer(http.HandlerFunc(unwanted.handler))
	defer unwantedSrv.Close()

	disabled := &recorder{}
	disabledSrv := httptest.NewServer(http.HandlerFunc(disabled.handler))
	defer disabledSrv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, wantedSrv.URL, BreakerOpened, BreakerClosed)

	ctx := context.Background()
	if _, err := st.CreateWebhook(ctx, &store.Webhook{
		TenantID: tenantID, URL: unwantedSrv.URL, Secret: testSecret,
		Events: []string{QuotaHardCapReached}, Enabled: true,
	}); err != nil {
		t.Fatalf("creating webhook: %v", err)
	}
	if _, err := st.CreateWebhook(ctx, &store.Webhook{
		TenantID: tenantID, URL: disabledSrv.URL, Secret: testSecret,
		Events: []string{BreakerOpened}, Enabled: false,
	}); err != nil {
		t.Fatalf("creating webhook: %v", err)
	}

	d := newDispatcher(t, st)
	d.Emit(ctx, tenantID, BreakerOpened, map[string]any{"provider": "primary"})

	if !waitFor(t, func() bool { return len(wanted.deliveries()) == 1 }) {
		t.Fatalf("subscribed endpoint got %d deliveries, want 1", len(wanted.deliveries()))
	}
	// A short settle so a wrong delivery has time to arrive and be caught, rather
	// than the test passing because it looked too early.
	time.Sleep(50 * time.Millisecond)

	if n := len(unwanted.deliveries()); n != 0 {
		t.Errorf("an endpoint subscribed to another type got %d deliveries", n)
	}
	if n := len(disabled.deliveries()); n != 0 {
		t.Errorf("a disabled endpoint got %d deliveries", n)
	}
}

func TestDeliversToEveryEndpointForOneEvent(t *testing.T) {
	first := &recorder{}
	firstSrv := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstSrv.Close()

	second := &recorder{}
	secondSrv := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondSrv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, firstSrv.URL, CatalogModelAdded)
	if _, err := st.CreateWebhook(context.Background(), &store.Webhook{
		TenantID: tenantID, URL: secondSrv.URL, Secret: testSecret,
		Events: []string{CatalogModelAdded}, Enabled: true,
	}); err != nil {
		t.Fatalf("creating webhook: %v", err)
	}

	d := newDispatcher(t, st)
	d.Emit(context.Background(), tenantID, CatalogModelAdded, map[string]any{"models": []string{"test-small"}})

	if !waitFor(t, func() bool {
		return len(first.deliveries()) == 1 && len(second.deliveries()) == 1
	}) {
		t.Fatalf("deliveries = %d and %d, want 1 each", len(first.deliveries()), len(second.deliveries()))
	}
	// Both endpoints must see the same event id, or a receiver correlating
	// notifications across systems sees two occurrences where there was one.
	if a, b := first.deliveries()[0].eventID, second.deliveries()[0].eventID; a != b {
		t.Errorf("event ids differ across endpoints: %q and %q", a, b)
	}
}

// At-least-once is only meaningful if the retry carries the same id, and if the
// receiver's rejection is actually retried rather than logged and forgotten.
func TestRetriesWithAStableEventID(t *testing.T) {
	rec := &recorder{failFirst: 2}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, QuotaHardCapReached)

	d := newDispatcher(t, st)
	d.Emit(context.Background(), tenantID, QuotaHardCapReached, map[string]any{"state": "hard-exceeded"})

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 3 }) {
		t.Fatalf("attempts = %d, want 3 (two rejected, one accepted)", len(rec.deliveries()))
	}
	got := rec.deliveries()
	for i, d := range got {
		if d.eventID != got[0].eventID {
			t.Errorf("attempt %d carries event id %q, want %q — a redelivery must be recognisable as one",
				i+1, d.eventID, got[0].eventID)
		}
	}

	// And it stops once accepted.
	time.Sleep(50 * time.Millisecond)
	if n := len(rec.deliveries()); n != 3 {
		t.Errorf("attempts = %d after acceptance, want 3", n)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, AliasDegraded)

	d := newDispatcher(t, st, func(o *Options) { o.MaxAttempts = 3 })
	d.Emit(context.Background(), tenantID, AliasDegraded, map[string]any{"alias": "fast"})

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 3 }) {
		t.Fatalf("attempts = %d, want 3", len(rec.deliveries()))
	}
	// A bounded schedule is the point: an endpoint that is permanently broken must
	// not be retried forever.
	time.Sleep(50 * time.Millisecond)
	if n := len(rec.deliveries()); n != 3 {
		t.Errorf("attempts = %d, want the schedule to stop at max_attempts", n)
	}
}

// The backoff has to actually delay. Without this, a dead endpoint would be hit
// max_attempts times as fast as the network allows, which is a retry storm
// rather than a retry policy.
func TestBackoffDelaysBetweenAttempts(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, RuleDegraded)

	d := newDispatcher(t, st, func(o *Options) {
		o.MaxAttempts = 3
		o.BaseBackoff = 40 * time.Millisecond // then 80ms: at least 120ms in total
	})

	started := time.Now()
	d.Emit(context.Background(), tenantID, RuleDegraded, map[string]any{"rule": "test-small"})
	if !waitFor(t, func() bool { return len(rec.deliveries()) == 3 }) {
		t.Fatalf("attempts = %d, want 3", len(rec.deliveries()))
	}
	if elapsed := time.Since(started); elapsed < 120*time.Millisecond {
		t.Errorf("three attempts took %v; the backoff is not doubling between them", elapsed)
	}
}

// A tenant with no webhooks is the common case, and it must cost nothing beyond
// the lookup — in particular it must not block the caller, which is on a request
// path.
func TestEmitIsANoOpWithoutWebhooks(t *testing.T) {
	st := store.NewMemory(false)
	tenant, err := st.CreateTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("creating tenant: %v", err)
	}

	d := newDispatcher(t, st)
	done := make(chan struct{})
	go func() {
		d.Emit(context.Background(), tenant.ID, BreakerOpened, nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked for a tenant with no webhooks")
	}
}

// Emit runs on a request path, so a full queue must drop rather than block.
// Losing a notification is bad; stalling the request that raised it is worse.
func TestEmitDoesNotBlockWhenTheQueueIsFull(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// Released before the line above runs: Close waits on the connection the
	// held handler is still holding, so a later close(block) never arrives.
	defer close(block)

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, BreakerOpened)

	// One worker, held by the first delivery, and a queue of one. Everything
	// after the second emit has nowhere to go.
	d := newDispatcher(t, st, func(o *Options) {
		o.Workers = 1
		o.Queue = 1
	})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			d.Emit(context.Background(), tenantID, BreakerOpened, map[string]any{"n": i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a full queue")
	}

	// The worker has to actually be held for the queue to have been full,
	// or this would pass without ever testing what it claims.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery ever reached the endpoint")
	}
}

// Close has to return even while a delivery is waiting out its backoff, or a
// single unreachable endpoint would hold shutdown open for the whole retry
// schedule.
func TestCloseInterruptsAPendingRetry(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	st := store.NewMemory(false)
	tenantID := tenantWithWebhook(t, st, srv.URL, BreakerOpened)

	d := New(st, Options{
		BaseBackoff: 30 * time.Second,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	d.Emit(context.Background(), tenantID, BreakerOpened, nil)

	if !waitFor(t, func() bool { return len(rec.deliveries()) == 1 }) {
		t.Fatal("the first attempt never happened")
	}

	closed := make(chan struct{})
	go func() {
		d.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited out the backoff instead of abandoning it")
	}

	// Close is idempotent; a second call from a shutdown path must not panic on
	// an already-closed channel.
	d.Close()
}

func TestRegistryMatchesTheDocumentedTypes(t *testing.T) {
	want := []string{
		"quota.threshold_crossed",
		"quota.hard_cap_reached",
		"breaker.opened",
		"breaker.closed",
		"catalog.model_added",
		"catalog.model_removed",
		"alias.degraded",
		"rule.degraded",
		"debug_capture.enabled",
		"debug_capture.disabled",
	}
	if len(Registry) != len(want) {
		t.Fatalf("registry has %d types, want %d", len(Registry), len(want))
	}
	for i, w := range want {
		// A rename is a breaking change for every subscriber, so the literal
		// strings are pinned here rather than compared against the constants
		// they come from.
		if Registry[i] != w {
			t.Errorf("registry[%d] = %q, want %q", i, Registry[i], w)
		}
	}
}
