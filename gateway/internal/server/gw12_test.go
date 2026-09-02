package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// --- fixtures ---------------------------------------------------------------

// cachingOn is the deployment switch these tests turn. Nothing else in the
// default configuration changes: GW-12 is a capability a deployment adds, not a
// mode it runs in.
func cachingOn(c *config.Config) { c.Cache.Enabled = true }

// numberedUpstream answers with a body that differs on every call, so a test
// that gets the same bytes twice has proved a replay rather than a coincidence.
//
// It returns the counter so a test can also assert how many times the upstream
// was reached, which is the other half of the same claim: a hit that still
// called the provider has saved nothing.
func numberedUpstream(h *harness) *atomic.Int64 {
	var n atomic.Int64
	h.adapter.do = func(_ context.Context, _ provider.Credential, _ *provider.Request) (*provider.Response, error) {
		res := upstreamOK(10, 5)
		res.Body = []byte(`{"object":"chat.completion","id":"call-` +
			string(rune('0'+n.Add(1))) + `","choices":[]}`)
		return res, nil
	}
	return &n
}

// prefer is the opt-in header a client sends per request.
var prefer = map[string]string{httpx.HeaderCache: "prefer"}

// deterministicRequest is a body GW-12 considers eligible: temperature 0.
func deterministicRequest(model string) map[string]any {
	req := chatRequest(model, false)
	req["temperature"] = 0
	return req
}

func (h *harness) chat(tenant tenantFixture, body any, headers map[string]string) reply {
	h.t.Helper()
	return h.doWithHeaders(http.MethodPost, "/v1/chat/completions", tenant.dataKey, body, headers)
}

// --- AC-7: the capability is invisible when it is absent ---------------------

func TestNoCacheHeaderWhenTheDeploymentDeclinedCaching(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	// Asking for it explicitly must not conjure it: a header that answered
	// "bypass" would tell a client the gateway has a cache it does not have.
	res := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderCache); got != "" {
		t.Errorf("%s = %q with caching disabled; the header must be absent entirely",
			httpx.HeaderCache, got)
	}
}

func TestNoCacheHeaderWithoutAnOptIn(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	first := h.chat(tenant, deterministicRequest("test-small"), nil)
	second := h.chat(tenant, deterministicRequest("test-small"), nil)

	for i, res := range []reply{first, second} {
		if got := res.header.Get(httpx.HeaderCache); got != "" {
			t.Errorf("request %d: %s = %q; a caller who never opted in is owed no commentary on the cache",
				i+1, httpx.HeaderCache, got)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2; nothing may be cached without an opt-in", n)
	}
}

// --- AC-1: an identical request is served from store -------------------------

func TestSecondIdenticalRequestIsServedFromTheCache(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	first := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if first.status != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (body %s)", first.status, first.body)
	}
	if got := first.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Errorf("first request: %s = %q, want %q", httpx.HeaderCache, got, cacheMiss)
	}

	second := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if got := second.header.Get(httpx.HeaderCache); got != cacheHit {
		t.Fatalf("second request: %s = %q, want %q", httpx.HeaderCache, got, cacheHit)
	}
	if string(second.body) != string(first.body) {
		t.Errorf("the replayed body differs from the stored one:\nfirst:  %s\nsecond: %s",
			first.body, second.body)
	}
	if got := second.header.Get(httpx.HeaderServedBy); got != "primary/test-small" {
		t.Errorf("%s = %q on a hit, want the model that produced the entry",
			httpx.HeaderServedBy, got)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want 1; a hit that still called the provider saved nothing", n)
	}
}

// A hit is a different request even though it carries the same answer, and its
// own request id is what an operator correlates the log line by.
func TestAHitCarriesItsOwnRequestID(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	numberedUpstream(h)

	first := h.chat(tenant, deterministicRequest("test-small"), prefer)
	second := h.chat(tenant, deterministicRequest("test-small"), prefer)

	a, b := first.header.Get(httpx.HeaderRequestID), second.header.Get(httpx.HeaderRequestID)
	if b == "" {
		t.Fatal("a cache hit carries no request id")
	}
	if a == b {
		t.Error("the hit replayed the original request id; two requests happened, and a log line has to distinguish them")
	}
}

// --- AC-2: a hit consumes no quota -------------------------------------------

