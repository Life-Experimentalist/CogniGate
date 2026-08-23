package conformance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-8: observability. What an operator can see about traffic they are not
// allowed to read — one log line per request, a metric surface with fixed names,
// and events pushed to a receiver that can prove they came from the gateway.

// canaryPrompt is planted in one completion so the leak assertions have
// something to search for.
//
// Every other test in this suite sends a deliberately trivial prompt, because a
// distinctive one invites an implementation to satisfy a test by retaining it.
// This requirement is the opposite case: it is about proving the content did not
// survive, and a search for "hi" would match a log line by accident.
const canaryPrompt = "cognigate-conformance-canary-4f21b8-this-prompt-must-never-be-logged"

// chatWithPrompt sends a completion whose content the caller chooses.
func chatWithPrompt(t *testing.T, key, model, prompt string) *response {
	t.Helper()
	return suite.client.do(t, http.MethodPost, "/v1/chat/completions", key, map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
	})
}

// --- AC-1 --------------------------------------------------------------------

func TestGW8_AC1_OneCompletionWritesOneRequestLogLine(t *testing.T) {
	begin(t)
	requireLogAccess(t)

	from := markLog(t)
	resp := chatWithPrompt(t, suite.dataKey, "mock-chat-a", canaryPrompt)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}
	requestID := resp.Header.Get(headerRequestID)
	if requestID == "" {
		t.Fatalf("the response carries no %s, so no log line can be tied to it", headerRequestID)
	}

	lines := awaitLogLines(t, from, func(r map[string]any) bool {
		return r["msg"] == "request" && r["request_id"] == requestID
	})
	// Exactly one. A gateway that logs the same request from three layers gives
	// an operator three rows to reconcile and a collector three times the bill,
	// and a log volume that scales with the implementation's internals rather
	// than with its traffic is not the contract this requirement describes.
	if len(lines) != 1 {
		t.Fatalf("the completion produced %d request log lines for %s, want exactly 1",
			len(lines), requestID)
	}
	line := lines[0]

	// The header and the log line have to name the same request. That identity
	// is the whole point of publishing the id: an operator holding what a client
	// saw must be able to find the line without a second index.
	if got, _ := line["request_id"].(string); got != requestID {
		t.Errorf("the log line reports request_id %q, want the %s the client was given, %q",
			got, headerRequestID, requestID)
	}
	if got, _ := line["tenant"].(string); got != suite.tenantID {
		t.Errorf("the log line reports tenant %q, want %q", got, suite.tenantID)
	}
	if got, _ := line["model"].(string); got != "mock-chat-a" {
		t.Errorf("the log line reports model %q, want the model that served, %q", got, "mock-chat-a")
	}
	if got, _ := line["provider"].(string); got == "" {
		t.Error("the log line names no provider, so a request cannot be attributed to an upstream")
	}
	if got, _ := line["status"].(float64); got != http.StatusOK {
		t.Errorf("the log line reports status %v, want %d", line["status"], http.StatusOK)
	}
	// Token counts are what makes a log line answer a cost question. Zero on a
	// completion the upstream reported usage for means the gateway dropped them.
	for _, field := range []string{"prompt_tokens", "completion_tokens"} {
		if got, _ := line[field].(float64); got <= 0 {
			t.Errorf("the log line reports %s = %v on a served completion", field, line[field])
		}
	}

	// The remaining fields are ones the requirement fixes the presence of rather
	// than the value: a duration of zero is a legitimate reading, an absent one
	// is a line that answers nothing about a slow request. `client_request_id`,
	// `alias` and `error_code` are excluded because the specification qualifies
	// each with "when supplied", "when used" and "when any" respectively, and
	// none of the three applies here.
	for _, field := range []string{
		"ts", "level", "key_prefix", "route",
		"fallback_depth", "duration_ms", "upstream_duration_ms",
	} {
		if _, ok := line[field]; !ok {
			t.Errorf("the request log line has no %s field", field)
		}
	}
}

// --- AC-2 --------------------------------------------------------------------

