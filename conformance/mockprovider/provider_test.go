package mockprovider_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// The mock is the instrument every other conformance test measures with, so it
// is tested on its own terms first. A fault the mock fails to inject would show
// up as a gateway failure, and that is an expensive place to debug it.

func newMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mockprovider.New().Handler())
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return out
}

func TestListModelsSpeaksTheAdaptersWireFormat(t *testing.T) {
	srv := newMock(t)

	resp, err := http.Get(srv.URL + "/models")
	if err != nil {
		t.Fatalf("GET /models: %v", err)
	}
	defer resp.Body.Close()

	body := decode(t, resp)
	if body["object"] != "list" {
		t.Errorf(`object = %v, want "list"`, body["object"])
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) != len(mockprovider.SeedModels()) {
		t.Fatalf("data = %v, want %d seed models", body["data"], len(mockprovider.SeedModels()))
	}
	first, _ := data[0].(map[string]any)
	for _, field := range []string{"id", "owned_by", "context_window", "max_output_tokens"} {
		if _, present := first[field]; !present {
			t.Errorf("model entry is missing %q, which the gateway's adapter reads", field)
		}
	}
}

// The base URL an operator registers may already end in /v1. Both spellings
// have to reach the same handler or half of the deployments that configure the
// mock would see an empty catalog.
func TestTheV1PrefixIsAcceptedToo(t *testing.T) {
	srv := newMock(t)

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCompletionCarriesUsage(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := decode(t, resp)
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatal("no usage block; the gateway cannot account for the request without one")
	}
	if usage["total_tokens"] != float64(18) {
		t.Errorf("total_tokens = %v, want 18", usage["total_tokens"])
	}
}

// An unknown model is a 404 and therefore a client failure, which is the case
// GW-3 says a gateway must NOT cascade on. The mock has to produce it faithfully
// or that test proves nothing.
func TestAnUnknownModelIsAClientError(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"no-such-model","messages":[]}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStreamingEndsWithUsageThenDone(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","stream":true,"messages":[]}`)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var payloads []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if line := strings.TrimPrefix(scanner.Text(), "data: "); line != scanner.Text() {
			payloads = append(payloads, line)
		}
	}
	if len(payloads) < 2 {
		t.Fatalf("got %d data frames, want the chunk sequence", len(payloads))
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		t.Errorf("last frame = %q, want [DONE]", last)
	}

	var penultimate map[string]any
	if err := json.Unmarshal([]byte(payloads[len(payloads)-2]), &penultimate); err != nil {
		t.Fatalf("the frame before [DONE] is not JSON: %v", err)
	}
	if _, ok := penultimate["usage"]; !ok {
		t.Error("the final chunk carries no usage")
	}
}

func setFault(t *testing.T, srv *httptest.Server, body string) {
	t.Helper()
	resp := post(t, srv.URL+"/_control/faults", body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("setting fault %s: status %d", body, resp.StatusCode)
	}
}

func TestRateLimitFaultApplies(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"rate_limit","count":1}`)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After on a 429")
	}
}

// A counted fault must stop after its count, or a test that arranges one
// failure and expects the retry to succeed would loop forever.
func TestACountedFaultIsConsumed(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"server_error","count":2}`)

	for i, want := range []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK} {
		resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)
		if resp.StatusCode != want {
			t.Fatalf("request %d: status = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
}

// Faults are keyed by model so two suite runs against one mock cannot see each
// other's arrangements (GW-10.AC-3).
func TestAFaultIsScopedToItsModel(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"server_error"}`)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-b","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("an unrelated model saw the fault: status %d", resp.StatusCode)
	}
}

func TestTheCatchAllFaultCoversEveryModel(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"mode":"server_error"}`)

	for _, model := range []string{"mock-chat-a", "mock-chat-b"} {
		resp := post(t, srv.URL+"/chat/completions", `{"model":"`+model+`","messages":[]}`)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", model, resp.StatusCode)
		}
	}
}

func TestClearingAFaultRestoresService(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"server_error"}`)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"none"}`)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the fault was cleared", resp.StatusCode)
	}
}

func TestAnUnknownFaultModeIsRejected(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/_control/faults", `{"model":"mock-chat-a","mode":"explode"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown mode", resp.StatusCode)
	}
}

func TestTimeoutFaultStallsAndHonoursCancellation(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"timeout","delay_ms":30000}`)

	client := &http.Client{Timeout: 300 * time.Millisecond}
	started := time.Now()
	_, err := client.Post(srv.URL+"/chat/completions", "application/json",
		strings.NewReader(`{"model":"mock-chat-a","messages":[]}`))
	if err == nil {
		t.Fatal("the request completed; the timeout fault did not stall it")
	}
	// The point is that the client's own deadline is what ended it, not that
	// the mock returned quickly.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s; the mock is not watching the request context", elapsed)
	}
}

func TestModelsCanBeAddedAndRemoved(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/_control/models", `{"id":"mock-added","context_window":4096}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("adding a model: status %d", resp.StatusCode)
	}
	if !modelListed(t, srv, "mock-added") {
		t.Fatal("the added model is not in GET /models")
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/_control/models/mock-added", nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("removing a model: %v", err)
	}
	defer del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("removing a model: status %d", del.StatusCode)
	}
	if modelListed(t, srv, "mock-added") {
		t.Error("the removed model is still in GET /models")
	}
}