func TestAHitCostsNoTokensAndNoMoney(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	numberedUpstream(h)

	h.chat(tenant, deterministicRequest("test-small"), prefer)
	h.chat(tenant, deterministicRequest("test-small"), prefer)
	h.flushTelemetry()

	totals, err := h.mem.Usage(context.Background(), tenant.id,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("reading usage: %v", err)
	}
	// One upstream call at 10+5 tokens. The hit is counted as a request and as
	// nothing else: it consumed no upstream tokens, so charging for them would
	// bill a tenant twice for one completion.
	if totals.TotalTokens != 15 {
		t.Errorf("total tokens = %d, want 15; a cache hit must not advance the quota position",
			totals.TotalTokens)
	}
	if totals.Requests != 2 {
		t.Errorf("requests = %d, want 2; a hit is still a request the tenant made", totals.Requests)
	}
}

func TestAHitIsRecordedAsCached(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	numberedUpstream(h)

	h.chat(tenant, deterministicRequest("test-small"), prefer)
	h.chat(tenant, deterministicRequest("test-small"), prefer)
	h.flushTelemetry()

	rows := h.usage.all()
	if len(rows) != 2 {
		t.Fatalf("usage rows = %d, want 2", len(rows))
	}
	var cached int
	for _, r := range rows {
		if !r.Cached {
			continue
		}
		cached++
		if r.Provider != "primary" || r.Model != "test-small" {
			t.Errorf("the cached row is attributed to %s/%s, want the model that produced the entry",
				r.Provider, r.Model)
		}
		if r.TotalTokens != 0 || r.CostUSD != 0 {
			t.Errorf("the cached row charges %d tokens and %v USD; a replay consumed neither",
				r.TotalTokens, r.CostUSD)
		}
	}
	if cached != 1 {
		t.Errorf("rows marked cached = %d, want exactly the hit", cached)
	}
}

// --- AC-3: bypass ------------------------------------------------------------

func TestBypassNeitherReadsNorWritesTheCache(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	h.chat(tenant, deterministicRequest("test-small"), prefer)

	bypass := h.chat(tenant, deterministicRequest("test-small"),
		map[string]string{httpx.HeaderCache: "bypass"})
	if got := bypass.header.Get(httpx.HeaderCache); got != cacheBypass {
		t.Errorf("%s = %q, want %q", httpx.HeaderCache, got, cacheBypass)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("upstream calls = %d, want 2; bypass must reach the provider", n)
	}

	// And it left the stored entry alone rather than replacing it with its own
	// fresher answer: bypass is the caller declining the cache, not flushing it.
	after := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if got := after.header.Get(httpx.HeaderCache); got != cacheHit {
		t.Errorf("%s = %q after a bypass, want the original entry still there", httpx.HeaderCache, got)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2; the bypass overwrote the entry", n)
	}
}

// --- eligibility -------------------------------------------------------------

func TestSamplingRequestsAreNotCached(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	body := chatRequest("test-small", false)
	body["temperature"] = 0.7

	for i := 0; i < 2; i++ {
		res := h.chat(tenant, body, prefer)
		if got := res.header.Get(httpx.HeaderCache); got != cacheBypass {
			t.Errorf("request %d: %s = %q, want %q — a sampled completion is not reproducible",
				i+1, httpx.HeaderCache, got, cacheBypass)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2", n)
	}
}

func TestStreamsAreNeverCached(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamStream(), nil
	}

	body := deterministicRequest("test-small")
	body["stream"] = true

	res := h.chat(tenant, body, prefer)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderCache); got != cacheBypass {
		t.Errorf("%s = %q on a stream, want %q", httpx.HeaderCache, got, cacheBypass)
	}
}

// --- AC-4: keys do not cross tenants -----------------------------------------

func TestTheCacheIsTenantScoped(t *testing.T) {
	h := newHarness(t, cachingOn)
	acme := h.routeTenant("acme")
	other := h.routeTenant("other")
	calls := numberedUpstream(h)

	acmeFirst := h.chat(acme, deterministicRequest("test-small"), prefer)
	otherFirst := h.chat(other, deterministicRequest("test-small"), prefer)

	if got := otherFirst.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Fatalf("the second tenant's identical request: %s = %q, want %q — one tenant's prompt must never be answered from another's",
			httpx.HeaderCache, got, cacheMiss)
	}
	if string(otherFirst.body) == string(acmeFirst.body) {
		t.Error("the second tenant received the first tenant's stored answer")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want one per tenant", n)
	}
}

// --- AC-5: the key names the resolved model, so a repin misses ---------------