func TestGW8_AC2_NoLogLineCarriesPromptContentOrAWholeKey(t *testing.T) {
	begin(t)
	requireLogAccess(t)

	from := markLog(t)
	resp := chatWithPrompt(t, suite.dataKey, "mock-chat-a", canaryPrompt)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}
	requestID := resp.Header.Get(headerRequestID)

	// Waiting for the request's own line first, so the scan below runs against a
	// log that has actually caught up rather than one that is merely empty.
	lines := awaitLogLines(t, from, func(r map[string]any) bool {
		return r["msg"] == "request" && r["request_id"] == requestID
	})

	written := rawSince(t, from)
	if strings.Contains(written, canaryPrompt) {
		t.Error("the gateway's log contains the prompt that was sent to it; " +
			"request content may not reach a durable store, and a log is one")
	}

	// The credential the request was made with, whole, is the other thing that
	// must not be there. Checked before the prefix rule below because it holds
	// however the gateway spells a prefix, or whether it publishes one at all.
	if strings.Contains(written, suite.dataKey) {
		t.Error("the gateway's log contains a whole cg-* key; a log reader would be able to replay traffic as the tenant")
	}
	if suite.cfg.AdminKey != "" && strings.Contains(written, suite.cfg.AdminKey) {
		t.Error("the gateway's log contains a whole cga-* key")
	}

	// "No more than its documented prefix" is measured against the prefix the
	// gateway itself publishes, not against a length this suite invents: the
	// specification fixes that a prefix is what appears, not how long one is.
	prefix, _ := lines[0]["key_prefix"].(string)
	if prefix == "" {
		t.Fatal("the request log line carries no key_prefix, so a line cannot be attributed to a credential")
	}
	if !strings.HasPrefix(suite.dataKey, prefix) {
		// A fingerprint rather than a leading substring is permitted, and there
		// is nothing further to measure against one.
		return
	}
	if len(suite.dataKey) > len(prefix) {
		overLong := suite.dataKey[:len(prefix)+1]
		if strings.Contains(written, overLong) {
			t.Errorf("the log carries at least %d characters of a cg-* key, more than the %d-character prefix (%q) it publishes",
				len(overLong), len(prefix), prefix)
		}
	}
}

// --- AC-3 --------------------------------------------------------------------

func TestGW8_AC3_TheScrapeEndpointParsesAndCountsRequests(t *testing.T) {
	begin(t)

	// The label set is narrowed to the two the assertion is about. `route` and
	// `code` are left free so the sum covers however the gateway split them, and
	// `tenant` is enough to keep another test's traffic out of the total.
	want := map[string]string{"tenant": suite.tenantID, "model": "mock-chat-a"}

	// Parsing is half the criterion, and metrics() fails the test when the body
	// is not the exposition format — so this call is already an assertion.
	before := suite.client.metrics(t).value("cognigate_requests_total", want)

	const completions = 3
	for i := 0; i < completions; i++ {
		if resp := chat(t, suite.dataKey, "mock-chat-a"); resp.Status != http.StatusOK {
			t.Fatalf("completion %d: status %d, want 200\n%s", i+1, resp.Status, truncate(resp.Body))
		}
	}

	after := suite.client.metrics(t)
	if got := after.value("cognigate_requests_total", want) - before; got != completions {
		t.Errorf("cognigate_requests_total{tenant=%q,model=%q} rose by %v over %d completions, want %d\nexposed metrics: %v",
			suite.tenantID, "mock-chat-a", got, completions, completions, after.names())
	}

	// Two more of the required series, checked here because a served completion
	// writes both unconditionally and their absence is the same defect. The
	// histogram is looked up by its _count child: a histogram is exposed as its
	// buckets, sum and count, and never under its bare name.
	if !after.has("cognigate_tokens_total", map[string]string{"tenant": suite.tenantID}) {
		t.Errorf("no cognigate_tokens_total series for tenant %q after %d completions\nexposed metrics: %v",
			suite.tenantID, completions, after.names())
	}
	if !after.has("cognigate_request_duration_seconds_count", map[string]string{"tenant": suite.tenantID}) {
		t.Errorf("no cognigate_request_duration_seconds histogram for tenant %q\nexposed metrics: %v",
			suite.tenantID, after.names())
	}
}

