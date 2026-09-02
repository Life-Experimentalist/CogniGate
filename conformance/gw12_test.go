package conformance

import (
	"encoding/json"
	"net/http"
	"testing"
)

// GW-12 — response caching for deterministic requests.
//
// The capability is optional, so every criterion but the last runs only where
// /v1/meta claims gw-12, and the last only where it does not. Certifying both
// takes two runs against two configurations, which is the same honest cost
// GW-4's two quota-enforcement modes carry: a switch that changes what the
// gateway does cannot be certified from one side of itself.

// headerCache is GW-12's response header, spelled out rather than imported for
// the reason the GW-7 headers are.
const headerCache = "X-CogniGate-Cache"

// The three dispositions it carries.
const (
	cacheHit    = "hit"
	cacheMiss   = "miss"
	cacheBypass = "bypass"
)

// preferCache is the opt-in. Nothing is cached for a caller who did not ask for
// it and whose tenant has no policy, so almost every test here sends it.
var preferCache = map[string]string{headerCache: "prefer"}

// beginCacheDisabled starts the one criterion that is about the capability being
// absent.
//
// begin() skips a test whose capability is unclaimed, which is exactly backwards
// for AC-7: it is the criterion that only means something where caching is off.
// The id is registered the same way, so the GW-10 mapping still sees it and the
// report still carries its verdict.
func beginCacheDisabled(t *testing.T) *client {
	t.Helper()

	id := acID(t.Name())
	if id == "" {
		t.Fatalf("test name %q does not embed an acceptance-criterion id", t.Name())
	}
	record(t, id)

	if suite.cfg.BaseURL == "" {
		t.Skip("no target: set CONF_BASE_URL to the gateway under test")
	}
	if suite.provision != nil {
		t.Fatalf("the suite could not provision against the target: %v", suite.provision)
	}
	if suite.capabilities["gw-12"] {
		t.Skip("gw-12 is claimed; this criterion is about a deployment that declined caching")
	}
	return suite.client
}

// cacheTenant is a tenant of its own, pointed at the shared mock.
//
// Every test here takes one, because the cache is tenant-scoped: a test reusing
// the suite's tenant could not tell a hit it earned from one another test had
// stored under the same key.
func cacheTenant(t *testing.T, hint string) tenant {
	t.Helper()
	tn := newTenant(t, hint)
	addMockProvider(t, tn)
	return tn
}

// cacheableBody is what GW-12 calls eligible: non-streaming, and deterministic
// as expressed. It is built here rather than at each call site so that two
// requests in one test are identical by construction rather than by the author
// remembering to keep them so.
func cacheableBody(model string) map[string]any {
	return map[string]any{
		"model":       model,
		"messages":    []any{map[string]any{"role": "user", "content": "hi"}},
		"temperature": 0,
	}
}

func chatBody(t *testing.T, key string, body map[string]any, headers map[string]string) *response {
	t.Helper()
	resp, err := suite.client.tryWithHeaders(http.MethodPost, "/v1/chat/completions", key, body, headers)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	return resp
}

// cacheableChat is the request every hit in this file is a repeat of.
func cacheableChat(t *testing.T, key, model string, headers map[string]string) *response {
	t.Helper()
	return chatBody(t, key, cacheableBody(model), headers)
}

// notCached reports whether a disposition is one of the two an ineligible
// request may carry.
//
// AC-3 says "bypass/miss semantics per the eligibility rules" rather than naming
// one, and it is right to: a gateway that answers `miss` to a request it will
// never store has told the caller the truth about this response. What the
// criterion actually turns on is the upstream call count, which no wording
// leaves room to argue with.
func notCached(disposition string) bool {
	return disposition == cacheBypass || disposition == cacheMiss
}

// choicesOf is the part of a completion AC-1 requires to come back identical.
func choicesOf(t *testing.T, r *response) string {
	t.Helper()
	var body struct {
		Choices json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("completion body is not JSON: %v\n%s", err, truncate(r.Body))
	}
	return string(body.Choices)
}

// --- the criteria -----------------------------------------------------------