// The cache key is built from the resolved provider+model rather than from what
// the caller typed. That is what makes an alias repin safe: an operator who
// moves "primary" onto another model has changed what the answer should be, and
// a cache keyed on the alias name would go on serving the old model's work under
// the new model's name for the rest of the TTL.
func TestRepinningAnAliasMissesTheOldModelsEntry(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	h.putAlias(tenant, "swappable", map[string]any{"pin": "test-small"})
	req := deterministicRequest("swappable")

	first := h.chat(tenant, req, prefer)
	if first.status != http.StatusOK {
		t.Fatalf("first status = %d, want 200 (body %s)", first.status, first.body)
	}
	if got := h.chat(tenant, req, prefer).header.Get(httpx.HeaderCache); got != cacheHit {
		t.Fatalf("the alias did not cache at all: %s = %q", httpx.HeaderCache, got)
	}

	h.putAlias(tenant, "swappable", map[string]any{"pin": "test-large"})

	after := h.chat(tenant, req, prefer)
	if got := after.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Errorf("%s = %q after a repin, want %q: the entry belongs to the old model",
			httpx.HeaderCache, got, cacheMiss)
	}
	if got := after.header.Get(httpx.HeaderServedBy); got != "primary/test-large" {
		t.Errorf("%s = %q, want the newly pinned model", httpx.HeaderServedBy, got)
	}
	if string(after.body) == string(first.body) {
		t.Error("the repinned alias replayed the model it no longer points at")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2: one per distinct resolved model", n)
	}
}

// --- compatibility: reading a field must not narrow what the gateway accepts --

// The three sampling fields are read only to decide eligibility, so a value the
// gateway cannot interpret costs the caller a cache entry, never their request.
// {"n": 1.0} is what a Python client's json.dumps of a float emits and no int
// will hold it; before these fields were parsed at all it was forwarded
// untouched, and it still must be.
func TestAFloatValuedNIsAcceptedAndStillCacheable(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	numberedUpstream(h)

	req := chatRequest("test-small", false)
	req["n"] = 1.0
	req["top_p"] = 1.0

	first := h.chat(tenant, req, prefer)
	if first.status != http.StatusOK {
		t.Fatalf("status = %d, want 200: a float-valued n is a request the upstream accepts (body %s)",
			first.status, first.body)
	}
	if got := first.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Errorf("%s = %q, want %q: n of 1.0 is n of 1", httpx.HeaderCache, got, cacheMiss)
	}
	if got := h.chat(tenant, req, prefer).header.Get(httpx.HeaderCache); got != cacheHit {
		t.Errorf("%s = %q on the repeat, want %q", httpx.HeaderCache, got, cacheHit)
	}
}

// And a value that is not a number at all is refused a cache entry rather than a
// response: whether the upstream accepts it is the upstream's to say.
func TestAnUnreadableTemperatureIsRelayedAndNotCached(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	req := chatRequest("test-small", false)
	req["temperature"] = "hot"

	first := h.chat(tenant, req, prefer)
	if first.status != http.StatusOK {
		t.Fatalf("status = %d, want 200: the gateway does not adjudicate sampling values (body %s)",
			first.status, first.body)
	}
	if got := first.header.Get(httpx.HeaderCache); got != cacheBypass {
		t.Errorf("%s = %q, want %q: a temperature the gateway cannot read is not known to be 0",
			httpx.HeaderCache, got, cacheBypass)
	}
	h.chat(tenant, req, prefer)
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2: nothing was cached", n)
	}
}

// --- AC-6: flush --------------------------------------------------------------

func TestFlushMakesTheNextIdenticalRequestMiss(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	h.chat(tenant, deterministicRequest("test-small"), prefer)
	if res := h.chat(tenant, deterministicRequest("test-small"), prefer); res.header.Get(httpx.HeaderCache) != cacheHit {
		t.Fatal("the entry to be flushed was never stored")
	}

	flush := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/cache/flush", tenant.adminKey, nil)
	if flush.status != http.StatusOK {
		t.Fatalf("flush status = %d, want 200 (body %s)", flush.status, flush.body)
	}
	var flushed struct {
		Flushed int `json:"flushed"`
	}
	flush.decode(t, &flushed)
	if flushed.Flushed != 1 {
		t.Errorf("flush reported %d entries, want 1", flushed.Flushed)
	}

	after := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if got := after.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Errorf("%s = %q after a flush, want %q", httpx.HeaderCache, got, cacheMiss)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want the flushed request to have reached the provider", n)
	}
}