// --- AC-4 --------------------------------------------------------------------

func TestGW8_AC4_CascadesAndBreakerTransitionsAreMetered(t *testing.T) {
	begin(t)

	// Two halves of one criterion, kept as subtests so the cascade half can skip
	// on a gateway that does not claim fallback chains without taking the
	// breaker half — which every gateway owes — down with it.
	t.Run("cascade", func(t *testing.T) {
		requireFeature(t, "fallback_chains")

		primary := addMockModel(t, uniqueName("gw8-ac4-cascade"))
		putRoute(t, suite.tenantID, primary, primary, "mock-chat-a")
		injectFault(t, primary, mockprovider.FaultServerError, mockprovider.ForeverCount)

		want := map[string]string{"from_model": primary, "to_model": "mock-chat-a"}
		before := suite.client.metrics(t).value("cognigate_fallback_cascades_total", want)

		resp := chat(t, suite.dataKey, primary)
		if resp.Status != http.StatusOK {
			t.Fatalf("a completion whose first chain entry is failing: status %d, want 200\n%s",
				resp.Status, truncate(resp.Body))
		}
		if depth := resp.Header.Get(headerFallbackDepth); depth != "1" {
			t.Fatalf("%s = %q, want %q — without a cascade there is nothing for the counter to have recorded",
				headerFallbackDepth, depth, "1")
		}

		after := suite.client.metrics(t)
		if got := after.value("cognigate_fallback_cascades_total", want) - before; got != 1 {
			t.Errorf("cognigate_fallback_cascades_total{from_model=%q,to_model=%q} rose by %v over one cascade, want 1\nexposed metrics: %v",
				primary, "mock-chat-a", got, after.names())
		}
	})

	t.Run("breaker", func(t *testing.T) {
		// The model has to exist before the tenant's first catalogue poll, which
		// is triggered by registering its provider.
		model := addMockModel(t, uniqueName("gw8-ac4-breaker"))
		tn := newTenant(t, "gw8-ac4-breaker")
		addMockProvider(t, tn)
		awaitModel(t, tn.Key, model, true)

		injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)
		// The default threshold is five failures inside the window; a sixth costs
		// nothing and covers a deployment that counts differently.
		for i := 0; i < 6; i++ {
			chat(t, tn.Key, model)
		}
		awaitHealth(t, tn.Key, func(report map[string]any) bool {
			row, ok := providerHealthRow(report, "mock")
			return ok && row["breaker"] == "open"
		}, `providers[mock].breaker "open"`)

		// Only the two labels the specification names. A gateway that also keys
		// the series by tenant is conformant — the label sets are minimums — and
		// constraining one this suite is not owed would fail it for that. The
		// model id is unique to this test, so the pair identifies one series.
		want := map[string]string{"provider": "mock", "model": model}
		scraped := suite.client.metrics(t)
		if !scraped.has("cognigate_breaker_state", want) {
			t.Fatalf("no cognigate_breaker_state series for provider %q model %q, whose breaker the health report says is open\nexposed metrics: %v",
				"mock", model, scraped.names())
		}
		// 2 is "open" in the encoding the specification fixes: 0 closed, 1
		// half-open, 2 open, so that a threshold alert fires on severity.
		if got := scraped.value("cognigate_breaker_state", want); got != 2 {
			t.Errorf("cognigate_breaker_state{provider=%q,model=%q} = %v, want 2 (open)",
				"mock", model, got)
		}
	})
}

// --- AC-5 --------------------------------------------------------------------

