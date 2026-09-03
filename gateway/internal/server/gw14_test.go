package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/events"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// --- fixtures ---------------------------------------------------------------

// sentinel appears in no fixture, template or error message anywhere else in
// this package, so finding it in something the gateway wrote down is proof it
// came from a request body rather than from something that merely looks like
// one.
const sentinel = "sentinel-8f2b1c-do-not-record"

// sentinelRequest is an ordinary completion carrying the sentinel where a user's
// prompt would be.
func sentinelRequest() map[string]any {
	return map[string]any{
		"model":    "test-small",
		"messages": []map[string]string{{"role": "user", "content": sentinel}},
	}
}

// captureOn is the policy an operator sets to open the window. Sampling at 1.0
// is what makes these tests deterministic; it is also the setting GW-14 requires
// the API to warn about, which is asserted separately.
func captureOn(ttl time.Duration) map[string]any {
	return map[string]any{"debug_capture": map[string]any{
		"enabled":     true,
		"ttl_seconds": int(ttl.Seconds()),
		"sample_rate": 1.0,
	}}
}

// enableCapture turns capture on for a tenant and hands back the admin response,
// which is where GW-14's warning field lives.
func (h *harness) enableCapture(tenant tenantFixture, ttl time.Duration) reply {
	h.t.Helper()
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey, captureOn(ttl))
	if res.status != http.StatusOK {
		h.t.Fatalf("enabling capture: status %d, body %s", res.status, res.body)
	}
	return res
}

// captureList is the admin plane's view of what has been retained.
//
// Request and Response are []byte here because they are []byte there: JSON has
// no byte string, so the endpoint sends base64 and this decodes it. That is
// deliberate on both ends — a capture holds the bytes as they arrived, and a
// body that is not valid JSON is often exactly the one being investigated.
type captureList struct {
	Data []struct {
		ID        string    `json:"id"`
		RequestID string    `json:"request_id"`
		Status    int       `json:"status"`
		ExpiresAt time.Time `json:"expires_at"`
		Request   []byte    `json:"request"`
		Response  []byte    `json:"response"`
	} `json:"data"`
}

func (h *harness) captures(tenant tenantFixture) captureList {
	h.t.Helper()
	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/captures", tenant.adminKey, nil)
	if res.status != http.StatusOK {
		h.t.Fatalf("listing captures: status %d, body %s", res.status, res.body)
	}
	var list captureList
	res.decode(h.t, &list)
	return list
}

// carriesSentinel reports whether anything in v's JSON form holds the sentinel.
// Marshalling rather than reaching for named fields is the point: the claim
// under test is about the whole record, including fields added later.
func carriesSentinel(t *testing.T, v any) bool {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %T: %v", v, err)
	}
	return strings.Contains(string(raw), sentinel)
}

// --- AC-1: the content ban --------------------------------------------------

// The sentinel travels through a complete request and must not surface in
// anything the gateway retains for its own purposes. The conformance suite
// repeats this against a running deployment, where stdout and webhook
// deliveries are also readable; here the reachable surfaces are the usage
// records, the events, the audit log and /metrics.
func TestNoContentReachesAnythingTheGatewayKeeps(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamOK(10, 5), nil
	}

	if res := h.chat(tenant, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}
	h.flushTelemetry()

	for _, rec := range h.usage.all() {
		if carriesSentinel(t, rec) {
			t.Error("a usage record carries prompt content")
		}
	}
	for _, ev := range h.events.emitted {
		if carriesSentinel(t, ev.data) {
			t.Errorf("event %q carries prompt content", ev.typ)
		}
	}
	for _, path := range []string{
		"/metrics",
		"/admin/v1/audit",
		"/admin/v1/tenants/" + tenant.id + "/events",
		"/admin/v1/tenants/" + tenant.id + "/usage",
		"/admin/v1/tenants/" + tenant.id + "/usage/breakdown",
	} {
		res := h.do(http.MethodGet, path, testBootstrapKey, nil)
		if strings.Contains(string(res.body), sentinel) {
			t.Errorf("%s carries prompt content", path)
		}
	}
}

// --- AC-2: off by default ---------------------------------------------------