func TestGW12_AC1_AnIdenticalRequestIsServedFromTheStoredAnswer(t *testing.T) {
	begin(t)
	tn := cacheTenant(t, "gw12-ac1")

	before := upstreamCalls(t, "mock-chat-a")

	first := cacheableChat(t, tn.Key, "mock-chat-a", preferCache)
	if first.Status != http.StatusOK {
		t.Fatalf("first completion: status %d\n%s", first.Status, truncate(first.Body))
	}
	if got := first.Header.Get(headerCache); got != cacheMiss {
		t.Errorf("first completion: %s = %q, want %q", headerCache, got, cacheMiss)
	}

	second := cacheableChat(t, tn.Key, "mock-chat-a", preferCache)
	if second.Status != http.StatusOK {
		t.Fatalf("second completion: status %d\n%s", second.Status, truncate(second.Body))
	}
	if got := second.Header.Get(headerCache); got != cacheHit {
		t.Fatalf("second completion: %s = %q, want %q", headerCache, got, cacheHit)
	}

	// The upstream counter is what carries this criterion. The mock answers
	// every completion with the same bytes, so two identical bodies would be
	// identical with no cache at all; a call that never reached it would not.
	if calls := upstreamCalls(t, "mock-chat-a") - before; calls != 1 {
		t.Errorf("the mock served %d completions across two identical requests, want 1", calls)
	}
	if a, b := choicesOf(t, first), choicesOf(t, second); a != b {
		t.Errorf("the replayed choices differ from the stored ones:\nfirst:  %s\nsecond: %s", a, b)
	}
	if got := second.Header.Get(headerServedBy); got != first.Header.Get(headerServedBy) {
		t.Errorf("%s = %q on the hit and %q on the miss; a hit names the model that produced it",
			headerServedBy, got, first.Header.Get(headerServedBy))
	}
	// The header the criterion excepts from byte-identity: two requests happened,
	// and an operator correlates each by its own id.
	if a, b := first.Header.Get(headerRequestID), second.Header.Get(headerRequestID); a == b {
		t.Errorf("both responses carry request id %q; the hit replayed the original's", a)
	}
}

