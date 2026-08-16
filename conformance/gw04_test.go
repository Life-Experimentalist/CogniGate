package conformance

import (
	"math"
	"net/http"
	"testing"
	"time"
)

// GW-4: consumption is measured against caps, rejected at admission before an
// upstream call is made, and reported back accurately enough for a caller to
// understand why it was rejected.
//
// Every test here works on a tenant of its own. Usage accumulates for the life
// of a window, so a cap left on the suite's shared tenant would follow every
// test that ran afterwards — and one that had already spent its allowance would
// answer 429 to tests that are not about quotas at all.
//
// The mock meters a fixed 18 tokens per completion, which is what makes the caps
// below arithmetic rather than guesswork.
const mockTokensPerCompletion = 18

// quotaTenant is the common setup: a tenant of its own, pointed at the same mock
// as everything else, with no quota yet.
func quotaTenant(t *testing.T, hint string) tenant {
	t.Helper()
	tn := newTenant(t, hint)
	addMockProvider(t, tn)
	return tn
}

// requireEnforcement skips a test that only means something when the gateway
// actually rejects. /v1/meta publishes which mode the target runs in, so the
// suite reads it there rather than asking the operator to keep an environment
// variable in step with the deployment. Certifying both modes takes two runs,
// which is the honest cost of a setting that changes what the gateway does.
func requireEnforcement(t *testing.T, on bool) {
	t.Helper()
	if suite.features["quota_enforcement"] == on {
		return
	}
	if on {
		t.Skip("not claimed: /v1/meta reports quota enforcement is not on; " +
			"run again against a gateway with quotas.enforcement: on")
	}
	t.Skip("not applicable: /v1/meta reports quota enforcement is on; " +
		"run again against a gateway with quotas.enforcement: observe")
}

func TestGW4_AC1_AHardCapRejectsBeforeAnyUpstreamCall(t *testing.T) {
	begin(t)
	requireEnforcement(t, true)

	tn := quotaTenant(t, "gw4-ac1")
	// Below one completion, so the first request is admitted at zero usage and
	// lands the tenant over. GW-4 accepts that overshoot explicitly: nothing is
	// pre-reserved, so the request that crosses the cap completes.
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(10, 80)})

	rejected := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was rejected for the day token cap")

	if code := rejected.ErrorCode(t); code != "quota_exceeded" {
		t.Errorf("error.code = %q, want %q\n%s", code, "quota_exceeded", truncate(rejected.Body))
	}
	if retry := rejected.Header.Get("Retry-After"); retry == "" {
		t.Error("a 429 with no Retry-After leaves the caller guessing how long to wait")
	}
	if state := rejected.Header.Get(headerQuotaState); state != quotaHardExceeded {
		t.Errorf("%s = %q, want %q", headerQuotaState, state, quotaHardExceeded)
	}

	// The point of admission control: a rejected request costs nothing upstream.
	// Counted around one further request rather than from the start of the test,
	// because the requests before the cap was reached were legitimately relayed.
	before := upstreamCalls(t, "mock-chat-a")
	again := chat(t, tn.Key, "mock-chat-a")
	if again.Status != http.StatusTooManyRequests {
		t.Fatalf("the second rejected completion: status %d, want 429\n%s",
			again.Status, truncate(again.Body))
	}
	if after := upstreamCalls(t, "mock-chat-a"); after != before {
		t.Errorf("the mock served %d call(s) for a request the gateway rejected; "+
			"a quota enforced after the upstream call has already been paid for is not a spend control",
			after-before)
	}
}