// A gateway nobody has configured retains nothing, and the header that would
// say otherwise is absent.
func TestCaptureIsOffAndSilentByDefault(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	res := h.chat(tenant, sentinelRequest(), nil)
	if res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderDebugCapture); got != "" {
		t.Errorf("X-CogniGate-Debug-Capture = %q with capture off; want absent", got)
	}
	if list := h.captures(tenant); len(list.Data) != 0 {
		t.Fatalf("%d captures retained with capture off", len(list.Data))
	}
}

// --- AC-3 and AC-6: the exception, and what it retains -----------------------

// With capture on, the bodies are retrievable from the admin plane and every
// data-plane response says retention is active.
func TestEnabledCaptureRetainsAndLabels(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)

	res := h.chat(tenant, sentinelRequest(), nil)
	if res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderDebugCapture); got != debugCaptureOn {
		t.Errorf("X-CogniGate-Debug-Capture = %q, want %q", got, debugCaptureOn)
	}

	list := h.captures(tenant)
	if len(list.Data) != 1 {
		t.Fatalf("%d captures, want 1", len(list.Data))
	}
	got := list.Data[0]
	if !strings.Contains(string(got.Request), sentinel) {
		t.Errorf("the captured request is not what was sent:\n%s", got.Request)
	}
	if len(got.Response) == 0 {
		t.Error("the capture kept no response body")
	}
	if got.Status != http.StatusOK {
		t.Errorf("capture status = %d, want 200", got.Status)
	}
	if got.RequestID == "" {
		t.Error("the capture carries no request id, so it cannot be tied to a log line")
	}
}

// The header is owed on every response, not only the ones a handler completed —
// and a failure is exactly when someone reads it.
func TestCaptureCoversRefusalsToo(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)

	req := sentinelRequest()
	req["model"] = "no-such-model"
	res := h.chat(tenant, req, nil)
	if res.status == http.StatusOK {
		t.Fatalf("a request for an unknown model succeeded: %s", res.body)
	}
	if got := res.header.Get(httpx.HeaderDebugCapture); got != debugCaptureOn {
		t.Errorf("X-CogniGate-Debug-Capture = %q on a refusal, want %q", got, debugCaptureOn)
	}

	list := h.captures(tenant)
	if len(list.Data) != 1 {
		t.Fatalf("%d captures for a refused request, want 1 — a failure is what capture is for",
			len(list.Data))
	}
	if list.Data[0].Status == http.StatusOK {
		t.Error("the capture recorded a refusal as a success")
	}
}

// "Every response" has to include the ones no handler runs for. A 429 from the
// tenant limiter is the response someone who turned capture on to investigate
// their rate limiting most wants to see, and it is produced upstream of every
// route — so capture has to sit ahead of the limiter, not behind it.
func TestALimiterRefusalIsLabelledAndCaptured(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)
	patchLimits(t, h, tenant.id, map[string]any{"requests_per_second": 1, "burst_capacity": 1})

	if res := h.chat(tenant, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("the first completion was refused: status %d, body %s", res.status, res.body)
	}
	res := h.chat(tenant, sentinelRequest(), nil)
	h.expectError(res, http.StatusTooManyRequests, apierr.CodeRateLimited)
	if got := res.header.Get(httpx.HeaderDebugCapture); got != debugCaptureOn {
		t.Errorf("X-CogniGate-Debug-Capture = %q on a rate-limit refusal, want %q",
			got, debugCaptureOn)
	}

	list := h.captures(tenant)
	if len(list.Data) != 2 {
		t.Fatalf("%d captures, want 2 — the refused request is the interesting one", len(list.Data))
	}
	if list.Data[0].Status != http.StatusTooManyRequests {
		t.Errorf("the newest capture has status %d, want 429", list.Data[0].Status)
	}
}

// The header is owed on every data-plane response, but a capture is only worth
// keeping for a request that carried something. Retaining catalogue reads would
// spend a tenant's byte budget evicting the completions capture was turned on
// for — and a client polling /v1/models would do it within seconds.
func TestBodylessReadsAreLabelledButNotCaptured(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)

	for _, path := range []string{"/v1/models", "/v1/meta", "/v1/health", "/v1/usage"} {
		res := h.do(http.MethodGet, path, tenant.dataKey, nil)
		if res.status != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", path, res.status, res.body)
		}
		if got := res.header.Get(httpx.HeaderDebugCapture); got != debugCaptureOn {
			t.Errorf("X-CogniGate-Debug-Capture = %q on GET %s, want %q",
				got, path, debugCaptureOn)
		}
	}

	if list := h.captures(tenant); len(list.Data) != 0 {
		t.Fatalf("%d captures after four bodyless reads, want 0", len(list.Data))
	}
}