func TestGW12_AC2_AHitCostsNoTokensAndNoMoney(t *testing.T) {
	begin(t)
	tn := cacheTenant(t, "gw12-ac2")

	if resp := cacheableChat(t, tn.Key, "mock-chat-a", preferCache); resp.Status != http.StatusOK {
		t.Fatalf("priming the cache: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	primed := awaitUsage(t, tn.Key, "day",
		func(u usageReport) bool { return u.Requests == 1 && u.TotalTokens > 0 },
		"the metered miss")

	hit := cacheableChat(t, tn.Key, "mock-chat-a", preferCache)
	if got := hit.Header.Get(headerCache); got != cacheHit {
		t.Fatalf("the second request was not a hit: %s = %q", headerCache, got)
	}

	// Waiting on the request count rather than sleeping: usage is written after
	// the response has already gone back, so a "nothing changed" assertion made
	// too early would pass against a gateway that had simply not written the row
	// yet, and would go on passing after it did.
	after := awaitUsage(t, tn.Key, "day",
		func(u usageReport) bool { return u.Requests >= 2 },
		"the hit's own usage row")

	if after.TotalTokens != primed.TotalTokens {
		t.Errorf("total_tokens went from %d to %d across a cache hit; a replayed answer consumed none",
			primed.TotalTokens, after.TotalTokens)
	}
	if after.PromptTokens != primed.PromptTokens || after.CompletionTokens != primed.CompletionTokens {
		t.Errorf("prompt/completion tokens went from %d/%d to %d/%d across a cache hit",
			primed.PromptTokens, primed.CompletionTokens, after.PromptTokens, after.CompletionTokens)
	}
	if after.CostUSD != primed.CostUSD {
		t.Errorf("cost_usd went from %v to %v across a cache hit; nothing was bought",
			primed.CostUSD, after.CostUSD)
	}
	if after.Requests != 2 {
		t.Errorf("requests = %d, want 2: a hit is still a request the tenant made", after.Requests)
	}

	t.Run("the served-from-cache classification survives into the record", func(t *testing.T) {
		// The criterion's other half is that the usage record carries
		// cached: true. No published endpoint exposes that field — GET
		// /v1/usage aggregates, and its breakdown groups by model, provider,
		// key and client request id — so the strongest statement this suite can
		// make about a deployment is the GW-8 access log, which classifies the
		// same request the same way. The record's own flag is pinned by the
		// gateway's unit tests, where the store can be read directly.
		requireLogAccess(t)

		from := markLog(t)
		again := cacheableChat(t, tn.Key, "mock-chat-a", preferCache)
		if got := again.Header.Get(headerCache); got != cacheHit {
			t.Fatalf("the request under test was not a hit: %s = %q", headerCache, got)
		}
		id := again.Header.Get(headerRequestID)

		records := awaitLogLines(t, from, func(r map[string]any) bool {
			return r["request_id"] == id && r["cache"] != nil
		})
		lines := requestLines(records, id)
		if len(lines) == 0 {
			t.Fatalf("no access-log line for request %s", id)
		}
		for _, line := range lines {
			if line["cache"] != cacheHit {
				t.Errorf("the log line for a cache hit reports cache = %v, want %q",
					line["cache"], cacheHit)
			}
		}
	})
}

func TestGW12_AC3_SamplingAndStreamingRequestsAreNeverCached(t *testing.T) {
	begin(t)
	tn := cacheTenant(t, "gw12-ac3")

	// A request that asks the provider to sample cannot be replayed: the caller
	// asked for a fresh roll of the dice, and handing back a stored one answers a
	// different question than the one they put.
	sampling := cacheableBody("mock-chat-a")
	sampling["temperature"] = 0.7

	before := upstreamCalls(t, "mock-chat-a")
	for i := 1; i <= 2; i++ {
		resp := chatBody(t, tn.Key, sampling, preferCache)
		if resp.Status != http.StatusOK {
			t.Fatalf("sampling completion %d: status %d\n%s", i, resp.Status, truncate(resp.Body))
		}
		if got := resp.Header.Get(headerCache); !notCached(got) {
			t.Errorf("sampling completion %d: %s = %q; the request opted in but is not eligible",
				i, headerCache, got)
		}
	}
	if calls := upstreamCalls(t, "mock-chat-a") - before; calls != 2 {
		t.Errorf("the mock served %d sampling completions, want 2: neither may be cached", calls)
	}

	// And a stream, which GW-12 excludes outright: there is no complete body to
	// store at the moment the first frame goes out.
	streamed := cacheableBody("mock-chat-a")
	streamed["stream"] = true

	before = upstreamCalls(t, "mock-chat-a")
	for i := 1; i <= 2; i++ {
		resp := chatBody(t, tn.Key, streamed, preferCache)
		if resp.Status != http.StatusOK {
			t.Fatalf("streamed completion %d: status %d\n%s", i, resp.Status, truncate(resp.Body))
		}
		if got := resp.Header.Get(headerCache); !notCached(got) {
			t.Errorf("streamed completion %d: %s = %q; a stream is never cached", i, headerCache, got)
		}
	}
	if calls := upstreamCalls(t, "mock-chat-a") - before; calls != 2 {
		t.Errorf("the mock served %d streamed completions, want 2: a stream is never cached", calls)
	}
}

func TestGW12_AC4_AnotherTenantsIdenticalRequestMisses(t *testing.T) {
	begin(t)
	a := cacheTenant(t, "gw12-ac4a")
	b := cacheTenant(t, "gw12-ac4b")

	if got := cacheableChat(t, a.Key, "mock-chat-a", preferCache).Header.Get(headerCache); got != cacheMiss {
		t.Fatalf("tenant A's first request: %s = %q, want %q", headerCache, got, cacheMiss)
	}
	if got := cacheableChat(t, a.Key, "mock-chat-a", preferCache).Header.Get(headerCache); got != cacheHit {
		t.Fatalf("tenant A did not cache at all: %s = %q", headerCache, got)
	}

	// The same bytes, the same resolved model, a different tenant. GW-12 forbids
	// the sharing outright: tenant isolation beats hit rate, and one tenant
	// learning that another has already asked a question is itself the leak.
	if got := cacheableChat(t, b.Key, "mock-chat-a", preferCache).Header.Get(headerCache); got != cacheMiss {
		t.Errorf("tenant B: %s = %q, want %q; B was served an entry A paid for",
			headerCache, got, cacheMiss)
	}
}

func TestGW12_AC5_ARepinnedAliasMissesTheOldModelsEntry(t *testing.T) {
	begin(t)
	tn := cacheTenant(t, "gw12-ac5")

	alias := uniqueName("gw12-repin")
	putAlias(t, tn.ID, alias, map[string]any{"pin": "mock-chat-a"})

	if got := cacheableChat(t, tn.Key, alias, preferCache).Header.Get(headerCache); got != cacheMiss {
		t.Fatalf("the alias's first request: %s = %q, want %q", headerCache, got, cacheMiss)
	}
	hit := cacheableChat(t, tn.Key, alias, preferCache)
	if got := hit.Header.Get(headerCache); got != cacheHit {
		t.Fatalf("the alias did not cache at all: %s = %q", headerCache, got)
	}
	if served := servedModel(t, hit); served != "mock-chat-a" {
		t.Fatalf("the alias resolved to %q, not the model it was pinned to", served)
	}

	// The key names the resolved provider and model, never the alias, so moving
	// the alias moves the key with it. Without that, an operator who repinned
	// would go on serving the old model's answers under the new model's name for
	// the rest of the TTL, and nothing on the wire would say so.
	putAlias(t, tn.ID, alias, map[string]any{"pin": "mock-chat-b"})

	after := cacheableChat(t, tn.Key, alias, preferCache)
	if got := after.Header.Get(headerCache); got != cacheMiss {
		t.Errorf("%s = %q after the repin, want %q: the stored entry belongs to the old model",
			headerCache, got, cacheMiss)
	}
	if served := servedModel(t, after); served != "mock-chat-b" {
		t.Errorf("the repinned alias was served by %q, want mock-chat-b", served)
	}
}

func TestGW12_AC6_AFlushMakesTheNextIdenticalRequestMiss(t *testing.T) {
	begin(t)
	tn := cacheTenant(t, "gw12-ac6")

	cacheableChat(t, tn.Key, "mock-chat-a", preferCache)
	if got := cacheableChat(t, tn.Key, "mock-chat-a", preferCache).Header.Get(headerCache); got != cacheHit {
		t.Fatalf("nothing was cached to flush: %s = %q", headerCache, got)
	}

	flushed := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tn.ID+"/cache/flush", nil)
	if flushed.Status != http.StatusOK {
		t.Fatalf("POST cache/flush: status %d\n%s", flushed.Status, truncate(flushed.Body))
	}

	// The criterion allows ten seconds. This asserts the stricter thing, that the
	// very next request misses, because a flush that were eventually consistent
	// would leave an operator unable to tell whether the answer in front of them
	// is the one they just cleared.
	if got := cacheableChat(t, tn.Key, "mock-chat-a", preferCache).Header.Get(headerCache); got != cacheMiss {
		t.Errorf("%s = %q immediately after a flush, want %q", headerCache, got, cacheMiss)
	}
}

