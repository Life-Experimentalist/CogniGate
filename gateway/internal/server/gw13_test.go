package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// GW-13 gives four rejections in the 429 family and requires them to stay
// distinct. Two of them — the rate limit and the concurrency cap — are limits
// the gateway imposes on itself rather than accounting it reads from quotas, and
// these are the tests for those two and for the per-tenant narrowing GW-13
// allows of both.

func TestRateLimiterSpendsABurstAndThenRefills(t *testing.T) {
	now := time.Now()
	l := newRateLimiter()
	l.now = func() time.Time { return now }

	// A burst of three is three requests and no more, however fast they arrive.
	for i := range 3 {
		if _, ok := l.take("ten_a", 1, 3, 0); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	wait, ok := l.take("ten_a", 1, 3, 0)
	if ok {
		t.Fatal("the fourth request inside one instant was admitted; the burst is not a bound")
	}
	// One token per second, and the bucket is empty, so the caller waits about a
	// second. Asserted as a range because the arithmetic is floating point.
	if wait <= 0 || wait > time.Second {
		t.Errorf("Retry-After hint = %v, want (0s, 1s]", wait)
	}

	// Refill is proportional to elapsed time, not a reset: two seconds at one per
	// second buys exactly two requests.
	now = now.Add(2 * time.Second)
	for i := range 2 {
		if _, ok := l.take("ten_a", 1, 3, 0); !ok {
			t.Fatalf("request %d after a two-second refill was refused", i+1)
		}
	}
	if _, ok := l.take("ten_a", 1, 3, 0); ok {
		t.Error("a third request was admitted; the refill gave back more than it should")
	}

	// A different tenant is untouched by any of it, which is what "per tenant"
	// has to mean to be worth configuring.
	if _, ok := l.take("ten_b", 1, 3, 0); !ok {
		t.Error("a second tenant was refused for the first tenant's spending")
	}
}

func TestRateLimiterTreatsZeroAsNoLimit(t *testing.T) {
	l := newRateLimiter()
	for i := range 100 {
		if _, ok := l.take("ten_a", 0, 0, 0); !ok {
			t.Fatalf("request %d was refused by a rate limit configured to zero", i+1)
		}
	}
}

// A per-day request allowance is a quota rather than a third limiter window,
// so it reads back the way the other caps do: a slot with a cap, a remaining
// figure and a reset, in its own unit and unaffected by cost_visibility.
func TestRequestQuotaReportsItsOwnSlot(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	for range 3 {
		err := h.mem.RecordUsage(context.Background(), &store.UsageRecord{
			RequestID:   store.NewID(store.IDRequest),
			TenantID:    tenant.id,
			KeyPrefix:   "cg-testkey",
			Provider:    "test",
			Model:       "test-small",
			TotalTokens: 10,
			StatusCode:  http.StatusOK,
			RecordedAt:  time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("recording usage: %v", err)
		}
	}

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/quota", tenant.adminKey,
		map[string]any{"day": map[string]any{"requests": map[string]any{"cap": 5}}})
	if res.status != http.StatusOK {
		t.Fatalf("setting a request quota: status %d, body %s", res.status, res.body)
	}

	var out usageResponse
	h.do(http.MethodGet, "/v1/usage?window=day", tenant.dataKey, nil).decode(t, &out)
	var slot *usageLimit
	for i := range out.Limits {
		if out.Limits[i].Unit == unitRequests {
			slot = &out.Limits[i]
		}
	}
	if slot == nil {
		t.Fatalf("the requests cap is missing from /v1/usage, limits: %+v", out.Limits)
	}
	if slot.Window != windowDay {
		t.Errorf("window = %q, want %q", slot.Window, windowDay)
	}
	if slot.Consumed != 3 {
		t.Errorf("consumed = %v, want the 3 rows recorded", slot.Consumed)
	}
	if slot.Remaining != 2 {
		t.Errorf("remaining = %v, want 2", slot.Remaining)
	}
}

// A request cap that is reached rejects, and rejects as a quota rather than a
// budget: nothing here is about money, and a caller keying on the code should
// not have to guess which.
func TestRequestQuotaRejectsAsAQuotaNotABudget(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	err := h.mem.RecordUsage(context.Background(), &store.UsageRecord{
		RequestID:   store.NewID(store.IDRequest),
		TenantID:    tenant.id,
		KeyPrefix:   "cg-testkey",
		Provider:    "test",
		Model:       "test-small",
		TotalTokens: 10,
		StatusCode:  http.StatusOK,
		RecordedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("recording usage: %v", err)
	}

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/quota", tenant.adminKey,
		map[string]any{"day": map[string]any{"requests": map[string]any{"cap": 1}}})
	if res.status != http.StatusOK {
		t.Fatalf("setting a request quota: status %d, body %s", res.status, res.body)
	}

	res = h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey,
		map[string]any{"model": "test-small", "messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		}})
	if res.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the request cap is reached; body %s",
			res.status, res.body)
	}
	if !bytes.Contains(res.body, []byte("quota_exceeded")) {
		t.Errorf("body = %s, want the quota_exceeded code rather than a budget one", res.body)
	}
}