func TestFlushDoesNotReachAnotherTenant(t *testing.T) {
	h := newHarness(t, cachingOn)
	acme := h.routeTenant("acme")
	other := h.routeTenant("other")
	numberedUpstream(h)

	h.chat(other, deterministicRequest("test-small"), prefer)

	// acme's own admin key, aimed at another tenant: 404 rather than 403, for
	// the reason tenantScope gives — 403 would confirm the tenant exists.
	res := h.do(http.MethodPost, "/admin/v1/tenants/"+other.id+"/cache/flush", acme.adminKey, nil)
	if res.status != http.StatusNotFound {
		t.Errorf("cross-tenant flush status = %d, want 404 (body %s)", res.status, res.body)
	}

	if hit := h.chat(other, deterministicRequest("test-small"), prefer); hit.header.Get(httpx.HeaderCache) != cacheHit {
		t.Error("the other tenant's entry was flushed by a key that has no reach over it")
	}
}

func TestFlushAnswersEvenWithCachingDisabled(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/cache/flush", tenant.adminKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if !strings.Contains(string(res.body), `"flushed":0`) {
		t.Errorf("body = %s, want a zero count rather than an error", res.body)
	}
}

// --- eligibility: a tenant policy opts in without the client's help -----------

func TestTenantPolicyCachesWithoutAHeader(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	calls := numberedUpstream(h)

	// The root key, because a tenant's cache policy is set through the same
	// PATCH that renames and suspends it, and those are the operator's to make.
	patch := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"cache": map[string]any{"enabled": true, "ttl_seconds": 60}})
	if patch.status != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (body %s)", patch.status, patch.body)
	}

	h.chat(tenant, deterministicRequest("test-small"), nil)
	second := h.chat(tenant, deterministicRequest("test-small"), nil)

	if got := second.header.Get(httpx.HeaderCache); got != cacheHit {
		t.Errorf("%s = %q, want %q — an operator must be able to enable caching for a client it does not control",
			httpx.HeaderCache, got, cacheHit)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want 1", n)
	}
}

func TestTenantPolicyIsRefusedAboveTheDeploymentCeiling(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")

	over := int(h.srv.Config.Cache.MaxTTL.Seconds()) + 1
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"cache": map[string]any{"enabled": true, "ttl_seconds": over}})

	body := h.expectError(res, http.StatusBadRequest, "invalid_request")
	if body.Error.Param == nil || *body.Error.Param != "cache.ttl_seconds" {
		t.Errorf("param = %v, want cache.ttl_seconds", body.Error.Param)
	}
}

func TestTenantPolicySurvivesARead(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")

	h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"cache": map[string]any{"enabled": true, "ttl_seconds": 45}})

	var got store.Tenant
	h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id, tenant.adminKey, nil).decode(t, &got)
	if !got.Cache.Enabled || got.Cache.TTLSeconds != 45 {
		t.Errorf("cache policy read back as %+v, want it as written", got.Cache)
	}
}

// --- attribution --------------------------------------------------------------

// A cascade's answer came from a model the key does not name. Storing it would
// make the fallback invisible to whichever caller was later served the replay.
func TestAFallbackAnswerIsNotStoredUnderThePrimaryKey(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		if req.Model == "test-small" {
			return nil, errTestUpstreamDown
		}
		return upstreamOK(10, 5), nil
	}

	first := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if got := first.header.Get(httpx.HeaderServedBy); got != "primary/test-large" {
		t.Fatalf("%s = %q, want the fallback to have served", httpx.HeaderServedBy, got)
	}

	second := h.chat(tenant, deterministicRequest("test-small"), prefer)
	if got := second.header.Get(httpx.HeaderCache); got != cacheMiss {
		t.Errorf("%s = %q; the fallback's answer was stored under the primary's key and replayed as if the primary had produced it",
			httpx.HeaderCache, got)
	}
	if calls := len(log.calls()); calls != 4 {
		t.Errorf("upstream calls = %d, want both cascades to have run", calls)
	}
}

// --- capability ---------------------------------------------------------------

func TestCacheHeaderIsRejectedNowhere(t *testing.T) {
	h := newHarness(t, cachingOn)
	tenant := h.routeTenant("acme")
	numberedUpstream(h)

	// An unknown value is not an error. GW-12 makes the header a preference,
	// and a client sending a value from a newer spec should get the default
	// behaviour rather than a 400 for a request the gateway could serve.
	res := h.chat(tenant, deterministicRequest("test-small"),
		map[string]string{httpx.HeaderCache: "no-store"})
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderCache); got != "" {
		t.Errorf("%s = %q; an unrecognised preference falls back to the tenant policy, which is off here",
			httpx.HeaderCache, got)
	}
}