// TestGW12_AC7_NothingCarriesTheCacheHeaderWhenCachingIsOff is the criterion the
// specification marks as shared with GW-9.AC-4.
//
// It is written out rather than delegated because the mapping GW-10.AC-2
// enforces is 1:1: a criterion whose only test lives under another requirement's
// name has no verdict of its own in the report, and a reader of that report
// cannot tell an untested criterion from a shared one.
func TestGW12_AC7_NothingCarriesTheCacheHeaderWhenCachingIsOff(t *testing.T) {
	c := beginCacheDisabled(t)

	meta := c.data(t, http.MethodGet, "/v1/meta", nil).JSON(t)
	claimed, _ := meta["capabilities"].([]any)
	for _, entry := range claimed {
		if entry == "gw-12" {
			t.Error("/v1/meta claims gw-12 on a deployment whose caching is off")
		}
	}

	// A caller who never mentioned the cache is owed no commentary on it.
	if got := chat(t, suite.dataKey, "mock-chat-a").Header.Get(headerCache); got != "" {
		t.Errorf("an ordinary completion answered with %s: %q", headerCache, got)
	}

	// Asking for it explicitly must not turn it on, and must not be an error
	// either: a client that sends the header to every gateway it talks to is
	// entitled to the same answer from one that does not cache.
	asked := cacheableChat(t, suite.dataKey, "mock-chat-a", preferCache)
	if asked.Status != http.StatusOK {
		t.Errorf("a request preferring the cache was refused with %d where caching is off\n%s",
			asked.Status, truncate(asked.Body))
	}
	if got := asked.Header.Get(headerCache); got != "" {
		t.Errorf("a request that asked for the cache was answered with %s: %q", headerCache, got)
	}

	// The same request again. A cache that were quietly on would answer the
	// second from memory, and the header is how a client is told — so its absence
	// on a repeat is the stronger statement.
	repeat := cacheableChat(t, suite.dataKey, "mock-chat-a", preferCache)
	if got := repeat.Header.Get(headerCache); got != "" {
		t.Errorf("a repeated request was answered with %s: %q", headerCache, got)
	}
}
