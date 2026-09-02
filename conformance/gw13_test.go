package conformance

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-13: an unbounded proxy is a denial-of-service amplifier. Every criterion
// here is a comparison between a figure /v1/meta publishes and what the gateway
// actually does when that figure is crossed, because a limit nobody enforces is
// worse than no limit at all — a client sizes its batches against it.
//
// The numbers the tests narrow to are far below the deployment's defaults.
// Holding thirty-three completions open, or moving eight mebibytes twice, would
// buy nothing: each criterion is about the boundary, and the boundary is the
// published figure whatever it happens to be. So the tests set a small one, read
// it back from meta, and assert against what they read.

func TestGW13_AC1_AnOversizeRequestNeverReachesTheUpstream(t *testing.T) {
	c := begin(t)

	// A routable model, so "zero upstream calls" is a claim about the size check
	// running first rather than a claim about a name that would have been
	// refused anyway.
	model := addMockModel(t, uniqueName("gw13-ac1"))
	limit := limitInt(t, publishedLimits(t, suite.dataKey), "max_request_bytes")
	before := upstreamCalls(t, model)

	resp := c.do(t, http.MethodPost, "/v1/chat/completions", suite.dataKey,
		paddedRequest(model, limit+1))
	if resp.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body of max_request_bytes+1 (%d) answered %d, want 413\n%s",
			limit+1, resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "request_too_large" {
		t.Errorf("error.code = %q, want request_too_large", code)
	}

	// The point of the criterion. A gateway that forwards first and measures
	// afterwards has spent an upstream call, and the caller's money, on a
	// request it was always going to refuse.
	if after := upstreamCalls(t, model); after != before {
		t.Errorf("the mock saw %d calls for %s, want %d: the oversize request was forwarded before it was measured",
			after, model, before)
	}
}

