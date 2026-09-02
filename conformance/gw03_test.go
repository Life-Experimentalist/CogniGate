package conformance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-3: a fallback chain is tried in order when an upstream fails, and is not
// tried at all when the caller is the one who failed.

func TestGW3_AC1_AChainCannotNameTheSameModelTwice(t *testing.T) {
	begin(t)

	resp := tryPutRoute(t, suite.tenantID, uniqueName("gw3-ac1"),
		[]string{"mock-chat-a", "mock-chat-b", "mock-chat-a"})
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("PUT a chain naming a model twice: status %d, want 400\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "fallback_duplicate_model" {
		t.Errorf("error.code = %q, want %q\n%s", code, "fallback_duplicate_model", truncate(resp.Body))
	}
}

func TestGW3_AC2_AFailingUpstreamCascadesToTheNextEntry(t *testing.T) {
	begin(t)

	primary := addMockModel(t, uniqueName("gw3-ac2"))
	putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
	injectFault(t, primary, mockprovider.FaultServerError, mockprovider.ForeverCount)

	resp := chat(t, suite.dataKey, primary)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion whose first chain entry is failing: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	if served := servedModel(t, resp); served != "mock-chat-a" {
		t.Errorf("%s names %q, want the chain's second entry %q", headerServedBy, served, "mock-chat-a")
	}
	if depth := resp.Header.Get(headerFallbackDepth); depth != "1" {
		t.Errorf("%s = %q, want %q — the caller has to be able to tell a fallback happened",
			headerFallbackDepth, depth, "1")
	}
	if model, _ := resp.JSON(t)["model"].(string); model != "mock-chat-a" {
		t.Errorf("the response body names model %q, want the model that answered %q", model, "mock-chat-a")
	}
}

func TestGW3_AC3_EveryKeyInAPoolIsTriedBeforeTheChainMovesOn(t *testing.T) {
	begin(t)

	primary := addMockModel(t, uniqueName("gw3-ac3"))
	putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
	// Rate limiting is the one upstream failure that is about the credential
	// rather than about the model, so it is the failure a second pooled key can
	// still answer.
	injectFault(t, primary, mockprovider.FaultRateLimit, mockprovider.ForeverCount)

	before := mockState(t)
	resp := chat(t, suite.dataKey, primary)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion whose first chain entry is rate limited: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	if served := servedModel(t, resp); served != "mock-chat-a" {
		t.Errorf("%s names %q, want the chain's second entry %q", headerServedBy, served, "mock-chat-a")
	}

	// Fingerprints, never the key material: the mock reports which credentials it
	// saw, and a test that named them would be a test that wrote them down.
	if used := keysSince(t, before); len(used) < 2 {
		t.Errorf("the request reached the provider with %d distinct credential(s) (%v); "+
			"a pooled key was left untried before the chain moved on", len(used), used)
	}
}

func TestGW3_AC4_ARequestCausedFailureDoesNotCascade(t *testing.T) {
	begin(t)

	primary := addMockModel(t, uniqueName("gw3-ac4"))
	putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
	injectFault(t, primary, mockprovider.FaultClientError, mockprovider.ForeverCount)

	before := upstreamCalls(t, "mock-chat-a")
	resp := chat(t, suite.dataKey, primary)
	// Cascading here would charge the caller once per chain entry for a request
	// that was never going to succeed anywhere.
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("a completion the upstream rejected as malformed: status %d, want 400\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "invalid_request" {
		t.Errorf("error.code = %q, want %q\n%s", code, "invalid_request", truncate(resp.Body))
	}
	if after := upstreamCalls(t, "mock-chat-a"); after != before {
		t.Errorf("the fallback entry was dialled %d time(s) after a request-caused failure", after-before)
	}
}

func TestGW3_AC5_AnExhaustedChainReportsEveryAttempt(t *testing.T) {
	begin(t)

	first := addMockModel(t, uniqueName("gw3-ac5a"))
	second := addMockModel(t, uniqueName("gw3-ac5b"))
	putRoute(t, suite.tenantID, first, first, second)
	injectFault(t, first, mockprovider.FaultServerError, mockprovider.ForeverCount)
	injectFault(t, second, mockprovider.FaultServerError, mockprovider.ForeverCount)

	resp := chat(t, suite.dataKey, first)
	if resp.Status != http.StatusBadGateway {
		t.Fatalf("a completion whose whole chain is failing: status %d, want 502\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "upstream_exhausted" {
		t.Errorf("error.code = %q, want %q\n%s", code, "upstream_exhausted", truncate(resp.Body))
	}

	envelope, _ := resp.JSON(t)["error"].(map[string]any)
	attempts, _ := envelope["attempts"].([]any)
	if len(attempts) < 2 {
		t.Fatalf("error.attempts describes %d attempt(s), want one per chain entry\n%s",
			len(attempts), truncate(resp.Body))
	}
	for i, raw := range attempts {
		attempt, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("error.attempts[%d] is not an object\n%s", i, truncate(resp.Body))
		}
		for _, field := range []string{"provider", "model", "failure"} {
			if s, _ := attempt[field].(string); s == "" {
				t.Errorf("error.attempts[%d]: %s is %v, want a non-empty string", i, field, attempt[field])
			}
		}
	}

	// The diagnostic names providers and models. It must not name credentials.
	if strings.Contains(string(resp.Body), "mock-key") {
		t.Errorf("the failure envelope carries provider key material\n%s", truncate(resp.Body))
	}
}