func TestMinuteWindowCapsTheMinuteAndResetsWhole(t *testing.T) {
	now := time.Now()
	l := newRateLimiter()
	l.now = func() time.Time { return now }

	// The bucket is switched off, so the minute window is the only ceiling and
	// what it admits is exactly its cap.
	for i := range 5 {
		if _, ok := l.take("ten_a", 0, 0, 5); !ok {
			t.Fatalf("request %d of the minute allowance was refused", i+1)
		}
	}
	wait, ok := l.take("ten_a", 0, 0, 5)
	if ok {
		t.Fatal("a sixth request was admitted against a cap of five")
	}
	// A fixed window resets whole, so the cooldown is the rest of the minute
	// rather than the time for one request-worth of allowance to appear.
	if wait <= 0 || wait > time.Minute {
		t.Errorf("Retry-After hint = %v, want (0s, 1m]", wait)
	}

	// Half a minute buys nothing: unlike the bucket, the window does not refill
	// proportionally.
	now = now.Add(30 * time.Second)
	if _, ok := l.take("ten_a", 0, 0, 5); ok {
		t.Error("the window refilled part way through; it is meant to reset whole")
	}

	now = now.Add(31 * time.Second)
	for i := range 5 {
		if _, ok := l.take("ten_a", 0, 0, 5); !ok {
			t.Fatalf("request %d of the next minute was refused", i+1)
		}
	}

	// Per tenant, like the bucket it sits over.
	if _, ok := l.take("ten_b", 0, 0, 5); !ok {
		t.Error("a second tenant was refused for the first tenant's spending")
	}
}

// Whichever ceiling refuses, the request is charged against neither: a client
// held off by the bucket must not spend its minute allowance on requests that
// never ran, and the cooldown it is given has to clear both.
func TestRefusedRequestsAreChargedAgainstNeitherCeiling(t *testing.T) {
	now := time.Now()
	l := newRateLimiter()
	l.now = func() time.Time { return now }

	// A bucket of one token per second with a burst of one, under a minute cap
	// of three. The burst is spent immediately, so everything after it in this
	// instant is refused by the bucket.
	if _, ok := l.take("ten_a", 1, 1, 3); !ok {
		t.Fatal("the first request was refused")
	}
	for i := range 20 {
		if _, ok := l.take("ten_a", 1, 1, 3); ok {
			t.Fatalf("refusal %d was admitted; the burst is not a bound", i+1)
		}
	}

	// Twenty refusals later the minute allowance still has two left, because a
	// refused request consumed none of it.
	for i := range 2 {
		now = now.Add(time.Second)
		if _, ok := l.take("ten_a", 1, 1, 3); !ok {
			t.Fatalf("request %d was refused; the refusals ate the minute allowance", i+1)
		}
	}

	// The minute cap is now the binding one, and the cooldown reported is its
	// own rather than the second the bucket would have quoted.
	now = now.Add(time.Second)
	wait, ok := l.take("ten_a", 1, 1, 3)
	if ok {
		t.Fatal("a fourth request was admitted against a minute cap of three")
	}
	if wait <= time.Second {
		t.Errorf("Retry-After hint = %v, want the rest of the minute, not the bucket's second", wait)
	}
}

// The sweep drops entries to bound memory, and an entry it drops takes its
// minute count with it. A tenant idle long enough for its bucket to refill has
// not necessarily finished its minute, so refilled alone must not be enough.
func TestSweepKeepsAPartlySpentMinuteWindow(t *testing.T) {
	now := time.Now()
	l := newRateLimiter()
	l.now = func() time.Time { return now }

	if _, ok := l.take("ten_a", 50, 100, 1); !ok {
		t.Fatal("the first request was refused")
	}
	// Long enough for a burst of 100 at 50 a second to have refilled twice over,
	// and well short of the minute.
	now = now.Add(10 * time.Second)
	l.mu.Lock()
	l.sweep(now, 50, 100)
	l.mu.Unlock()
	if _, ok := l.take("ten_a", 50, 100, 1); ok {
		t.Error("the sweep forgot a spent minute window and handed back a fresh allowance")
	}

	// Past the minute it is genuinely finished with, and the entry may go.
	now = now.Add(51 * time.Second)
	l.mu.Lock()
	l.sweep(now, 50, 100)
	l.mu.Unlock()
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("buckets held %d entries after the window expired, want 0", n)
	}
}