func TestGW13_AC2_AnOversizeResponseIsRefusedRatherThanBuffered(t *testing.T) {
	begin(t)

	limit := limitInt(t, publishedLimits(t, suite.dataKey), "max_response_bytes")
	model := addMockModel(t, uniqueName("gw13-ac2"))
	injectFaultWith(t, model, mockprovider.FaultOversizeBody, mockprovider.ForeverCount,
		map[string]any{"bytes": limit + 1})

	resp := chat(t, suite.dataKey, model)
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("an upstream body of max_response_bytes+1 (%d) answered %d, want 502\n%s",
			limit+1, resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "response_too_large" {
		t.Errorf("error.code = %q, want response_too_large", code)
	}

	// The specification asks for a memory assertion in -perf mode and the code
	// path otherwise. This is the code path: an answer this small is only
	// possible if the read stopped at the ceiling, because a gateway that
	// forwarded the body would be answering with it.
	if len(resp.Body) > 64*1024 {
		t.Errorf("the refusal was %d bytes; a gateway that stopped reading at the ceiling has nothing that large to say",
			len(resp.Body))
	}
}

func TestGW13_AC3_AStalledStreamEndsWithATerminalErrorEvent(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw13-ac3")
	addMockProvider(t, tn)
	model := addMockModel(t, uniqueName("gw13-ac3"))
	refreshCatalogFor(t, tn.ID)
	awaitModel(t, tn.Key, model, true)

	// Content first, then silence on a connection that stays open. That is the
	// one upstream failure a stream cannot detect for itself: an abort arrives
	// as an EOF, but a stall arrives as nothing at all, and only a watchdog
	// notices nothing.
	//
	// The silence outlasts the deployment's own idle timeout, so a stream that
	// ends on time can only have been cut by the tenant's. Were the two the same
	// figure, this criterion would pass just as readily against a gateway that
	// ignored the override entirely.
	injectFaultWith(t, model, mockprovider.FaultStreamStall, mockprovider.ForeverCount,
		map[string]any{"delay_ms": 120_000})
	narrowLimits(t, tn.ID, map[string]any{"stream_idle_timeout_seconds": 2})

	// Read the narrowed figure back before relying on it. A stream that ran long
	// has two explanations — an override that never took, and a watchdog that
	// never fired — and only one of them is the gateway's fault.
	if got := limitInt(t, publishedLimits(t, tn.Key), "stream_idle_timeout_seconds"); got != 2 {
		t.Fatalf("the tenant was narrowed to a 2s idle timeout but is told it has %ds", got)
	}

	elapsed := allowSeconds(t, 30*time.Second)
	res := chatStream(t, tn.Key, model)
	elapsed("the stalled stream was cut off")

	if res.Status != http.StatusOK {
		t.Fatalf("the stream answered %d, want 200: the stall is after the headers, not before them", res.Status)
	}
	if !res.hasFrameContaining("upstream_stream_stalled") {
		t.Errorf("no frame carried upstream_stream_stalled; a caller left to guess why the frames stopped "+
			"cannot tell a finished answer from an abandoned one\nframes: %v", res.Frames)
	}
}

func TestGW13_AC4_ASlowCascadeIsCutOffAtTheRequestBudget(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw13-ac4")
	addMockProvider(t, tn)
	first := addMockModel(t, uniqueName("gw13-ac4-a"))
	second := addMockModel(t, uniqueName("gw13-ac4-b"))
	refreshCatalogFor(t, tn.ID)
	awaitModel(t, tn.Key, first, true)
	awaitModel(t, tn.Key, second, true)

	// Slow *and* wrong, which is the combination the criterion turns on: an
	// entry that only fails costs no time, and an entry that only hangs never
	// cascades, so neither on its own builds a chain that runs out of budget
	// partway down.
	const budget = 10
	for _, model := range []string{first, second} {
		injectFaultWith(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount,
			map[string]any{"delay_ms": 6_000})
	}

	alias := uniqueName("gw13-ac4-route")
	putRoute(t, tn.ID, alias, first, second)
	narrowLimits(t, tn.ID, map[string]any{"request_timeout_seconds": budget})

	elapsed := allowSeconds(t, (budget+5)*time.Second)
	resp := chat(t, tn.Key, alias)
	elapsed("the cascade was cut off")

	if resp.Status != http.StatusGatewayTimeout {
		t.Fatalf("a cascade that outran its %ds budget answered %d, want 504\n%s",
			budget, resp.Status, truncate(resp.Body))
	}
	// Not upstream_exhausted. Both are true of this request, but only one of
	// them sends an operator somewhere useful: the providers are healthy and the
	// deadline is what ran out.
	if code := resp.ErrorCode(t); code != "gateway_timeout" {
		t.Errorf("error.code = %q, want gateway_timeout", code)
	}
}

func TestGW13_AC5_TheRequestPastTheConcurrencyLimitIsRefused(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw13-ac5")
	addMockProvider(t, tn)
	held := addMockModel(t, uniqueName("gw13-ac5-held"))
	free := addMockModel(t, uniqueName("gw13-ac5-free"))
	refreshCatalogFor(t, tn.ID)
	awaitModel(t, tn.Key, held, true)
	awaitModel(t, tn.Key, free, true)
	sibling := newDataKey(t, tn.ID, uniqueName("gw13-ac5-sibling"))

	// The budget is what ends the held requests. Relying on the client hanging
	// up instead would test how quickly a disconnect propagates through the
	// gateway's HTTP stack, which is not what this criterion is about.
	narrowLimits(t, tn.ID, map[string]any{
		"max_concurrent_per_key":  2,
		"request_timeout_seconds": 10,
	})
	limit := limitInt(t, publishedLimits(t, sibling.Secret), "max_concurrent_per_key")

	holdSlots(t, tn.Key, held, limit)

	// The one past the limit. It names an unfaulted model, so the only thing
	// that can refuse it is the concurrency check.
	over, err := suite.client.try(http.MethodPost, "/v1/chat/completions", tn.Key, completionBody(free))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	if over.Status != http.StatusTooManyRequests {
		t.Fatalf("request %d with %d in flight answered %d, want 429\n%s",
			limit+1, limit, over.Status, truncate(over.Body))
	}
	if code := over.ErrorCode(t); code != "concurrency_exceeded" {
		t.Errorf("error.code = %q, want concurrency_exceeded", code)
	}
	// A client told only "too many requests" has to guess how long to wait, and
	// guesses badly. The specification suggests a second.
	if retry := over.Header.Get("Retry-After"); retry == "" {
		t.Error("no Retry-After on the concurrency refusal")
	}

	// Per key, not per tenant. A second integration inside one tenant must not
	// be starved by the first one's open requests.
	sib, err := suite.client.try(http.MethodPost, "/v1/chat/completions", sibling.Secret, completionBody(free))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions on the sibling key: %v", err)
	}
	if sib.Status != http.StatusOK {
		t.Errorf("the sibling key answered %d, want 200: the limit is being applied per tenant rather than per key\n%s",
			sib.Status, truncate(sib.Body))
	}
}