func modelListed(t *testing.T, srv *httptest.Server, id string) bool {
	t.Helper()
	resp, err := http.Get(srv.URL + "/models")
	if err != nil {
		t.Fatalf("GET /models: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Data []mockprovider.Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding /models: %v", err)
	}
	for _, m := range body.Data {
		if m.ID == id {
			return true
		}
	}
	return false
}

// Which pooled credential served a request is observable, so a test can prove
// key rotation happened — but only by its tail, so a CI log that captures the
// state dump does not capture a key.
func TestTheStateDumpNamesCredentialsOnlyByTheirTail(t *testing.T) {
	srv := newMock(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/chat/completions",
		strings.NewReader(`{"model":"mock-chat-a","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-super-secret-value")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	resp.Body.Close()

	state, err := http.Get(srv.URL + "/_control/state")
	if err != nil {
		t.Fatalf("GET /_control/state: %v", err)
	}
	defer state.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(state.Body); err != nil {
		t.Fatalf("reading state: %v", err)
	}
	dump := buf.String()
	if strings.Contains(dump, "sk-super-secret-value") {
		t.Fatal("the state dump echoes the full credential")
	}
	if !strings.Contains(dump, "-value") {
		t.Errorf("the state dump does not identify the credential at all: %s", dump)
	}
}

func TestRequestCountsAreTrackedPerModel(t *testing.T) {
	srv := newMock(t)
	post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-b","messages":[]}`)
	post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-b","messages":[]}`)

	resp, err := http.Get(srv.URL + "/_control/state")
	if err != nil {
		t.Fatalf("GET /_control/state: %v", err)
	}
	defer resp.Body.Close()

	var state struct {
		Requests map[string]int `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	if state.Requests["mock-chat-b"] != 2 {
		t.Errorf("mock-chat-b count = %d, want 2", state.Requests["mock-chat-b"])
	}
}

// The counts measure upstream calls, not successes. GW-3 asks whether an open
// breaker skipped an entry with zero calls, and whether a client error was
// retried; neither question can be answered from a counter that a 500 leaves
// unchanged, because "not tried" and "tried and failed" would read the same.
func TestAFaultedRequestIsStillCountedAsAnUpstreamCall(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"server_error","count":1}`)
	post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)

	if got := requestCount(t, srv, "mock-chat-a"); got != 1 {
		t.Errorf("mock-chat-a count = %d, want 1", got)
	}
}

func requestCount(t *testing.T, srv *httptest.Server, model string) int {
	t.Helper()
	resp, err := http.Get(srv.URL + "/_control/state")
	if err != nil {
		t.Fatalf("GET /_control/state: %v", err)
	}
	defer resp.Body.Close()

	var state struct {
		Requests map[string]int `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return state.Requests[model]
}

func TestTheClientErrorFaultIsA400(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"client_error"}`)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decode(t, resp)
	errObj, _ := body["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("error.type = %v, want invalid_request_error", errObj["type"])
	}
}

// The abort has to come after content has been delivered. A stream that dies
// before its first byte is the case a gateway may still fall back on; this is
// the case it may not.
func TestTheStreamAbortFaultDiesAfterContent(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"mock-chat-a","mode":"stream_abort"}`)

	resp := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","stream":true,"messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; the abort must happen after the stream opens", resp.StatusCode)
	}

	var payloads []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if line := strings.TrimPrefix(scanner.Text(), "data: "); line != scanner.Text() {
			payloads = append(payloads, line)
		}
	}
	if len(payloads) < 2 {
		t.Fatalf("got %d data frames, want at least the role and content chunks", len(payloads))
	}
	if !strings.Contains(payloads[1], completionContentField) {
		t.Errorf("second frame carries no content: %s", payloads[1])
	}
	for _, p := range payloads {
		if p == "[DONE]" {
			t.Fatal("the stream terminated cleanly; nothing was aborted")
		}
	}
}

// The gateway reads content out of delta.content, so that is the field an
// aborted stream has to have already delivered for the abort to mean anything.
const completionContentField = `"content"`

func TestTheListingEndpointCanBeFaulted(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"model":"`+mockprovider.ListingTarget+`","mode":"server_error"}`)

	resp, err := http.Get(srv.URL + "/models")
	if err != nil {
		t.Fatalf("GET /models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	// Completions must be unaffected: GW-1.AC-6 is about a gateway that keeps
	// serving from a stale catalog, which it can only do if it can still serve.
	if c := post(t, srv.URL+"/chat/completions", `{"model":"mock-chat-a","messages":[]}`); c.StatusCode != http.StatusOK {
		t.Errorf("completion status = %d, want 200; the listing fault leaked", c.StatusCode)
	}
}

// A catch-all fault is an arrangement about completions. If it also broke
// catalog refresh, every fallback test would be quietly testing a gateway with
// an empty catalog instead of a failing provider.
func TestTheCatchAllFaultLeavesListingAlone(t *testing.T) {
	srv := newMock(t)
	setFault(t, srv, `{"mode":"server_error"}`)

	resp, err := http.Get(srv.URL + "/models")
	if err != nil {
		t.Fatalf("GET /models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAModeThatCannotApplyToListingIsRejected(t *testing.T) {
	srv := newMock(t)

	resp := post(t, srv.URL+"/_control/faults",
		`{"model":"`+mockprovider.ListingTarget+`","mode":"stream_abort"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