func TestTenantLimitsOnlyNarrow(t *testing.T) {
	h := newHarness(t)

	tenant := &store.Tenant{ID: "ten_x", Limits: store.TenantLimits{
		RequestTimeoutSeconds: 5,
		MaxConcurrentPerKey:   2,
		// Deliberately absurd. A value above the deployment's is refused by the
		// admin API, so the only way one reaches the store is a build whose
		// validation was laxer than this one's — and the resolver must not
		// honour it either.
		RequestsPerSecond: 1_000_000,
	}}
	eff := h.srv.limitsFor(tenant)

	if eff.RequestTimeout != 5*time.Second {
		t.Errorf("request timeout = %v, want the tenant's 5s", eff.RequestTimeout)
	}
	if eff.MaxConcurrentPerKey != 2 {
		t.Errorf("max concurrent = %d, want the tenant's 2", eff.MaxConcurrentPerKey)
	}
	if want := h.srv.Config.RateLimit.RequestsPerSecond; eff.RequestsPerSecond != want {
		t.Errorf("requests per second = %d, want the deployment ceiling %d: an override above it is not a limit",
			eff.RequestsPerSecond, want)
	}
	// Untouched fields keep the deployment's values rather than falling to zero,
	// which would be a limit of nothing.
	if eff.MaxRequestBytes != h.srv.Config.Limits.MaxRequestBytes {
		t.Errorf("max request bytes = %d, want the deployment's %d",
			eff.MaxRequestBytes, h.srv.Config.Limits.MaxRequestBytes)
	}

	// And a tenant with no overrides at all is the deployment, exactly.
	if bare := h.srv.limitsFor(&store.Tenant{ID: "ten_y"}); bare != h.srv.limitsFor(nil) {
		t.Errorf("a tenant without overrides resolves to %+v, want the deployment's %+v",
			bare, h.srv.limitsFor(nil))
	}
}

func TestTenantLimitsAboveTheCeilingAreRefused(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, tc := range []struct {
		name  string
		body  map[string]any
		param string
	}{
		{
			name:  "above the ceiling",
			body:  map[string]any{"request_timeout_seconds": 100000},
			param: "limits.request_timeout_seconds",
		},
		{
			name:  "negative",
			body:  map[string]any{"max_concurrent_per_key": -1},
			param: "limits.max_concurrent_per_key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
				map[string]any{"limits": tc.body})
			body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
			if body.Error.Param == nil || *body.Error.Param != tc.param {
				t.Errorf("param = %v, want %q: an operator has to be told which limit was wrong",
					body.Error.Param, tc.param)
			}
		})
	}
}

func TestTenantRateLimitAnswersRateLimitedWithRetryAfter(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	patchLimits(t, h, tenant.id, map[string]any{"requests_per_second": 1, "burst_capacity": 1})

	// The burst is one, so the second request inside the same instant is over it.
	// Metered on /v1/models rather than /v1/meta: health and meta are outside the
	// rate limit by design, so they cannot demonstrate it.
	if res := h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil); res.status != http.StatusOK {
		t.Fatalf("the first request was refused: status %d, body %s", res.status, res.body)
	}
	res := h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil)
	h.expectError(res, http.StatusTooManyRequests, apierr.CodeRateLimited)

	// GW-13 requires the hint, and GW-7 requires it on every 429: a client told
	// only "too many requests" retries by guessing.
	retry := res.header.Get("Retry-After")
	if n, err := strconv.Atoi(retry); err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
	}

	// A second tenant is unaffected, which is the difference between a rate limit
	// and an outage.
	other := h.newTenant("globex")
	if res := h.do(http.MethodGet, "/v1/models", other.dataKey, nil); res.status != http.StatusOK {
		t.Errorf("a second tenant got %d; one tenant's rate limit reached another", res.status)
	}
}