func TestGW8_AC5_AnEventIsDeliveredWithASignatureOverItsBody(t *testing.T) {
	begin(t)
	requireFeature(t, "webhooks")

	model := addMockModel(t, uniqueName("gw8-ac5"))
	tn := newTenant(t, "gw8-ac5")
	addMockProvider(t, tn)
	awaitModel(t, tn.Key, model, true)

	receiver := newSink(t, tn.ID, "breaker.opened")

	injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)
	for i := 0; i < 6; i++ {
		chat(t, tn.Key, model)
	}
	// The clock starts at the transition, which is the moment the bound is
	// measured from.
	within := allowSeconds(t, 30*time.Second)

	opened := awaitDeliveries(t, receiver, "breaker.opened", 1)
	within("the breaker.opened webhook arrived")
	if len(opened) < 1 {
		t.Fatal("no breaker.opened event was delivered")
	}
	d := opened[0]

	if d.EventID == "" {
		t.Error("the delivery carries no event id header, so a receiver cannot deduplicate a retry")
	}
	if !strings.HasPrefix(d.Signature, "sha256=") {
		t.Fatalf("the signature is %q, want the form sha256=<hex>", d.Signature)
	}
	// Recomputed over the bytes that arrived, not over a re-encoding of the
	// parsed body: a receiver verifying anything else would reject a payload
	// whose key order or spacing differed from its own encoder's.
	if want := signWebhook(receiver.secret, d.Body); !hmac.Equal([]byte(d.Signature), []byte(want)) {
		t.Errorf("the delivery is signed %q; HMAC-SHA256 over the delivered bytes with the registered secret gives %q\n%s",
			d.Signature, want, truncate(d.Body))
	}

	// The envelope's own id and the header have to agree, or a receiver
	// deduplicating on one and logging the other cannot be audited.
	var envelope struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created any    `json:"created"`
		Tenant  string `json:"tenant"`
	}
	if err := json.Unmarshal(d.Body, &envelope); err != nil {
		t.Fatalf("the delivered body is not JSON: %v\n%s", err, truncate(d.Body))
	}
	if envelope.ID != d.EventID {
		t.Errorf("the body's id is %q and the header's is %q; they name one event and must agree",
			envelope.ID, d.EventID)
	}
	if envelope.Type != "breaker.opened" {
		t.Errorf("the body's type is %q, want %q", envelope.Type, "breaker.opened")
	}
	if envelope.Tenant != tn.ID {
		t.Errorf("the body names tenant %q, want %q", envelope.Tenant, tn.ID)
	}
	if envelope.Created == nil {
		t.Error("the body has no created field, so a receiver cannot order two events")
	}
}

// --- AC-6 --------------------------------------------------------------------

func TestGW8_AC6_ARefusedDeliveryIsRetriedAndArrivesOnce(t *testing.T) {
	begin(t)
	requireFeature(t, "webhooks")

	model := addMockModel(t, uniqueName("gw8-ac6"))
	tn := newTenant(t, "gw8-ac6")
	addMockProvider(t, tn)
	awaitModel(t, tn.Key, model, true)

	receiver := newSink(t, tn.ID, "breaker.opened")
	// Armed before the transition. A fault installed afterwards would catch a
	// redelivery rather than the first attempt, and prove nothing about retry.
	receiver.rejectNext(t, http.StatusInternalServerError, 2)

	injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)
	for i := 0; i < 6; i++ {
		chat(t, tn.Key, model)
	}

	// Two refusals cost the gateway its first two backoffs — five seconds and
	// then ten — so the accepted attempt cannot land inside the patience a
	// single attempt is given. Naming a longer one here is the point of the
	// call; the default would time out on a conformant gateway.
	delivered := awaitDeliveriesWithin(t, receiver, "breaker.opened", 1, 45*time.Second)
	if len(delivered) != 1 {
		t.Fatalf("the receiver accepted %d breaker.opened deliveries, want exactly 1 — "+
			"at-least-once delivery is only usable if the duplicates are the same event, not several",
			len(delivered))
	}

	attempts := attemptsOfType(receiver.read(t), "breaker.opened")
	if len(attempts) < 3 {
		t.Fatalf("the receiver saw %d attempt(s) after refusing two; the gateway gave up rather than retrying",
			len(attempts))
	}

	id := delivered[0].EventID
	if id == "" {
		t.Fatal("the accepted delivery carries no event id, so a receiver has nothing to deduplicate on")
	}
	// Stable across retries is what makes at-least-once safe: a receiver that
	// took the third attempt has to be able to tell it was the first two over
	// again, and a fresh id on every attempt makes one event look like three.
	for _, a := range attempts {
		if a.EventID != id {
			t.Errorf("the attempt the receiver answered %d carries event id %q, want %q",
				a.Status, a.EventID, id)
		}
	}
}