func TestGW4_AC2_CrossingTheSoftThresholdWarnsOnceAndStillSucceeds(t *testing.T) {
	begin(t)
	requireFeature(t, "webhooks")

	tn := quotaTenant(t, "gw4-ac2")
	events := newSink(t, tn.ID, "quota.threshold_crossed")

	// One completion has to cross the soft threshold, and the polling that
	// follows must not approach the hard cap. A hundred completions of headroom
	// with the threshold at one hundredth of it puts the soft boundary at exactly
	// one completion and the hard cap far out of reach.
	const cap = mockTokensPerCompletion * 100
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(cap, 1)})

	warned := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Header.Get(headerQuotaState) == quotaSoftExceeded },
		"reported the soft threshold")
	if warned.Status != http.StatusOK {
		t.Fatalf("a completion past the soft threshold: status %d, want 200 — "+
			"a soft threshold warns, it does not reject\n%s", warned.Status, truncate(warned.Body))
	}

	// Several more, well inside the cap. The gateway must not turn one crossing
	// into one webhook per request for the rest of the window, which is how an
	// alerting integration gets muted by the people it was meant to warn.
	for i := 0; i < 5; i++ {
		resp := chat(t, tn.Key, "mock-chat-a")
		if resp.Status != http.StatusOK {
			t.Fatalf("a completion inside the cap: status %d, want 200\n%s",
				resp.Status, truncate(resp.Body))
		}
		if state := resp.Header.Get(headerQuotaState); state != quotaSoftExceeded {
			t.Errorf("%s = %q on a request past the soft threshold, want %q",
				headerQuotaState, state, quotaSoftExceeded)
		}
	}

	crossings := awaitDeliveries(t, events, "quota.threshold_crossed", 1)
	if len(crossings) != 1 {
		t.Fatalf("the window produced %d quota.threshold_crossed events, want exactly 1",
			len(crossings))
	}
	if got, _ := crossings[0].data(t)["state"].(string); got != quotaSoftExceeded {
		t.Errorf("the event reports state %q, want %q", got, quotaSoftExceeded)
	}
	if crossings[0].Signature == "" {
		t.Error("the delivery carries no signature, so a receiver cannot tell it came from the gateway")
	}
}

func TestGW4_AC3_AKeyCapBindsThatKeyAndNotItsSiblings(t *testing.T) {
	begin(t)
	requireEnforcement(t, true)

	tn := quotaTenant(t, "gw4-ac3")
	capped := newDataKey(t, tn.ID, "capped")
	sibling := newDataKey(t, tn.ID, "sibling")

	// The tenant's own cap is far away, so anything that rejects here rejects
	// because of the key's cap and not its tenant's.
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(1_000_000, 80)})
	putKeyQuota(t, tn.ID, capped.ID, map[string]any{"day": tokenCap(10, 80)})

	awaitChat(t, capped.Secret, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was rejected for the key's own cap")

	// The sibling shares the tenant, and the tenant is nowhere near its cap.
	resp := chat(t, sibling.Secret, "mock-chat-a")
	if resp.Status != http.StatusOK {
		t.Fatalf("a sibling key under the same tenant: status %d, want 200 — "+
			"a key-level cap must bind the key it was written for and nothing else\n%s",
			resp.Status, truncate(resp.Body))
	}
	if state := resp.Header.Get(headerQuotaState); state != quotaOK {
		t.Errorf("%s = %q for the sibling, want %q", headerQuotaState, state, quotaOK)
	}
}