func TestHealthAndMetaAreOutsideTheRateLimit(t *testing.T) {
	// GW-5 exists so a caller can tell a degraded gateway from a healthy one, and
	// GW-9 makes /v1/meta the document it polls to do so. A tenant that has spent
	// its budget is precisely the one that needs both answers, so neither route
	// may be metered — otherwise the endpoints go dark in the one case they are
	// for. GW-5.AC-7 and GW-9.AC-5 say the same thing in numbers: each requires
	// 100 sequential calls to complete, which a burst of 100 cannot supply once
	// anything else has spent a token.
	h := newHarness(t)
	tenant := h.newTenant("acme")
	patchLimits(t, h, tenant.id, map[string]any{"requests_per_second": 1, "burst_capacity": 1})

	// One token, spent on a metered route. Everything below runs on an empty
	// bucket refilling at one per second, so a metered route cannot answer 200.
	if res := h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil); res.status != http.StatusOK {
		t.Fatalf("the first request on a fresh bucket was refused: status %d", res.status)
	}
	if res := h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil); res.status != http.StatusTooManyRequests {
		t.Fatalf("/v1/models answered %d on a spent bucket, want 429; the bucket is not empty "+
			"and the rest of this test would prove nothing", res.status)
	}

	for _, path := range []string{"/v1/health", "/v1/meta"} {
		for i := 0; i < 5; i++ {
			if res := h.do(http.MethodGet, path, tenant.dataKey, nil); res.status != http.StatusOK {
				t.Fatalf("%s call %d answered %d on a spent bucket, want 200: it is being metered",
					path, i+1, res.status)
			}
		}
	}

	// The exemption is from the rate limit alone. Concurrency still applies, which
	// is what stops a valid key polling health in a hot loop.
	release, ok := h.srv.limiter.acquire(keyIDFor(t, h, tenant.dataKey), 1)
	if !ok {
		t.Fatal("could not take a concurrency slot")
	}
	defer release()
	patchLimits(t, h, tenant.id, map[string]any{"max_concurrent_per_key": 1})
	res := h.do(http.MethodGet, "/v1/health", tenant.dataKey, nil)
	h.expectError(res, http.StatusTooManyRequests, apierr.CodeConcurrencyExceeded)
}

func TestTenantRateLimitIsSeparateFromTheConcurrencyLimit(t *testing.T) {
	// GW-13.AC-7 in the small: the two limits the gateway imposes on itself must
	// answer with their own codes. Conflating them would tell a client to back
	// off for a second when what it actually needs is to close a connection.
	h := newHarness(t)
	tenant := h.newTenant("acme")
	patchLimits(t, h, tenant.id, map[string]any{"max_concurrent_per_key": 1})

	// Held open by hand rather than by racing real requests: what is under test
	// is which code comes back, not the scheduler.
	release, ok := h.srv.limiter.acquire(keyIDFor(t, h, tenant.dataKey), 1)
	if !ok {
		t.Fatal("could not take the tenant's only concurrency slot")
	}
	defer release()

	res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	h.expectError(res, http.StatusTooManyRequests, apierr.CodeConcurrencyExceeded)
}

func TestTenantBodyLimitNarrowsTheDeploymentCeiling(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.MaxRequestBytes = 4096 })
	tenant := h.newTenant("acme")
	patchLimits(t, h, tenant.id, map[string]any{"max_request_bytes": 512})

	// Under the deployment ceiling and over the tenant's: the band that only the
	// post-authentication check can see.
	body := make([]byte, 1024)
	for i := range body {
		body[i] = 'x'
	}
	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, body)
	h.expectError(res, http.StatusRequestEntityTooLarge, apierr.CodeRequestTooLarge)
}

func TestMetaPublishesTheCallersOwnLimits(t *testing.T) {
	// GW-13.AC-6 is "the published limits are the enforced ones". Once a tenant
	// can be held to lower ones, publishing the deployment's would make the
	// document wrong for exactly the tenant that most needs it to be right.
	h := newHarness(t)
	tenant := h.newTenant("acme")
	patchLimits(t, h, tenant.id, map[string]any{"request_timeout_seconds": 7, "max_concurrent_per_key": 3})

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)
	if meta.Limits.RequestTimeoutSec != 7 {
		t.Errorf("request_timeout_seconds = %d, want the tenant's 7", meta.Limits.RequestTimeoutSec)
	}
	if meta.Limits.MaxConcurrentPerKey != 3 {
		t.Errorf("max_concurrent_per_key = %d, want the tenant's 3", meta.Limits.MaxConcurrentPerKey)
	}

	// Both planes describe the same tenant, so both must publish the same block.
	var admin adminMetaResponse
	h.do(http.MethodGet, "/admin/v1/meta", tenant.adminKey, nil).decode(t, &admin)
	if admin.Limits != meta.Limits {
		t.Errorf("admin limits %+v differ from data limits %+v; GW-9 requires one document",
			admin.Limits, meta.Limits)
	}
}