// A streamed response is never buffered to be captured: doing so would change
// how the gateway served the very request it was capturing. The request is
// still recorded, and the header still says retention is on, because it is.
func TestStreamsAreLabelledAndKeepNoResponseBody(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)
	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamStream(), nil
	}

	req := sentinelRequest()
	req["stream"] = true
	res := h.chat(tenant, req, nil)
	if res.status != http.StatusOK {
		t.Fatalf("stream: status %d, body %s", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderDebugCapture); got != debugCaptureOn {
		t.Errorf("X-CogniGate-Debug-Capture = %q on a stream, want %q", got, debugCaptureOn)
	}
	// Reading a streamed body is a drain rather than an observation, so the
	// client getting its chunks is the assertion that capture did not take them.
	if !strings.Contains(string(res.body), "pong") ||
		!strings.Contains(string(res.body), "[DONE]") {
		t.Fatalf("capture consumed the stream; the client received:\n%s", res.body)
	}
	for _, e := range h.captures(tenant).Data {
		if len(e.Response) != 0 {
			t.Errorf("a streamed response was captured: %s", e.Response)
		}
	}
}

// Tenant scoping through the API rather than the store: B's admin key must not
// reach A's captures even knowing A's id.
func TestCapturesAreNotReadableByAnotherTenant(t *testing.T) {
	h := newHarness(t)
	a := h.routeTenant("acme")
	b := h.newTenant("beta")
	h.enableCapture(a, time.Hour)

	if res := h.chat(a, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+a.id+"/captures", b.adminKey, nil)
	if res.status == http.StatusOK {
		t.Fatalf("tenant b read tenant a's captures: %s", res.body)
	}
	if strings.Contains(string(res.body), sentinel) {
		t.Fatal("the refusal leaked the content it was refusing")
	}
}

// The TTL is a deletion in both halves: unreadable the moment it passes, and
// gone from memory once the sweeper reaches it.
func TestCapturesExpireAndAreSwept(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Minute)

	if res := h.chat(tenant, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}
	if len(h.captures(tenant).Data) != 1 {
		t.Fatal("nothing was captured to expire")
	}

	after := time.Now().UTC().Add(2 * time.Minute)
	if got := h.srv.captures.List(tenant.id, after); len(got) != 0 {
		t.Errorf("%d captures still readable past the TTL", len(got))
	}
	if removed := h.srv.captures.Sweep(after); removed != 1 {
		t.Errorf("the sweeper removed %d expired captures, want 1", removed)
	}
}

// Deleting a tenant takes its content with it. Nothing could read these again in
// any case, which is exactly why they would otherwise be forgotten.
func TestDeletingATenantDropsItsCaptures(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)

	if res := h.chat(tenant, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}

	res := h.do(http.MethodDelete,
		"/admin/v1/tenants/"+tenant.id+"?confirm="+tenant.id, testBootstrapKey, nil)
	if res.status != http.StatusNoContent {
		t.Fatalf("deleting tenant: status %d, body %s", res.status, res.body)
	}
	if got := h.srv.captures.List(tenant.id, time.Now().UTC()); len(got) != 0 {
		t.Fatalf("%d captures outlived the tenant they belonged to", len(got))
	}
}

// --- AC-4 and the warning ---------------------------------------------------

// The ceiling is refused rather than clamped, and with the code that names it.
func TestTTLAboveTheCeilingIsRefused(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		captureOn(96*time.Hour))
	body := h.expectError(res, http.StatusBadRequest, "capture_ttl_too_long")
	if !strings.Contains(body.Error.Message, "72") {
		t.Errorf("the refusal does not name the ceiling: %q", body.Error.Message)
	}

	got := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id, testBootstrapKey, nil)
	var tn store.Tenant
	got.decode(t, &tn)
	if tn.DebugCapture.Enabled {
		t.Error("a refused policy was applied anyway")
	}
}

func TestSampleRateOutsideZeroToOneIsRefused(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"debug_capture": map[string]any{"enabled": true, "sample_rate": 1.5}})
	h.expectError(res, http.StatusBadRequest, "invalid_request")
}