func TestGW3_AC6_AnOpenBreakerIsSkippedWithoutDiallingTheProvider(t *testing.T) {
	begin(t)

	primary := addMockModel(t, uniqueName("gw3-ac6"))
	injectFault(t, primary, mockprovider.FaultServerError, mockprovider.ForeverCount)

	// Trip the breaker before the route exists, so these failures are attributed
	// to the model rather than absorbed by a fallback. The default threshold is
	// five failures inside the window; a sixth costs nothing and covers a
	// deployment that counts differently.
	for i := 0; i < 6; i++ {
		chat(t, suite.dataKey, primary)
	}
	awaitHealth(t, suite.dataKey, func(report map[string]any) bool {
		row, ok := providerHealthRow(report, "mock")
		if !ok {
			return false
		}
		breakers, _ := row["breakers"].([]any)
		for _, raw := range breakers {
			b, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if m, _ := b["model"].(string); m == primary {
				return b["breaker"] == "open"
			}
		}
		return false
	}, "providers[mock].breakers[] reporting "+primary+" as open")

	// With the fault cleared, the provider would answer this model if it were
	// asked. The only thing left that can stop the gateway asking is the breaker,
	// which is what makes the untouched call counter below mean something.
	mockControl(t, http.MethodPost, "/_control/faults", map[string]any{
		"model": primary, "mode": mockprovider.FaultNone,
	})

	putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
	before := upstreamCalls(t, primary)

	resp := chat(t, suite.dataKey, primary)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion whose first chain entry is broken out: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	if served := servedModel(t, resp); served != "mock-chat-a" {
		t.Errorf("%s names %q, want the chain's second entry %q", headerServedBy, served, "mock-chat-a")
	}
	if after := upstreamCalls(t, primary); after != before {
		t.Errorf("the gateway dialled the broken-out model %d time(s); an open breaker is supposed to "+
			"cost nothing upstream", after-before)
	}
	// A skipped entry still occupies its position in the chain, so the caller is
	// told the answer came from a fallback.
	if depth := resp.Header.Get(headerFallbackDepth); depth != "1" {
		t.Errorf("%s = %q, want %q", headerFallbackDepth, depth, "1")
	}
}

func TestGW3_AC7_AStreamThatDiesPartWayThroughSaysSo(t *testing.T) {
	begin(t)

	primary := addMockModel(t, uniqueName("gw3-ac7"))
	putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
	// The abort lands after the first chunk has been delivered, which is the case
	// a fallback cannot rescue: bytes claiming to be one model's answer have
	// already reached the client.
	injectFault(t, primary, mockprovider.FaultStreamAbort, mockprovider.ForeverCount)

	stream := chatStream(t, suite.dataKey, primary)
	if stream.Status != http.StatusOK {
		t.Fatalf("a streaming completion: status %d, want 200 — the stream never began", stream.Status)
	}
	if !stream.hasFrameContaining("upstream_error") {
		t.Errorf("the stream ended without a terminal error event, so a truncated answer is "+
			"indistinguishable from a complete one. Frames: %v", stream.Frames)
	}
	// Switching models mid-stream would splice two different answers together.
	if stream.hasFrameContaining("mock-chat-a") {
		t.Errorf("the stream fell back to the chain's next entry after it had already emitted "+
			"content. Frames: %v", stream.Frames)
	}
}

func TestGW3_AC8_AChainThatCollapsesAfterResolutionIsReportedAndStillServes(t *testing.T) {
	begin(t)

	target := addMockModel(t, uniqueName("gw3-ac8"))
	alias := uniqueName("gw3alias")

	// Two distinct names at write time, so the chain is accepted, pointing at two
	// different models. Nothing here is a duplicate yet.
	putAlias(t, suite.tenantID, alias, map[string]any{"pin": "mock-chat-b"})
	putRoute(t, suite.tenantID, target, alias, target)
	awaitHealth(t, suite.dataKey, func(report map[string]any) bool {
		row, ok := nameState(report, "rules", target)
		return ok && row["state"] == "ok"
	}, "rules[] naming "+target+" as ok before the alias is re-pinned")

	// Re-pointing the alias collapses both chain positions onto one model. The
	// chain itself is untouched, so no write-time check can see this.
	putAlias(t, suite.tenantID, alias, map[string]any{"pin": target})
	awaitHealth(t, suite.dataKey, func(report map[string]any) bool {
		row, ok := nameState(report, "rules", target)
		return ok && row["state"] == "degraded" && row["reason"] == "fallback_duplicate_model"
	}, "rules[] naming "+target+" as degraded with reason fallback_duplicate_model")

	// Degraded is not broken: the rule still has one live position, and a request
	// against it is answered rather than refused.
	resp := chat(t, suite.dataKey, target)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion against the collapsed rule: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	if served := servedModel(t, resp); served != target {
		t.Errorf("%s names %q, want %q", headerServedBy, served, target)
	}
	if depth := resp.Header.Get(headerFallbackDepth); depth != "0" {
		t.Errorf("%s = %q, want %q — the collapsed position is dropped, not retried",
			headerFallbackDepth, depth, "0")
	}
}