func TestStreamHoldsItsConcurrencySlotUntilTheRelayEnds(t *testing.T) {
	// GW-13 caps requests "in flight", and a stream is in flight for as long as
	// it is relaying — not for the few milliseconds it takes to send the status
	// line. Fiber runs the body writer after the handler returns, so without the
	// permit moving into the writer this limit would count how fast streams can
	// be started rather than how many are open.
	h := newHarness(t, func(c *config.Config) { c.Limits.StreamIdleTimeout = time.Minute })
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")
	patchLimits(t, h, tenant.id, map[string]any{"max_concurrent_per_key": 1})

	held := newHeldStream("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return &provider.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Stream:     held,
			Failure:    provider.FailNone,
		}, nil
	}

	payload, err := json.Marshal(chatRequest("test-small", true))
	if err != nil {
		t.Fatalf("marshalling the request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tenant.dataKey)

	done := make(chan error, 1)
	go func() {
		res, err := h.srv.App().Test(req, -1)
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		done <- err
	}()

	select {
	case <-held.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never read from the upstream stream")
	}

	// The only slot is spoken for, and it stays that way while the frames flow.
	res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	h.expectError(res, http.StatusTooManyRequests, apierr.CodeConcurrencyExceeded)

	held.finish()
	if err := <-done; err != nil {
		t.Fatalf("the streamed request failed: %v", err)
	}

	// And it is given back afterwards. A permit that leaked would make the first
	// stream on a key the last request on it.
	if again := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil); again.status != http.StatusOK {
		t.Errorf("status = %d after the stream finished, want 200: the slot was never released", again.status)
	}
}

func TestGatewayTimeoutQuotesTheBudgetThatWasEnforced(t *testing.T) {
	// The 504 message is the only place a caller learns what deadline it hit. A
	// tenant narrowed to five seconds being told it had two minutes sends them
	// looking for a hang that is not there.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	err := timeoutAware(ctx, 5*time.Second, apierr.UpstreamExhausted(2))
	var e *apierr.Error
	if !errors.As(err, &e) {
		t.Fatalf("timeoutAware returned %T, want *apierr.Error", err)
	}
	if e.Status != http.StatusGatewayTimeout || e.Code != apierr.CodeGatewayTimeout {
		t.Fatalf("status/code = %d/%s, want 504/%s", e.Status, e.Code, apierr.CodeGatewayTimeout)
	}
	if !strings.Contains(e.Msg, "5s") {
		t.Errorf("message = %q, want the tenant's 5s budget", e.Msg)
	}
}

// heldStream emits one frame and then blocks, so a test can hold a relay open
// for as long as it needs to look at what the gateway is doing meanwhile.
type heldStream struct {
	frame   []byte
	started chan struct{}
	release chan struct{}
	sent    bool
	once    sync.Once
}

func newHeldStream(frame string) *heldStream {
	return &heldStream{
		frame:   []byte(frame),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *heldStream) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		close(s.started)
		return copy(p, s.frame), nil
	}
	<-s.release
	return 0, io.EOF
}

// finish lets the blocked Read return, ending the relay. Close does the same,
// because the relay's stall watchdog has no other lever and a reader that ignored
// Close would hang the test rather than end it — so both go through one Once.
func (s *heldStream) finish() { s.once.Do(func() { close(s.release) }) }

func (s *heldStream) Close() error {
	s.finish()
	return nil
}

// patchLimits sets a tenant's overrides through the admin API rather than by
// reaching into the store, so every test here also exercises the route an
// operator actually uses.
func patchLimits(t *testing.T, h *harness, tenantID string, limits map[string]any) {
	t.Helper()
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenantID, testBootstrapKey,
		map[string]any{"limits": limits})
	if res.status != http.StatusOK {
		t.Fatalf("setting limits: status %d, body %s", res.status, res.body)
	}
}

// keyIDFor recovers the key id behind a plaintext secret, which is what the
// concurrency limiter counts against.
func keyIDFor(t *testing.T, h *harness, secret string) string {
	t.Helper()
	key, _, err := h.mem.ResolveKey(t.Context(), secret)
	if err != nil {
		t.Fatalf("resolving the test key: %v", err)
	}
	return key.ID
}
