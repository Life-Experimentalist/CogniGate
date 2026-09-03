package conformance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-14: the gateway is content-blind. Everything it writes down — logs,
// metrics, usage, events, webhooks, audit — carries metadata and nothing else,
// and the single exception is a per-tenant debug capture that an operator has to
// turn on deliberately and that deletes itself.
//
// Every test here works by planting a sentinel and then looking for it. The
// mock echoes the prompt back as the completion, so one plant lands on both
// halves of the exchange: a gateway that leaked only what the upstream said
// would otherwise pass a suite that searched the request half alone.

// gw14Sentinel is distinct from GW-8's canary so a failure names which
// requirement noticed. Neither string appears in any fixture, template or error
// message, so finding one in something the gateway kept is proof it came from a
// body.
const gw14Sentinel = "cognigate-conformance-sentinel-9c4e17-content-must-not-be-retained"

const headerDebugCapture = "X-CogniGate-Debug-Capture"

// captureRow is the admin plane's view of one retained exchange.
//
// Request and Response are []byte here because they are []byte on the wire too:
// JSON has no byte string, so the endpoint sends base64 and encoding/json
// decodes it back. That is deliberate at both ends — a capture holds the bytes
// as they arrived, and a body that is not valid JSON is often exactly the one
// being investigated.
type captureRow struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Status    int    `json:"status"`
	Model     string `json:"model"`
	Request   []byte `json:"request"`
	Response  []byte `json:"response"`
}

// setDebugCapture writes a tenant's capture policy through the admin API and
// hands back the raw response, because one criterion is about the policy being
// refused rather than applied.
func setDebugCapture(t *testing.T, tenantID string, policy map[string]any) *response {
	t.Helper()
	return suite.client.admin(t, http.MethodPatch, "/admin/v1/tenants/"+tenantID,
		map[string]any{"debug_capture": policy})
}