func TestGW13_AC6_APublishedLimitIsTheTenantsOwnAndIsEnforced(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw13-ac6")
	addMockProvider(t, tn)

	deployment := limitInt(t, publishedLimits(t, suite.dataKey), "max_request_bytes")
	const narrowed = 4096
	if deployment <= narrowed {
		t.Fatalf("the deployment publishes max_request_bytes = %d; this test needs a ceiling above %d to narrow below",
			deployment, narrowed)
	}
	narrowLimits(t, tn.ID, map[string]any{"max_request_bytes": narrowed})

	// Two tenants of one gateway must be told two different things, or the block
	// is a copy of the configuration file rather than a statement about the
	// caller reading it.
	if got := limitInt(t, publishedLimits(t, tn.Key), "max_request_bytes"); got != narrowed {
		t.Fatalf("the narrowed tenant is told max_request_bytes = %d, want %d", got, narrowed)
	}
	if again := limitInt(t, publishedLimits(t, suite.dataKey), "max_request_bytes"); again != deployment {
		t.Errorf("narrowing one tenant changed what another is told: %d, want %d", again, deployment)
	}

	// And the figure it publishes is the figure it rejects at, on both sides.
	over := suite.client.do(t, http.MethodPost, "/v1/chat/completions", tn.Key,
		paddedRequest("mock-chat-a", narrowed+1))
	if over.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("a body of %d bytes answered %d, want 413\n%s",
			narrowed+1, over.Status, truncate(over.Body))
	}
	under := suite.client.do(t, http.MethodPost, "/v1/chat/completions", tn.Key,
		paddedRequest("mock-chat-a", narrowed))
	if under.Status == http.StatusRequestEntityTooLarge {
		t.Errorf("a body of exactly %d bytes was rejected as too large; the enforced limit is below the published one",
			narrowed)
	}
}

func TestGW13_AC7_TheFour429CodesAreNeverConflated(t *testing.T) {
	begin(t)
	// Two of the four exist only when the gateway is rejecting rather than
	// observing, and a run that produced two codes and called them four would be
	// asserting nothing about conflation.
	requireEnforcement(t, true)

	// Ordered rather than a map, so a clash is always reported against the same
	// pair whichever run finds it.
	triggers := []struct {
		what string
		want string
		got  string
	}{
		{"the rate limit", "rate_limited", rateLimitedCode(t)},
		{"the in-flight cap", "concurrency_exceeded", concurrencyCode(t)},
		{"a token quota", "quota_exceeded", quotaCode(t, "tokens", tokenCap(10, 80))},
		{"a spend budget", "budget_exceeded", quotaCode(t, "cost", costCap(0.0001, 80))},
	}

	seen := map[string]string{}
	for _, tr := range triggers {
		if tr.got != tr.want {
			t.Errorf("%s produced error.code = %q, want %q", tr.what, tr.got, tr.want)
		}
		// The criterion is not only that each code is right but that no two
		// triggers share one: a caller that has run out of money for the month
		// and one that has too many requests open right now have entirely
		// different things to do about it.
		if other, clash := seen[tr.got]; clash {
			t.Errorf("%s and %s both answered %q, so a caller cannot tell them apart",
				tr.what, other, tr.got)
		}
		seen[tr.got] = tr.what
	}
}