// GW-14 requires the response to enabling sample_rate 1.0 to echo a warning.
func TestFullSamplingIsWarnedAbout(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var full struct {
		Warnings []string `json:"warnings"`
	}
	h.enableCapture(tenant, time.Hour).decode(t, &full)
	if len(full.Warnings) == 0 {
		t.Fatal("enabling sample_rate 1.0 produced no warning")
	}

	// A policy that does not deserve one does not get one: a warning on every
	// update would be read by nobody.
	var partial struct {
		Warnings []string `json:"warnings"`
	}
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"debug_capture": map[string]any{"enabled": true, "sample_rate": 0.1}})
	res.decode(t, &partial)
	if len(partial.Warnings) != 0 {
		t.Errorf("a 0.1 sample was warned about: %v", partial.Warnings)
	}
}

// --- AC-5: enabling and disabling are on the record --------------------------

func TestTogglingCaptureIsAuditedAndAnnounced(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	h.enableCapture(tenant, time.Hour)
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"debug_capture": map[string]any{"enabled": false}})
	if res.status != http.StatusOK {
		t.Fatalf("disabling capture: status %d, body %s", res.status, res.body)
	}

	var raised []string
	for _, ev := range h.events.emitted {
		if strings.HasPrefix(ev.typ, "debug_capture.") {
			raised = append(raised, ev.typ)
		}
	}
	want := []string{events.DebugCaptureEnabled, events.DebugCaptureDisabled}
	if len(raised) != 2 || raised[0] != want[0] || raised[1] != want[1] {
		t.Fatalf("events raised = %v, want %v", raised, want)
	}

	var log struct {
		Data []store.AuditEntry `json:"data"`
	}
	h.do(http.MethodGet, "/admin/v1/audit", testBootstrapKey, nil).decode(t, &log)
	updates := 0
	for _, e := range log.Data {
		if e.Action == "update" && e.Resource == "/admin/v1/tenants/"+tenant.id {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("%d tenant updates in the audit log, want 2 (enable and disable)", updates)
	}
}

// A PATCH that changes nothing about capture announces nothing. Without this, an
// unrelated rename that echoed the block back would report retention starting
// every time it was saved.
func TestARedundantCapturePatchAnnouncesNothing(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	h.enableCapture(tenant, time.Hour)
	h.enableCapture(tenant, time.Hour)

	n := 0
	for _, ev := range h.events.emitted {
		if strings.HasPrefix(ev.typ, "debug_capture.") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d capture events for one change of state, want 1", n)
	}
}

// --- AC-7: the error body summarising a cascade ------------------------------

func TestUpstreamExhaustedDoesNotEchoContent(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return nil, errTestUpstreamDown
	}

	res := h.chat(tenant, sentinelRequest(), nil)
	if res.status == http.StatusOK {
		t.Fatal("the upstream was supposed to be unreachable")
	}
	if strings.Contains(string(res.body), sentinel) {
		t.Fatalf("the error envelope echoed the prompt:\n%s", res.body)
	}
}

// --- the policy surface ------------------------------------------------------

// The policy is a tenant field like any other, so it survives a read back — and
// the document that carries it never carries the content it governs.
func TestPolicyIsReadableAndCarriesNoContent(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")
	h.enableCapture(tenant, time.Hour)

	if res := h.chat(tenant, sentinelRequest(), nil); res.status != http.StatusOK {
		t.Fatalf("chat: status %d, body %s", res.status, res.body)
	}

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id, testBootstrapKey, nil)
	var got store.Tenant
	res.decode(t, &got)
	if !got.DebugCapture.Enabled || got.DebugCapture.TTLSeconds != 3600 {
		t.Fatalf("policy read back as %+v", got.DebugCapture)
	}
	if strings.Contains(string(res.body), sentinel) {
		t.Error("the tenant document carries prompt content")
	}
}

// GW-9: the capability is unconditional, so every deployment claims it.
func TestMetaAlwaysClaimsGW14(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	var meta struct {
		Capabilities []string `json:"capabilities"`
	}
	res.decode(t, &meta)
	for _, c := range meta.Capabilities {
		if c == "gw-14" {
			return
		}
	}
	t.Fatalf("gw-14 absent from %v", meta.Capabilities)
}