func TestGW4_AC4_UsageReflectsACompletedRequest(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw4-ac4")
	const cap = 1000
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(cap, 80)})

	if resp := chat(t, tn.Key, "mock-chat-a"); resp.Status != http.StatusOK {
		t.Fatalf("the completion whose tokens are being counted: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}

	got := awaitUsage(t, tn.Key, "day",
		func(u usageReport) bool { return u.TotalTokens > 0 },
		"the tokens of a completed request")

	if got.Requests != 1 {
		t.Errorf("usage counts %d requests, want 1", got.Requests)
	}
	if got.PromptTokens+got.CompletionTokens != got.TotalTokens {
		t.Errorf("prompt %d + completion %d != total %d",
			got.PromptTokens, got.CompletionTokens, got.TotalTokens)
	}

	slot := got.slot(t, "tenant", "day", "tokens")
	if slot.Cap != cap {
		t.Errorf("the reported cap is %v, want %v", slot.Cap, float64(cap))
	}
	if slot.Consumed != float64(got.TotalTokens) {
		t.Errorf("the limit reports %v consumed while the totals report %d tokens",
			slot.Consumed, got.TotalTokens)
	}
	// The identity a caller does arithmetic with. It holds only below the cap,
	// where remaining has not been clamped at zero — which is why this tenant's
	// cap is far larger than the one request it made.
	if sum := slot.Consumed + slot.Remaining; math.Abs(sum-slot.Cap) > 1e-9 {
		t.Errorf("consumed %v + remaining %v = %v, want the cap %v",
			slot.Consumed, slot.Remaining, sum, slot.Cap)
	}
}

func TestGW4_AC5_TheBreakdownSumsToTheTotals(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw4-ac5")
	const completions = 3
	for i := 0; i < completions; i++ {
		if resp := chat(t, tn.Key, "mock-chat-a"); resp.Status != http.StatusOK {
			t.Fatalf("completion %d: status %d, want 200\n%s", i, resp.Status, truncate(resp.Body))
		}
	}

	totals := awaitUsage(t, tn.Key, "day",
		func(u usageReport) bool { return u.Requests == completions },
		"all three completions")

	// Read after the totals, so anything the breakdown is missing is genuinely
	// missing rather than merely later.
	breakdown := usageBreakdown(t, tn.Key, "day", "model")
	if len(breakdown.Data) == 0 {
		t.Fatal("the breakdown is empty for a window whose totals are not")
	}

	var requests, tokens int64
	var cost float64
	for _, bucket := range breakdown.Data {
		requests += bucket.Requests
		tokens += bucket.TotalTokens
		cost += bucket.CostUSD
	}
	if requests != totals.Requests {
		t.Errorf("the breakdown sums to %d requests, the totals report %d", requests, totals.Requests)
	}
	if tokens != totals.TotalTokens {
		t.Errorf("the breakdown sums to %d tokens, the totals report %d", tokens, totals.TotalTokens)
	}
	if math.Abs(cost-totals.CostUSD) > 1e-9 {
		t.Errorf("the breakdown sums to $%v, the totals report $%v", cost, totals.CostUSD)
	}
}

func TestGW4_AC6_RaisingACapUnblocksWithoutARestart(t *testing.T) {
	begin(t)
	requireEnforcement(t, true)

	tn := quotaTenant(t, "gw4-ac6")
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(10, 80)})
	awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was rejected for the day token cap")

	putQuota(t, tn.ID, map[string]any{"day": tokenCap(1_000_000, 80)})

	// GW-4 gives a quota change ten seconds to take effect. awaitChat allows
	// longer, so a gateway that is merely slow is reported as having missed this
	// bound rather than as a hang with no explanation.
	elapsed := allowSeconds(t, 10*time.Second)
	resp := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusOK },
		"succeeded after the cap was raised")
	elapsed("the raised cap took effect")

	if state := resp.Header.Get(headerQuotaState); state != quotaOK {
		t.Errorf("%s = %q after the cap was raised, want %q", headerQuotaState, state, quotaOK)
	}
}

func TestGW4_AC7_ACostCapIsDistinguishableFromATokenCap(t *testing.T) {
	begin(t)
	requireEnforcement(t, true)

	tn := quotaTenant(t, "gw4-ac7")
	// A cost cap and nothing else, so the code the caller receives can only have
	// come from the slot this test is about. mock-chat-a is priced, which is what
	// makes a completion cost anything at all to compare against.
	putQuota(t, tn.ID, map[string]any{"day": costCap(0.0001, 80)})

	rejected := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was rejected for the day cost cap")

	code := rejected.ErrorCode(t)
	if code != "budget_exceeded" {
		t.Errorf("error.code = %q, want %q — a client that has hit a spend limit and one that "+
			"has hit a token limit have different things to do about it\n%s",
			code, "budget_exceeded", truncate(rejected.Body))
	}
	if code == "quota_exceeded" {
		t.Error("a cost cap answered with the token cap's code, so the two are indistinguishable")
	}
	if state := rejected.Header.Get(headerQuotaState); state != quotaHardExceeded {
		t.Errorf("%s = %q, want %q", headerQuotaState, state, quotaHardExceeded)
	}
}

func TestGW4_AC8_ObserveModeReportsWithoutRejecting(t *testing.T) {
	begin(t)
	requireEnforcement(t, false)

	tn := quotaTenant(t, "gw4-ac8")
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(10, 80)})

	over := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Header.Get(headerQuotaState) == quotaHardExceeded },
		"reported the hard cap")
	if over.Status != http.StatusOK {
		t.Fatalf("an over-cap completion in observe mode: status %d, want 200 — "+
			"observe reports what would have been rejected without rejecting it\n%s",
			over.Status, truncate(over.Body))
	}

	// Not once, but for as long as it stays over: an operator sizing caps before
	// turning enforcement on reads these headers continuously.
	next := chat(t, tn.Key, "mock-chat-a")
	if next.Status != http.StatusOK {
		t.Fatalf("a second over-cap completion in observe mode: status %d, want 200\n%s",
			next.Status, truncate(next.Body))
	}
	if state := next.Header.Get(headerQuotaState); state != quotaHardExceeded {
		t.Errorf("%s = %q, want %q", headerQuotaState, state, quotaHardExceeded)
	}
}