// --- GW-13 helpers ----------------------------------------------------------

// completionBody is chat's request without chat's assertions, for a call issued
// off the test goroutine where t.Fatalf is not allowed.
func completionBody(model string) map[string]any {
	return map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
}

// holdSlots opens n completions against a model that will not answer, and
// returns once the mock has seen every one of them.
//
// The requests end on their own when the tenant's request budget runs out, which
// is why every caller narrows that budget first. The wait is registered as
// cleanup rather than deferred, so the assertions the caller came to make happen
// while the slots are still held.
func holdSlots(t *testing.T, key, model string, n int) {
	t.Helper()

	injectFault(t, model, mockprovider.FaultTimeout, mockprovider.ForeverCount)
	before := upstreamCalls(t, model)

	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Deliberately unchecked: this request exists to occupy a slot, and
			// how it ends is the subject of no criterion. Reporting on it from
			// here is not possible anyway — t.Fatalf outside the test goroutine
			// is an error in itself.
			_, _ = suite.client.try(http.MethodPost, "/v1/chat/completions", key, completionBody(model))
		}()
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		reached := upstreamCalls(t, model) - before
		if reached >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d held requests reached the upstream within 20s", reached, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rateLimitedCode trips a tenant's token bucket and reports the code it answers.
func rateLimitedCode(t *testing.T) string {
	t.Helper()

	tn := newTenant(t, "gw13-ac7-rate")
	addMockProvider(t, tn)
	// Narrowed last, and nothing may call /v1 on this tenant afterwards except
	// the two requests below: at one token per second every call spends the
	// bucket, including the ones a helper would make on the tenant's behalf.
	narrowLimits(t, tn.ID, map[string]any{"requests_per_second": 1, "burst_capacity": 1})

	if first := chat(t, tn.Key, "mock-chat-a"); first.Status == http.StatusTooManyRequests {
		t.Fatalf("the first request on a fresh bucket was already refused: %s", truncate(first.Body))
	}
	second := chat(t, tn.Key, "mock-chat-a")
	if second.Status != http.StatusTooManyRequests {
		t.Fatalf("the second request within a second answered %d, want 429\n%s",
			second.Status, truncate(second.Body))
	}
	return second.ErrorCode(t)
}

// concurrencyCode fills a key's in-flight allowance and reports the code the
// next request answers.
func concurrencyCode(t *testing.T) string {
	t.Helper()

	tn := newTenant(t, "gw13-ac7-concurrency")
	addMockProvider(t, tn)
	held := addMockModel(t, uniqueName("gw13-ac7-held"))
	refreshCatalogFor(t, tn.ID)
	awaitModel(t, tn.Key, held, true)

	narrowLimits(t, tn.ID, map[string]any{
		"max_concurrent_per_key":  1,
		"request_timeout_seconds": 10,
	})
	holdSlots(t, tn.Key, held, 1)

	resp, err := suite.client.try(http.MethodPost, "/v1/chat/completions", tn.Key,
		completionBody("mock-chat-a"))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	if resp.Status != http.StatusTooManyRequests {
		t.Fatalf("a second in-flight request answered %d, want 429\n%s", resp.Status, truncate(resp.Body))
	}
	return resp.ErrorCode(t)
}

// quotaCode installs one daily cap and reports the code the tenant is refused
// with once it is spent. hint only names the tenant.
func quotaCode(t *testing.T, hint string, cap map[string]any) string {
	t.Helper()

	tn := newTenant(t, "gw13-ac7-"+hint)
	addMockProvider(t, tn)
	// One slot and nothing else, so the code that comes back can only have come
	// from the cap this call installed. mock-chat-a is priced, which is what
	// makes a completion cost anything for a spend cap to be measured against.
	putQuota(t, tn.ID, map[string]any{"day": cap})

	rejected := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was refused for the "+hint+" cap")
	return rejected.ErrorCode(t)
}