// --- AC-7 --------------------------------------------------------------------

func TestGW8_AC7_ASoftThresholdCrossingRaisesOneEvent(t *testing.T) {
	begin(t)
	requireFeature(t, "quotas")
	requireFeature(t, "webhooks")

	tn := quotaTenant(t, "gw8-ac7")
	crossings := newSink(t, tn.ID, "quota.threshold_crossed")

	// The soft boundary has to fall at one completion and the hard cap has to
	// stay out of reach, so the requests that follow the crossing are inside the
	// cap and the only thing that could produce a second event is the gateway
	// re-announcing the same crossing.
	const cap = mockTokensPerCompletion * 100
	putQuota(t, tn.ID, map[string]any{"day": tokenCap(cap, 1)})

	awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Header.Get(headerQuotaState) == quotaSoftExceeded },
		"reported the soft threshold")

	for i := 0; i < 5; i++ {
		if resp := chat(t, tn.Key, "mock-chat-a"); resp.Status != http.StatusOK {
			t.Fatalf("a completion inside the cap: status %d, want 200\n%s",
				resp.Status, truncate(resp.Body))
		}
	}

	got := awaitDeliveries(t, crossings, "quota.threshold_crossed", 1)
	// One per window, not one per request past it. An integration that pages on
	// this event is muted by whoever silences it after the hundredth copy.
	if len(got) != 1 {
		t.Fatalf("the window produced %d quota.threshold_crossed events, want exactly 1", len(got))
	}

	// The payload has to say which limit moved, or an alert can only report that
	// some budget somewhere is close.
	data := got[0].data(t)
	if window, _ := data["window"].(string); window != "day" {
		t.Errorf("the event reports window %q, want %q", window, "day")
	}
	if state, _ := data["state"].(string); state != quotaSoftExceeded {
		t.Errorf("the event reports state %q, want %q", state, quotaSoftExceeded)
	}
}

// --- AC-8 --------------------------------------------------------------------

func TestGW8_AC8_TheAnalyticsEngineExposesAScrape(t *testing.T) {
	begin(t)

	if suite.cfg.AnalyticsURL == "" {
		t.Skip("set CONF_ANALYTICS_URL to the analytics engine's origin to check its scrape; " +
			"a deployment may run the gateway alone, and an address the suite is not told " +
			"cannot be derived from the gateway's")
	}

	// Unauthenticated on purpose. GW-8 says the endpoint "MUST NOT require a
	// key by default (scrapers are not tenants)", and scrapeURL sends no
	// credential -- so a 401 here fails the criterion rather than skipping it.
	// Parsing is the other half: scrapeURL fails the test when the body is not
	// the exposition format, which is what separates this from actuator's own
	// JSON, a document no Prometheus server reads.
	scrape := scrapeURL(t, suite.cfg.AnalyticsURL+"/metrics")

	// One series is the bar, and it is deliberately low. Which series the
	// analytics engine exposes is its own business -- the specification fixes
	// the gateway's names, not this process's -- but an endpoint that parses
	// while exposing nothing is indistinguishable from one that is not wired up.
	if len(scrape.samples) == 0 {
		t.Error("the analytics engine's /metrics parsed but exposed no series; " +
			"an empty scrape reports nothing about the process")
	}
}

// --- helpers -----------------------------------------------------------------

// signWebhook recomputes the header the specification fixes: the literal
// "sha256=" followed by the hex HMAC-SHA256 of the raw body, keyed by the secret
// the webhook was registered with.
func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// attemptsOfType is deliveriesOfType without its accepted filter, because this
// requirement is about the attempts a receiver refused as much as the one it
// finally took.
func attemptsOfType(all []delivery, eventType string) []delivery {
	var out []delivery
	for _, d := range all {
		if d.Type == eventType {
			out = append(out, d)
		}
	}
	return out
}