// enableCapture opens the window at full sampling, which is what makes these
// tests deterministic rather than flaky. It is also the setting the API is
// required to warn about; that warning is GW-14's own assertion elsewhere.
func enableCapture(t *testing.T, tenantID string, ttl time.Duration) {
	t.Helper()
	resp := setDebugCapture(t, tenantID, map[string]any{
		"enabled":     true,
		"ttl_seconds": int(ttl.Seconds()),
		"sample_rate": 1.0,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("enabling debug capture on %s: status %d\n%s",
			tenantID, resp.Status, truncate(resp.Body))
	}
}

func capturesOf(t *testing.T, tenantID string) []captureRow {
	t.Helper()
	resp := suite.client.admin(t, http.MethodGet, "/admin/v1/tenants/"+tenantID+"/captures", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET the captures of %s: status %d\n%s",
			tenantID, resp.Status, truncate(resp.Body))
	}
	var page struct {
		Data []captureRow `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("the captures endpoint is not the GW-6 list envelope: %v\n%s",
			err, truncate(resp.Body))
	}
	return page.Data
}

// plantSentinel sends one completion whose prompt carries the sentinel and
// checks the completion carried it back, so a later "it is nowhere" assertion is
// a claim about retention rather than about a plant that never happened.
func plantSentinel(t *testing.T, key, model string) *response {
	t.Helper()
	resp := chatWithPrompt(t, key, model, gw14Sentinel)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion carrying the sentinel: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	if !strings.Contains(string(resp.Body), gw14Sentinel) {
		t.Fatalf("the mock did not echo the sentinel, so only the request half was planted\n%s",
			truncate(resp.Body))
	}
	return resp
}

// sweepForSentinel checks every surface this suite can reach that GW-14 forbids
// content from reaching. The log leg is conditional because CONF_LOG_PATH is
// optional; the rest always run, so a deployment that publishes no log still
// gets the metrics, webhook and admin-plane halves of the criterion.
func sweepForSentinel(t *testing.T, tn tenant, from logCursor, hasLog bool, events *sink) {
	t.Helper()

	if hasLog {
		// Raw and undecoded on purpose: a prompt that escaped into a panic trace
		// or an unstructured line would be invisible to a reader that only
		// parsed well-formed JSON records.
		if raw := rawSince(t, from); strings.Contains(raw, gw14Sentinel) {
			t.Error("the gateway's log carries prompt content")
		}
	}

	if scrape := suite.client.metrics(t); strings.Contains(scrape.raw, gw14Sentinel) {
		t.Error("GET /metrics carries prompt content — a metric label is holding a body")
	}

	if events != nil {
		for _, d := range events.read(t) {
			if strings.Contains(string(d.Body), gw14Sentinel) {
				t.Errorf("the %s webhook delivery carries prompt content", d.Type)
			}
		}
	}

	for _, path := range []string{
		"/admin/v1/meta",
		"/admin/v1/audit?limit=200",
		"/admin/v1/tenants/" + tn.ID,
		"/admin/v1/tenants/" + tn.ID + "/events",
		"/admin/v1/tenants/" + tn.ID + "/quota",
	} {
		resp := suite.client.admin(t, http.MethodGet, path, nil)
		if strings.Contains(string(resp.Body), gw14Sentinel) {
			t.Errorf("%s carries prompt content", path)
		}
	}
	for _, path := range []string{"/v1/usage", "/v1/usage/breakdown", "/v1/models"} {
		resp := suite.client.do(t, http.MethodGet, path, tn.Key, nil)
		if strings.Contains(string(resp.Body), gw14Sentinel) {
			t.Errorf("%s carries prompt content", path)
		}
	}
}

// --- AC-1 --------------------------------------------------------------------

// With capture off — the default — a completion leaves no trace of what it said
// anywhere the gateway keeps things. This is a superset of GW-8.AC-2, which
// makes the same claim about the log alone.
func TestGW14_AC1_NoSentinelSurvivesWithCaptureOff(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac1")

	// A webhook the request will actually raise, so the delivery leg is a real
	// assertion rather than a search of an empty list. A cap of a hundred
	// completions with the threshold at one hundredth of it puts the soft
	// boundary at the first one and the hard cap far out of reach.
	var events *sink
	if suite.features["webhooks"] {
		events = newSink(t, tn.ID, "quota.threshold_crossed")
		putQuota(t, tn.ID, map[string]any{"day": tokenCap(mockTokensPerCompletion*100, 1)})
	}

	hasLog := suite.cfg.LogPath != ""
	var from logCursor
	if hasLog {
		from = markLog(t)
	}

	resp := plantSentinel(t, tn.Key, "mock-chat-a")
	if got := resp.Header.Get(headerDebugCapture); got != "" {
		t.Errorf("%s = %q with capture off; the header must be absent", headerDebugCapture, got)
	}
	if events != nil {
		// Metering happens after the response, so the completion that carried
		// the sentinel is not itself the one that reports the crossing — the
		// next one is, once its tokens have landed. Poll for it the way GW-4
		// does rather than assuming a single request is enough.
		awaitChat(t, tn.Key, "mock-chat-a",
			func(r *response) bool { return r.Header.Get(headerQuotaState) == quotaSoftExceeded },
			"reported the soft threshold")
		awaitDeliveries(t, events, "quota.threshold_crossed", 1)
	}

	sweepForSentinel(t, tn, from, hasLog, events)

	// The captures endpoint is swept separately from the rest, because with
	// capture off it is the surface an implementation is most likely to have
	// filled in anyway.
	if rows := capturesOf(t, tn.ID); len(rows) != 0 {
		t.Errorf("%d captures were retained with debug capture off", len(rows))
	}
}

// --- AC-2 --------------------------------------------------------------------

// The endpoint exists and answers, and answers empty. A 404 would be a
// different contract: an operator has to be able to ask "what is being kept"
// and be told "nothing" without first knowing whether capture was ever on.
func TestGW14_AC2_TheCapturesEndpointIsEmptyByDefault(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac2")
	for i := 0; i < 3; i++ {
		if resp := chat(t, tn.Key, "mock-chat-a"); resp.Status != http.StatusOK {
			t.Fatalf("a completion: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
		}
	}

	if rows := capturesOf(t, tn.ID); len(rows) != 0 {
		t.Fatalf("%d captures after three completions with capture off, want 0", len(rows))
	}
}

// --- AC-3 --------------------------------------------------------------------

// Enabling capture makes bodies retrievable, marks every response, and the
// entries are gone once the TTL has passed. The three are one criterion because
// the first without the third is a leak and the third without the second is a
// retention nobody was told about.
func TestGW14_AC3_EnabledCaptureIsRetrievableLabelledAndExpires(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac3")

	// Short, because the criterion is that the horizon is honoured, not that it
	// is any particular length. The specification's sixty seconds is
	// illustrative; a suite that slept for it would spend a minute proving what
	// two seconds prove.
	const ttl = 2 * time.Second
	enableCapture(t, tn.ID, ttl)

	resp := plantSentinel(t, tn.Key, "mock-chat-a")
	if got := resp.Header.Get(headerDebugCapture); got != "on" {
		t.Errorf("%s = %q while capture is enabled, want \"on\" — a consumer has no other "+
			"per-response way to see that retention is active", headerDebugCapture, got)
	}
	// The header is owed on every data-plane response for the tenant, not only
	// on the ones that were captured.
	if got := suite.client.do(t, http.MethodGet, "/v1/models", tn.Key, nil).
		Header.Get(headerDebugCapture); got != "on" {
		t.Errorf("%s = %q on GET /v1/models, want \"on\"", headerDebugCapture, got)
	}

	rows := capturesOf(t, tn.ID)
	if len(rows) == 0 {
		t.Fatal("capture is enabled at sample_rate 1.0 and nothing was retained")
	}
	if rows[0].RequestID == "" {
		t.Error("the capture carries no request id, so it cannot be tied to a log line")
	}

	// Expiry is a deletion promise, so it is asserted on the read an operator
	// would actually make rather than on any internal sweep.
	time.Sleep(ttl + time.Second)
	if rows := capturesOf(t, tn.ID); len(rows) != 0 {
		t.Fatalf("%d captures are still readable %s after a %s TTL", len(rows), ttl+time.Second, ttl)
	}
}

// --- AC-4 --------------------------------------------------------------------

// The 72-hour ceiling has no override, so it has to be refused rather than
// clamped: an operator who asks for a week and reads back three days believes
// the first number.
func TestGW14_AC4_ATTLAboveSeventyTwoHoursIsRejected(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac4")
	const seventyThreeHours = 73 * 60 * 60

	resp := setDebugCapture(t, tn.ID, map[string]any{
		"enabled":     true,
		"ttl_seconds": seventyThreeHours,
	})
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("a debug_capture.ttl_seconds of %d answered %d, want 400\n%s",
			seventyThreeHours, resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code == "" {
		t.Errorf("the refusal carries no error.code; GW-7 requires the envelope on every failure\n%s",
			truncate(resp.Body))
	}

	// Refused, not partially applied. A tenant left with capture on and a
	// silently lowered TTL would be the worst of both answers.
	tenantDoc := suite.client.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID, nil).JSON(t)
	policy, _ := tenantDoc["debug_capture"].(map[string]any)
	if enabled, _ := policy["enabled"].(bool); enabled {
		t.Error("the rejected policy was applied anyway: capture is enabled on the tenant")
	}
}

// --- AC-5 --------------------------------------------------------------------

// Turning retention on is an act someone must be able to account for
// afterwards, which is the whole reason capture is allowed to exist at all.
func TestGW14_AC5_EnablingAndDisablingCaptureIsAudited(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac5")
	before := auditIDs(t)

	enableCapture(t, tn.ID, time.Minute)
	if resp := setDebugCapture(t, tn.ID, map[string]any{"enabled": false}); resp.Status != http.StatusOK {
		t.Fatalf("disabling debug capture: status %d\n%s", resp.Status, truncate(resp.Body))
	}

	resource := "/admin/v1/tenants/" + tn.ID
	var added int
	for _, e := range auditEntries(t) {
		if before[e.ID] || e.Resource != resource {
			continue
		}
		if e.Actor == "" {
			t.Errorf("audit entry %s names no actor, so it answers when but not who", e.ID)
		}
		added++
	}
	if added < 2 {
		t.Fatalf("enabling and disabling capture left %d new audit entries for %s, want at least 2",
			added, resource)
	}
}

// --- AC-6 --------------------------------------------------------------------

// The complement of AC-1: with capture on, the content is readable through
// exactly one door and still through no other.
//
// The specification asks this as "the admin plane matches the sentinel, a raw
// Postgres inspection shows ciphertext". The gateway has no database — captures
// live in the process that made them and never reach a store — so the half of
// that sentence a suite can check is the half that matters: enabling capture
// must open the admin plane and nothing else.
func TestGW14_AC6_ACaptureIsTheOnlyPlaceContentIsReadable(t *testing.T) {
	begin(t)

	tn := quotaTenant(t, "gw14-ac6")

	var events *sink
	if suite.features["webhooks"] {
		events = newSink(t, tn.ID, "debug_capture.enabled")
	}

	hasLog := suite.cfg.LogPath != ""
	var from logCursor
	if hasLog {
		from = markLog(t)
	}

	enableCapture(t, tn.ID, time.Minute)
	if events != nil {
		awaitDeliveries(t, events, "debug_capture.enabled", 1)
	}
	plantSentinel(t, tn.Key, "mock-chat-a")

	rows := capturesOf(t, tn.ID)
	if len(rows) == 0 {
		t.Fatal("capture is enabled at sample_rate 1.0 and nothing was retained")
	}
	var found bool
	for _, row := range rows {
		if strings.Contains(string(row.Request), gw14Sentinel) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no capture holds the body that was sent; the retained request of the newest is:\n%s",
			truncate(rows[0].Request))
	}

	// Everything else stays shut. This is the regression the criterion is
	// really about: an implementation that satisfied "retrievable" by starting
	// to log the body would pass the check above and fail this one.
	sweepForSentinel(t, tn, from, hasLog, events)
}

// --- AC-7 --------------------------------------------------------------------

// A cascade that ran out of upstreams reports which classes of failure it saw.
// It must not quote the request back, which is the tempting thing to do when
// the failure looks like it might be the caller's fault.
func TestGW14_AC7_UpstreamExhaustedDoesNotEchoContent(t *testing.T) {
	begin(t)

	model := addMockModel(t, uniqueName("gw14-ac7"))
	injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)

	resp := chatWithPrompt(t, suite.dataKey, model, gw14Sentinel)
	if resp.Status == http.StatusOK {
		t.Fatalf("a model whose only upstream answers 500 succeeded\n%s", truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "upstream_exhausted" {
		t.Fatalf("error.code = %q, want upstream_exhausted\n%s", code, truncate(resp.Body))
	}
	if strings.Contains(string(resp.Body), gw14Sentinel) {
		t.Errorf("the upstream_exhausted body quotes the request back:\n%s", truncate(resp.Body))
	}
}
