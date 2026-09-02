package conformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// SuiteVersion is stamped into conformance-report.json so a stored report says
// which suite produced it. It tracks the specification revision, not the
// gateway under test.
const SuiteVersion = "1.0.0"

// --- configuration ----------------------------------------------------------

// config is the whole environment contract from GW-10. Three variables are
// enough to run the suite; the fourth exists because of where the mock lives.
type config struct {
	// BaseURL is the gateway under test. Empty means "no target", and the
	// suite skips rather than fails: `go test ./...` at the repository root
	// must not go red just because nobody started a gateway.
	BaseURL string

	// AdminKey is a root-scoped admin credential. In the reference deployment
	// that is the bootstrap key, which is not a cga- key — so the suite must
	// not validate its shape, only whether the admin plane accepts it.
	AdminKey string

	// MockProvider is the mock's base URL *as the gateway must dial it*. The
	// literal "embedded" (the default) hosts the mock inside this process,
	// which only works when the suite and the gateway share a host.
	MockProvider string

	// MockControl is the mock's base URL *as the suite must dial it*. It
	// defaults to MockProvider and differs only when the two sides reach the
	// mock by different names — a compose deployment being the case that
	// matters, where the gateway says http://mock-provider:9900 and the runner
	// says http://localhost:9900.
	MockControl string

	// MetricsToken authenticates the scrape endpoint. GW-8 leaves /metrics
	// unauthenticated by default — it is a private-network endpoint carrying no
	// request content — so this is empty unless the deployment chose otherwise.
	MetricsToken string

	// LogPath is a file the gateway's structured request log is written to, as
	// the *suite* must read it. GW-8's first two acceptance criteria are about
	// what a log line contains, which a wire-level suite cannot see: a gateway
	// logging to stdout in a container is conformant and still unreadable from
	// here. Empty skips those two, so a deployment that cannot expose its log is
	// reported as untested rather than as failing.
	LogPath string
}

func loadConfig() config {
	c := config{
		BaseURL:      strings.TrimRight(os.Getenv("CONF_BASE_URL"), "/"),
		AdminKey:     os.Getenv("CONF_ADMIN_KEY"),
		MockProvider: os.Getenv("CONF_MOCK_PROVIDER"),
		MockControl:  os.Getenv("CONF_MOCK_CONTROL_URL"),
		MetricsToken: os.Getenv("CONF_METRICS_TOKEN"),
		LogPath:      os.Getenv("CONF_LOG_PATH"),
	}
	if c.MockProvider == "" {
		c.MockProvider = "embedded"
	}
	return c
}

// --- suite state ------------------------------------------------------------

type suiteState struct {
	cfg    config
	client *client
	// capabilities is what /v1/meta claims, keyed by the gw-N id GW-9 fixes.
	// It selects which sections of the suite run at all.
	capabilities map[string]bool
	// features is finer grained than capabilities and does not replace it: a
	// gw-N id is too coarse to say whether quotas reject or only report, so a
	// handful of tests gate on these instead of on their whole requirement.
	features map[string]bool
	version  string

	// runID distinguishes this run's resources from a concurrent run's against
	// the same deployment (GW-10.AC-3). Every name the suite invents carries it.
	runID string

	tenantID string
	dataKey  string

	// providerURL is the mock's address as the *gateway* dials it, kept so a
	// test that creates a second tenant can point it at the same mock. mockCtrl
	// is the same mock as the *suite* dials it; in a container deployment those
	// two are different strings for the same process.
	providerURL string
	mockCtrl    string

	teardown  []func()
	provision error
}

var suite suiteState

// --- HTTP ------------------------------------------------------------------

// The GW-7 extension headers, spelled out rather than imported. The suite
// exercises a gateway through its published contract only, so it takes no
// dependency on the implementation's own constants: a header renamed in the Go
// code and not in the specification has to break this suite, not follow it.
const (
	headerRequestID       = "X-CogniGate-Request-Id"
	headerServedBy        = "X-CogniGate-Served-By"
	headerFallbackDepth   = "X-CogniGate-Fallback-Depth"
	headerQuotaState      = "X-CogniGate-Quota-State"
	headerClientRequestID = "X-Client-Request-Id"
)

// maxClientRequestID is the bound GW-7 puts on the echoed correlation id.
const maxClientRequestID = 128

// The GW-4 quota states, likewise spelled out rather than imported.
const (
	quotaOK           = "ok"
	quotaSoftExceeded = "soft-exceeded"
	quotaHardExceeded = "hard-exceeded"
)

type client struct {
	base string
	http *http.Client
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body, failing the test rather than returning an error: every
// caller would otherwise write the same three lines.
func (r *response) JSON(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("response is not JSON (status %d): %v\n%s", r.Status, err, truncate(r.Body))
	}
	return out
}

// ErrorCode reads error.code out of the envelope every plane answers failures
// in (GW-7). It returns "" when the body has no envelope, so a test can report
// what it actually got instead of panicking.
func (r *response) ErrorCode(t *testing.T) string {
	t.Helper()
	body := r.JSON(t)
	env, ok := body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := env["code"].(string)
	return code
}

func (c *client) do(t *testing.T, method, path, key string, body any) *response {
	t.Helper()
	resp, err := c.try(method, path, key, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (c *client) try(method, path, key string, body any) (*response, error) {
	return c.tryWithHeaders(method, path, key, body, nil)
}

// tryWithHeaders is try with extra request headers, for the GW-7 tests that are
// about what the caller sends rather than what it asks for.
func (c *client) tryWithHeaders(method, path, key string, body any, headers map[string]string) (*response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	raw, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer raw.Body.Close()

	payload, err := io.ReadAll(raw.Body)
	if err != nil {
		return nil, err
	}
	return &response{Status: raw.StatusCode, Header: raw.Header, Body: payload}, nil
}

// admin and data name which credential a call uses, so a test reads as the
// plane it is exercising rather than as a variable lookup.
func (c *client) admin(t *testing.T, method, path string, body any) *response {
	t.Helper()
	return c.do(t, method, path, suite.cfg.AdminKey, body)
}

func (c *client) data(t *testing.T, method, path string, body any) *response {
	t.Helper()
	return c.do(t, method, path, suite.dataKey, body)
}

func truncate(b []byte) string {
	const limit = 512
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "…"
}

// --- the mock ---------------------------------------------------------------

// startMock brings up the mock and returns the URL the gateway should use for
// it. An embedded mock is hosted here; anything else is assumed to be running
// already.
func startMock() (providerURL string) {
	if suite.cfg.MockProvider != "embedded" {
		suite.mockCtrl = strings.TrimRight(suite.cfg.MockControl, "/")
		if suite.mockCtrl == "" {
			suite.mockCtrl = strings.TrimRight(suite.cfg.MockProvider, "/")
		}
		return strings.TrimRight(suite.cfg.MockProvider, "/")
	}

	srv := httptest.NewServer(mockprovider.New().Handler())
	suite.teardown = append(suite.teardown, srv.Close)
	suite.mockCtrl = srv.URL
	return srv.URL
}

// mockControl drives the mock's fault injection. Errors fail the test that
// asked: a control call that did not land means the condition under test was
// never arranged, and continuing would produce a green result for an
// experiment that did not happen.
func mockControl(t *testing.T, method, path string, body any) *response {
	t.Helper()
	ctrl := &client{base: suite.mockCtrl, http: &http.Client{Timeout: 10 * time.Second}}
	resp, err := ctrl.try(method, path, "", body)
	if err != nil {
		t.Fatalf("mock control %s %s: %v", method, path, err)
	}
	if resp.Status >= 300 {
		t.Fatalf("mock control %s %s: status %d\n%s", method, path, resp.Status, truncate(resp.Body))
	}
	return resp
}

// tryMockControl is mockControl without the status assertion, for a call whose
// failure is not a test failure. Removing a model the test body already removed
// is the case that matters: the mock answers 404, which is the correct answer
// and not something a teardown should turn into a red run.
func tryMockControl(t *testing.T, method, path string, body any) *response {
	t.Helper()
	ctrl := &client{base: suite.mockCtrl, http: &http.Client{Timeout: 10 * time.Second}}
	resp, err := ctrl.try(method, path, "", body)
	if err != nil {
		t.Fatalf("mock control %s %s: %v", method, path, err)
	}
	return resp
}

// injectFault arranges an upstream condition and clears it when the test ends,
// so one test's fault can never leak into the next.
func injectFault(t *testing.T, model, mode string, count int) {
	t.Helper()
	injectFaultWith(t, model, mode, count, nil)
}

// injectFaultWith is injectFault for a mode that needs more than a count: a
// delay, or the body size the oversize mode produces. The extras are merged into
// the control request rather than added as parameters because every one of them
// applies to exactly one mode, and a signature carrying all of them would ask
// every existing caller to pass values that mean nothing for the fault it wants.
func injectFaultWith(t *testing.T, model, mode string, count int, extra map[string]any) {
	t.Helper()
	body := map[string]any{"model": model, "mode": mode, "count": count}
	for k, v := range extra {
		body[k] = v
	}
	mockControl(t, http.MethodPost, "/_control/faults", body)
	t.Cleanup(func() {
		mockControl(t, http.MethodPost, "/_control/faults", map[string]any{
			"model": model, "mode": mockprovider.FaultNone,
		})
	})
}

// addMockModel registers a model that belongs to this test alone, refreshes the
// suite tenant's catalog so the gateway can see it, and undoes both afterwards.
//
// Tests that need to fault a model use one of these rather than a seed model, so
// two concurrent suite runs cannot disturb each other (GW-10.AC-3). The refresh
// is here because without it the model stays invisible for up to the catalog TTL
// — an hour by default — and every test that forgot the refresh would fail for a
// reason unrelated to what it was testing. The two acceptance criteria that are
// *about* refresh timing drive the mock directly instead.
func addMockModel(t *testing.T, id string) string {
	t.Helper()
	// Registered before the removal it has to follow. t.Cleanup runs last-in
	// first-out, so refreshing here and adding the model afterwards is what puts
	// the refresh *after* the removal — the other order would refresh while the
	// model still existed and leave the gateway holding a catalog entry for a
	// model the mock no longer serves.
	t.Cleanup(func() { refreshCatalog(t) })
	addMockModelRaw(t, id, 128000)
	refreshCatalog(t)
	return id
}

// addPricedMockModel is addMockModel for a model the mock publishes a price for.
//
// Cost tiers are the one alias selector that cannot be tested without it: with
// every candidate at zero, "cheapest" falls through to the deterministic
// id tie-break, and a test that arranged its model to sort first would pass
// without the cost comparison ever running.
func addPricedMockModel(t *testing.T, id string, inputCostPerMTok float64) string {
	t.Helper()
	t.Cleanup(func() { refreshCatalog(t) })
	addMockModelSpec(t, map[string]any{
		"id":                  id,
		"context_window":      128000,
		"max_output_tokens":   4096,
		"input_cost_per_mtok": inputCostPerMTok,
	})
	refreshCatalog(t)
	return id
}

// addMockModelRaw registers a model without touching the gateway's catalog.
func addMockModelRaw(t *testing.T, id string, contextWindow int) string {
	t.Helper()
	addMockModelSpec(t, map[string]any{
		"id": id, "context_window": contextWindow, "max_output_tokens": 4096,
	})
	return id
}

// addMockModelSpec posts a model verbatim and removes it when the test ends.
func addMockModelSpec(t *testing.T, spec map[string]any) {
	t.Helper()
	id, _ := spec["id"].(string)
	mockControl(t, http.MethodPost, "/_control/models", spec)
	// Tolerant, because an acceptance criterion about a model disappearing
	// deletes it in the test body and this would then be the second delete.
	t.Cleanup(func() {
		tryMockControl(t, http.MethodDelete, "/_control/models/"+id, nil)
	})
}

// removeMockModel drops a model from the mock and puts it back when the test
// ends, so a test about a model disappearing does not permanently shrink the
// mock for the tests that run after it. The gateway's catalog is not touched
// here: the acceptance criteria that remove a model are about what a refresh
// does, so they post their own.
func removeMockModel(t *testing.T, id string, contextWindow int) {
	t.Helper()
	t.Cleanup(func() { refreshCatalog(t) })
	mockControl(t, http.MethodDelete, "/_control/models/"+id, nil)
	t.Cleanup(func() {
		mockControl(t, http.MethodPost, "/_control/models", map[string]any{
			"id": id, "context_window": contextWindow, "max_output_tokens": 4096,
		})
	})
}

// uniqueName builds a name that belongs to this run alone. The run id is
// lowercase base36 and a pid, so the result is also a legal alias name.
func uniqueName(hint string) string {
	return hint + "-" + suite.runID
}

// mockSnapshot is what GET /_control/state reports.
type mockSnapshot struct {
	Models   []string       `json:"models"`
	Requests map[string]int `json:"requests"`
	Keys     map[string]int `json:"keys"`
	Listings int            `json:"listings"`
}

func mockState(t *testing.T) mockSnapshot {
	t.Helper()
	resp := mockControl(t, http.MethodGet, "/_control/state", nil)
	var snap mockSnapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("mock control state: %v\n%s", err, truncate(resp.Body))
	}
	return snap
}

// upstreamCalls is how many times the gateway has dialled the mock for a model.
// It counts calls, not successes — a faulted call is still a call — which is the
// only counter that can separate "the gateway skipped this entry" from "the
// gateway tried it and it failed".
func upstreamCalls(t *testing.T, model string) int {
	t.Helper()
	return mockState(t).Requests[model]
}

// keysSince reports how many distinct pooled credentials the mock saw after the
// snapshot was taken. The counters are cumulative and shared by every model, so
// a test that wants "this request used two keys" has to diff rather than read.
func keysSince(t *testing.T, before mockSnapshot) []string {
	t.Helper()

	var used []string
	for key, count := range mockState(t).Keys {
		if count > before.Keys[key] {
			used = append(used, key)
		}
	}
	sort.Strings(used)
	return used
}

// --- gateway helpers --------------------------------------------------------

// refreshCatalog forces the suite tenant to re-poll its providers.
func refreshCatalog(t *testing.T) {
	t.Helper()
	refreshCatalogFor(t, suite.tenantID)
}

func refreshCatalogFor(t *testing.T, tenantID string) {
	t.Helper()
	resp := tryRefreshCatalogFor(t, tenantID)
	if resp.Status >= 300 {
		t.Fatalf("refreshing the catalog: status %d\n%s", resp.Status, truncate(resp.Body))
	}
}

// tryRefreshCatalogFor refreshes without insisting the refresh succeeded.
//
// A test that has deliberately taken a provider's listing endpoint down is
// asking the gateway to fail a poll, and the refresh route may well report that.
// What is under test there is what the catalog still serves afterwards, not the
// status of the call that failed.
func tryRefreshCatalogFor(t *testing.T, tenantID string) *response {
	t.Helper()
	return suite.client.admin(t, http.MethodPost, "/admin/v1/catalog/refresh",
		map[string]any{"tenant": tenantID})
}

// modelEntry is one row of GET /v1/models. Tests that need to prove a field is
// present rather than merely zero read the raw map instead.
type modelEntry struct {
	ID        string `json:"id"`
	OwnedBy   string `json:"owned_by"`
	CogniGate struct {
		Provider         string   `json:"provider"`
		Alias            bool     `json:"alias"`
		ResolvesTo       string   `json:"resolves_to"`
		ContextWindow    int      `json:"context_window"`
		Capabilities     []string `json:"capabilities"`
		InputCostPerMTok float64  `json:"input_cost_per_mtok"`
		DiscoveredAt     string   `json:"discovered_at"`
	} `json:"cognigate"`
}

func listModels(t *testing.T, key string) []modelEntry {
	t.Helper()
	resp := suite.client.do(t, http.MethodGet, "/v1/models", key, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/models: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	var body struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("GET /v1/models: %v\n%s", err, truncate(resp.Body))
	}
	return body.Data
}

func findModel(entries []modelEntry, id string) (modelEntry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return modelEntry{}, false
}

// concreteModels drops the alias rows. GW-2 puts aliases in the same list as
// the models they resolve to, so almost every assertion about "a model" has to
// say which of the two kinds of row it means.
func concreteModels(entries []modelEntry) []modelEntry {
	out := make([]modelEntry, 0, len(entries))
	for _, e := range entries {
		if !e.CogniGate.Alias {
			out = append(out, e)
		}
	}
	return out
}

// servedModel is the model half of X-CogniGate-Served-By.
//
// The header is qualified as provider/model while /v1/models lists bare ids, so
// comparing the two directly is the easiest way to write an assertion that can
// never pass.
func servedModel(t *testing.T, r *response) string {
	t.Helper()

	served := r.Header.Get(headerServedBy)
	if served == "" {
		t.Fatalf("the response carries no %s header (status %d)\n%s",
			headerServedBy, r.Status, truncate(r.Body))
	}
	// Cut at the first separator: a provider name never contains one, and a
	// model id occasionally does.
	_, model, ok := strings.Cut(served, "/")
	if !ok || model == "" {
		t.Fatalf("%s = %q, want the qualified provider/model form", headerServedBy, served)
	}
	return model
}

// --- aliases and routes -----------------------------------------------------

// tryPutAlias writes an alias without insisting the write succeeded, for the
// acceptance criteria that are about a rejection.
//
// The removal is registered even when the write failed. Deleting an alias that
// was never created is a 404 nobody reads, and making the cleanup conditional
// would mean a test that stopped between the two calls left the alias behind.
func tryPutAlias(t *testing.T, tenantID, name string, spec map[string]any) *response {
	t.Helper()

	if spec == nil {
		spec = map[string]any{}
	}
	t.Cleanup(func() {
		suite.client.admin(t, http.MethodDelete,
			"/admin/v1/tenants/"+tenantID+"/aliases/"+name, nil)
	})
	return suite.client.admin(t, http.MethodPut,
		"/admin/v1/tenants/"+tenantID+"/aliases/"+name, spec)
}

func putAlias(t *testing.T, tenantID, name string, spec map[string]any) {
	t.Helper()

	resp := tryPutAlias(t, tenantID, name, spec)
	if resp.Status != http.StatusOK {
		t.Fatalf("PUT alias %q: status %d\n%s", name, resp.Status, truncate(resp.Body))
	}
}

// tryPutRoute writes a fallback chain without insisting the write succeeded.
func tryPutRoute(t *testing.T, tenantID, match string, chain []string) *response {
	t.Helper()
	return suite.client.admin(t, http.MethodPut, "/admin/v1/tenants/"+tenantID+"/routing-rules",
		map[string]any{"match": match, "chain": chain})
}

// putRoute writes a fallback chain and removes it when the test ends.
//
// Routes are addressed by an assigned id rather than by their match, so the
// cleanup cannot be written without the response body.
func putRoute(t *testing.T, tenantID, match string, chain ...string) {
	t.Helper()

	resp := tryPutRoute(t, tenantID, match, chain)
	if resp.Status != http.StatusOK {
		t.Fatalf("PUT route %q: status %d\n%s", match, resp.Status, truncate(resp.Body))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil || created.ID == "" {
		t.Fatalf("the created route has no id: %s", truncate(resp.Body))
	}
	t.Cleanup(func() {
		suite.client.admin(t, http.MethodDelete,
			"/admin/v1/tenants/"+tenantID+"/routing-rules/"+created.ID, nil)
	})
}

// chat sends a minimal completion. The prompt is deliberately trivial: GW-14
// forbids the gateway from retaining content, and a distinctive prompt invites
// someone to satisfy a test by retaining one.
func chat(t *testing.T, key, model string) *response {
	t.Helper()
	return suite.client.do(t, http.MethodPost, "/v1/chat/completions", key, map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
}

// chatWithHeaders is chat with extra request headers.
func chatWithHeaders(t *testing.T, key, model string, headers map[string]string) *response {
	t.Helper()
	resp, err := suite.client.tryWithHeaders(http.MethodPost, "/v1/chat/completions", key,
		map[string]any{
			"model":    model,
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}, headers)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	return resp
}

// streamResult is what a streaming completion left behind.
type streamResult struct {
	Status int
	Header http.Header
	// Frames are the payloads of each `data:` line, in order, including the
	// "[DONE]" sentinel when one arrived.
	Frames []string
	// Err is the transport error that ended the read, if any. A stream that was
	// cut off rather than closed is the distinction GW-3.AC-7 turns on, and it
	// only survives if the reader declines to treat an unexpected EOF as fatal.
	Err error
}

// chatStream sends a streaming completion and reads the frames as they arrive.
//
// It deliberately does not fail the test on a read error. A gateway whose
// upstream dies mid-stream has two ways to behave — emit a terminal error event,
// or drop the connection — and only one of them conforms; a helper that fataled
// on the second would report the failure as a broken test rather than as the
// non-conformance it is.
func chatStream(t *testing.T, key, model string) streamResult {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"model":    model,
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, suite.client.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)

	raw, err := suite.client.http.Do(req)
	if err != nil {
		return streamResult{Err: err}
	}
	defer raw.Body.Close()

	out := streamResult{Status: raw.StatusCode, Header: raw.Header}

	scanner := bufio.NewScanner(raw.Body)
	// SSE frames are small, but a completion chunk carrying a long delta can
	// still outgrow the scanner's 64KiB default, and the failure mode there is a
	// truncated stream that looks exactly like an aborted one.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			out.Frames = append(out.Frames, strings.TrimSpace(payload))
		}
	}
	out.Err = scanner.Err()
	return out
}

// hasFrameContaining reports whether any frame mentions the needle. The frames
// are JSON, but a terminal error event and a content chunk differ by which keys
// they carry rather than by shape, so tests match on the key rather than
// decoding into a type that would have to model both.
func (s streamResult) hasFrameContaining(needle string) bool {
	for _, f := range s.Frames {
		if strings.Contains(f, needle) {
			return true
		}
	}
	return false
}

// --- polling ----------------------------------------------------------------

// awaitHealth polls GET /v1/health until the predicate holds.
//
// Reading health once is not enough. The report is cached for health.cache_ttl
// — two seconds by default, and not something the suite can turn off from
// outside — so a test that arranges a condition and immediately reads health can
// be answered from a report built before it acted. Polling also covers the
// second-granularity fields, where "non-zero age" is a claim that only becomes
// true once a second has passed.
func awaitHealth(t *testing.T, key string, want func(map[string]any) bool, describe string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	var last map[string]any
	for {
		resp := suite.client.do(t, http.MethodGet, "/v1/health", key, nil)
		last = resp.JSON(t)
		if want(last) {
			return last
		}
		if time.Now().After(deadline) {
			encoded, _ := json.MarshalIndent(last, "", "  ")
			t.Fatalf("GET /v1/health never reported %s\n%s", describe, truncate(encoded))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// awaitModel polls GET /v1/models until the id appears, or stops appearing.
func awaitModel(t *testing.T, key, id string, present bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		_, found := findModel(listModels(t, key), id)
		if found == present {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /v1/models: model %q present=%v, want present=%v", id, found, present)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// --- quotas -----------------------------------------------------------------

// tokenCap and costCap build the four-slot body PUT .../quota takes. Every
// GW-4 test configures exactly the slots it is about: an unset slot is
// unlimited, and a stray one would let a test be rejected by a cap it did not
// mean to set.
func tokenCap(cap float64, softPct int) map[string]any {
	return map[string]any{"tokens": map[string]any{"cap": cap, "soft_threshold_pct": softPct}}
}

func costCap(cap float64, softPct int) map[string]any {
	return map[string]any{"cost": map[string]any{"cap": cap, "soft_threshold_pct": softPct}}
}

// putQuota writes a tenant's quota. The caller passes the window bodies it
// wants; nothing is filled in for it.
func putQuota(t *testing.T, tenantID string, body map[string]any) {
	t.Helper()
	resp := suite.client.admin(t, http.MethodPut, "/admin/v1/tenants/"+tenantID+"/quota", body)
	if resp.Status != http.StatusOK {
		t.Fatalf("setting the quota for %s: status %d\n%s", tenantID, resp.Status, truncate(resp.Body))
	}
}

// putKeyQuota writes one key's quota, which narrows its tenant's rather than
// replacing it.
func putKeyQuota(t *testing.T, tenantID, keyID string, body map[string]any) {
	t.Helper()
	resp := suite.client.admin(t, http.MethodPut,
		"/admin/v1/tenants/"+tenantID+"/keys/"+keyID+"/quota", body)
	if resp.Status != http.StatusOK {
		t.Fatalf("setting the quota for key %s: status %d\n%s", keyID, resp.Status, truncate(resp.Body))
	}
}

// usageLimit is one configured cap as GET /v1/usage reports it.
type usageLimit struct {
	Scope            string  `json:"scope"`
	Window           string  `json:"window"`
	Unit             string  `json:"unit"`
	Cap              float64 `json:"cap"`
	SoftThresholdPct int     `json:"soft_threshold_pct"`
	Consumed         float64 `json:"consumed"`
	Remaining        float64 `json:"remaining"`
	ResetsAt         string  `json:"resets_at"`
	State            string  `json:"state"`
}

// usageReport is GET /v1/usage.
type usageReport struct {
	Object           string       `json:"object"`
	Window           string       `json:"window"`
	Requests         int64        `json:"requests"`
	PromptTokens     int64        `json:"prompt_tokens"`
	CompletionTokens int64        `json:"completion_tokens"`
	TotalTokens      int64        `json:"total_tokens"`
	CostUSD          float64      `json:"cost_usd"`
	State            string       `json:"state"`
	Limits           []usageLimit `json:"limits"`
}

// slot finds the limit for one window and unit, so an assertion can name what it
// is about rather than indexing into a list whose order is not contractual.
func (u usageReport) slot(t *testing.T, scope, window, unit string) usageLimit {
	t.Helper()
	for _, l := range u.Limits {
		if l.Scope == scope && l.Window == window && l.Unit == unit {
			return l
		}
	}
	t.Fatalf("GET /v1/usage reports no %s %s.%s limit; got %+v", scope, window, unit, u.Limits)
	return usageLimit{}
}

func usage(t *testing.T, key, window string) usageReport {
	t.Helper()
	resp := suite.client.do(t, http.MethodGet, "/v1/usage?window="+window, key, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/usage: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	var out usageReport
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("GET /v1/usage: %v\n%s", err, truncate(resp.Body))
	}
	return out
}

// breakdownReport is GET /v1/usage/breakdown.
type breakdownReport struct {
	Object  string `json:"object"`
	Window  string `json:"window"`
	GroupBy string `json:"group_by"`
	Data    []struct {
		Key              string  `json:"key"`
		Requests         int64   `json:"requests"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		TotalTokens      int64   `json:"total_tokens"`
		CostUSD          float64 `json:"cost_usd"`
	} `json:"data"`
	// Truncated says the deployment had more groups than it will return at once.
	Truncated bool `json:"truncated"`
}

func usageBreakdown(t *testing.T, key, window, groupBy string) breakdownReport {
	t.Helper()
	resp := suite.client.do(t, http.MethodGet,
		"/v1/usage/breakdown?window="+window+"&group_by="+groupBy, key, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/usage/breakdown: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	var out breakdownReport
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("GET /v1/usage/breakdown: %v\n%s", err, truncate(resp.Body))
	}
	return out
}

// awaitUsage polls GET /v1/usage until the predicate holds.
//
// Telemetry is written after the response is returned, so a test that completes
// a request and reads usage in the next statement is reading a total that has
// not been updated yet. GW-4 allows sixty seconds of staleness; the deadline here
// is shorter because a gateway that takes a minute would be within its rights and
// still worth reporting, and the suite would rather say so than hang.
func awaitUsage(t *testing.T, key, window string, want func(usageReport) bool, describe string) usageReport {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for {
		got := usage(t, key, window)
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			encoded, _ := json.MarshalIndent(got, "", "  ")
			t.Fatalf("GET /v1/usage never reported %s\n%s", describe, truncate(encoded))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// awaitBreakdown polls GET /v1/usage/breakdown until the predicate holds, for
// the same reason awaitUsage exists: the record a test is looking for is written
// after the response that produced it was already returned.
func awaitBreakdown(t *testing.T, key, window, groupBy string, want func(breakdownReport) bool, describe string) breakdownReport {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for {
		got := usageBreakdown(t, key, window, groupBy)
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			encoded, _ := json.MarshalIndent(got, "", "  ")
			t.Fatalf("GET /v1/usage/breakdown?group_by=%s never reported %s\n%s",
				groupBy, describe, truncate(encoded))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// awaitChat polls completions until one satisfies the predicate, and returns it.
//
// Unlike awaitUsage this costs tokens: every poll is a real completion, metered
// against the very quota the caller is waiting on. Tests that use it size their
// caps so the polling cannot itself reach the cap — a poll loop that exhausted
// the quota it was watching would fail for its own reason rather than the
// gateway's.
func awaitChat(t *testing.T, key, model string, want func(*response) bool, describe string) *response {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		resp := chat(t, key, model)
		if want(resp) {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completion ever %s; last was status %d with %s: %q\n%s",
				describe, resp.Status, headerQuotaState, resp.Header.Get(headerQuotaState),
				truncate(resp.Body))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// --- webhook sink -----------------------------------------------------------

// delivery is one webhook the mock's sink received.
//
// Body is kept as raw bytes rather than decoded in place because GW-8 signs the
// exact bytes delivered: an HMAC check has to run over what arrived on the wire,
// and a re-encoding of the parsed JSON will not reproduce it.
type delivery struct {
	Type      string          `json:"type"`
	EventID   string          `json:"event_id"`
	Signature string          `json:"signature"`
	Tenant    string          `json:"tenant"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body"`
}

// accepted reports whether the sink took the delivery. A rejection is an attempt
// the gateway is expected to repeat, not an event that arrived.
func (d delivery) accepted() bool { return d.Status >= 200 && d.Status <= 299 }

// data decodes the envelope's payload.
func (d delivery) data(t *testing.T) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(d.Body, &envelope); err != nil {
		t.Fatalf("decoding a %s delivery: %v\n%s", d.Type, err, truncate(d.Body))
	}
	return envelope.Data
}

// sink is one webhook endpoint on the mock, subscribed to a tenant's events.
type sink struct {
	name   string
	secret string
}

// newSink subscribes a tenant's webhook to the named events and returns the
// endpoint it was pointed at.
//
// The sink is hosted by the mock rather than by this process because the gateway
// has to dial it: in a container deployment the suite's own address is not
// reachable from the gateway's network, which is the same problem
// CONF_MOCK_PROVIDER already solves. Each sink has a path of its own, so two
// tests — or two concurrent runs — cannot count each other's deliveries.
func newSink(t *testing.T, tenantID string, eventTypes ...string) *sink {
	t.Helper()

	// Long enough for the gateway's minimum, and not a credential: it exists so
	// the suite can recompute the HMAC the gateway sent.
	const secret = "conformance-webhook-secret"

	name := uniqueName(strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())))
	resp := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/webhooks",
		map[string]any{
			"url":    suite.providerURL + "/_events/" + name,
			"secret": secret,
			"events": eventTypes,
		})
	if resp.Status != http.StatusCreated {
		t.Fatalf("subscribing a webhook for %s: status %d\n%s", tenantID, resp.Status, truncate(resp.Body))
	}

	return &sink{name: name, secret: secret}
}

// read returns every attempt the sink saw, rejected ones included.
func (s *sink) read(t *testing.T) []delivery {
	t.Helper()
	got := mockControl(t, http.MethodGet, "/_control/events/"+s.name, nil)
	var body struct {
		Data []delivery `json:"data"`
	}
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("reading the sink: %v\n%s", err, truncate(got.Body))
	}
	return body.Data
}

// rejectNext arms the sink to refuse its next count deliveries with status, so a
// test can drive the gateway's retry schedule rather than wait for one to happen
// by accident. It must be called before the event it is meant to catch is
// raised.
func (s *sink) rejectNext(t *testing.T, status, count int) {
	t.Helper()
	mockControl(t, http.MethodPost, "/_control/events/"+s.name+"/fault",
		map[string]any{"status": status, "count": count})
}

// deliveriesOfType filters a sink's accepted deliveries, because a quota that
// crosses its soft threshold and later its hard cap raises two different event
// types and only one of them is ever the subject of an assertion.
func deliveriesOfType(all []delivery, eventType string) []delivery {
	var out []delivery
	for _, d := range all {
		if d.Type == eventType && d.accepted() {
			out = append(out, d)
		}
	}
	return out
}

// awaitDeliveries waits for want events of a type to arrive, and then keeps
// waiting.
//
// The second wait is the point. "Exactly one" is two claims — that one arrived,
// and that a second did not — and a test that stopped at the first would prove
// only the easier half, passing for a gateway that goes on to deliver one webhook
// per request for the rest of the window. Delivery is asynchronous and retried,
// so the settling period has to outlast a redelivery rather than merely a
// round trip.
func awaitDeliveries(t *testing.T, s *sink, eventType string, want int) []delivery {
	t.Helper()
	return awaitDeliveriesWithin(t, s, eventType, want, 20*time.Second)
}

// awaitDeliveriesWithin is awaitDeliveries with the patience named explicitly,
// for the one test that arranges a rejection and so has to outlast the gateway's
// backoff rather than merely its first attempt.
func awaitDeliveriesWithin(t *testing.T, s *sink, eventType string, want int, patience time.Duration) []delivery {
	t.Helper()

	deadline := time.Now().Add(patience)
	var got []delivery
	for {
		got = deliveriesOfType(s.read(t), eventType)
		if len(got) >= want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d %s event(s) arrived, want %d", len(got), eventType, want)
		}
		time.Sleep(250 * time.Millisecond)
	}

	time.Sleep(2 * time.Second)
	return deliveriesOfType(s.read(t), eventType)
}

// allowSeconds starts a stopwatch and returns the check for it. Several
// acceptance criteria are bounded in wall-clock time, and a test that waited
// without measuring would pass a gateway that took a minute over a bound of ten
// seconds. The failure is reported rather than fatal: what the gateway
// eventually did is still worth asserting on.
func allowSeconds(t *testing.T, limit time.Duration) func(what string) {
	t.Helper()
	started := time.Now()
	return func(what string) {
		t.Helper()
		if elapsed := time.Since(started); elapsed > limit {
			t.Errorf("%s after %s, past the %s the specification allows",
				what, elapsed.Round(time.Millisecond), limit)
		}
	}
}

// --- extra tenants ----------------------------------------------------------

// tenant is a tenant the suite created beyond the one it provisions for itself.
type tenant struct {
	ID  string
	Key string
}

// newTenant creates a tenant with a data key and deletes it when the test ends.
//
// Several acceptance criteria are statements about isolation — different
// catalogs, aliases one tenant cannot see — and none of them can be written
// against a single tenant. The teardown is per test rather than per run so
// GW-10.AC-4's "the deployment is clean afterwards" stays true of every
// intermediate moment, not only of the end.
func newTenant(t *testing.T, hint string) tenant {
	t.Helper()

	created := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants",
		map[string]any{"name": uniqueName(hint)})
	if created.Status != http.StatusCreated {
		t.Fatalf("creating tenant %q: status %d\n%s", hint, created.Status, truncate(created.Body))
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body, &body); err != nil || body.ID == "" {
		t.Fatalf("the created tenant has no id: %s", truncate(created.Body))
	}
	t.Cleanup(func() {
		suite.client.admin(t, http.MethodDelete,
			"/admin/v1/tenants/"+body.ID+"?confirm="+body.ID, nil)
	})

	key := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+body.ID+"/keys",
		map[string]any{"name": "conformance", "plane": "data"})
	var minted struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(key.Body, &minted); err != nil || minted.Secret == "" {
		t.Fatalf("minting a data key for %s: status %d, no secret in %s",
			body.ID, key.Status, truncate(key.Body))
	}
	return tenant{ID: body.ID, Key: minted.Secret}
}

// dataKey is a minted data credential, kept with its id because a key-level
// quota is addressed by id while the requests it governs are made with the
// secret. newTenant returns only the secret, which is all every other
// requirement needs.
type dataKey struct {
	ID     string
	Secret string
}

// newDataKey mints an additional data key on a tenant.
//
// GW-4.AC-3 is the reason it exists: "a sibling key continues to work" cannot be
// written with one key, and the id has to survive the mint because the plaintext
// is shown exactly once and the id is never derivable from it.
func newDataKey(t *testing.T, tenantID, name string) dataKey {
	t.Helper()

	created := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/keys",
		map[string]any{"name": name, "plane": "data"})
	if created.Status != http.StatusCreated {
		t.Fatalf("minting %q for %s: status %d\n%s", name, tenantID, created.Status, truncate(created.Body))
	}
	var minted struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created.Body, &minted); err != nil ||
		minted.Secret == "" || minted.Key.ID == "" {
		t.Fatalf("minting %q for %s: no id and secret in %s", name, tenantID, truncate(created.Body))
	}
	return dataKey{ID: minted.Key.ID, Secret: minted.Secret}
}

// addMockProvider points a tenant at the same mock the suite provisioned, so two
// tenants can be compared on something other than one of them being empty.
func addMockProvider(t *testing.T, tn tenant) {
	t.Helper()

	resp := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tn.ID+"/providers",
		map[string]any{
			"name":     "mock",
			"kind":     "openai",
			"base_url": suite.providerURL,
			"keys":     []string{"mock-key-primary", "mock-key-secondary"},
		})
	if resp.Status != http.StatusCreated {
		t.Fatalf("registering the mock for %s: status %d\n%s", tn.ID, resp.Status, truncate(resp.Body))
	}
	awaitModel(t, tn.Key, "mock-chat-a", true)
}

// narrowLimits replaces a tenant's GW-13 limit overrides.
//
// The block is replaced wholesale, so passing an empty map clears every
// override. Callers patch last, after every other bit of provisioning: a tenant
// narrowed to one request per second spends its only token on the very next
// call, including the ones a helper makes on its behalf.
func narrowLimits(t *testing.T, tenantID string, limits map[string]any) {
	t.Helper()
	resp := suite.client.admin(t, http.MethodPatch, "/admin/v1/tenants/"+tenantID,
		map[string]any{"limits": limits})
	if resp.Status != http.StatusOK {
		t.Fatalf("narrowing the limits of %s: status %d\n%s", tenantID, resp.Status, truncate(resp.Body))
	}
}

// publishedLimits reads the limits block a key is told it is held to.
//
// Every GW-13 criterion is a comparison between what /v1/meta says and what the
// gateway does, so the tests read the figure rather than hard-coding the
// deployment's default: a suite that asserted 2 MiB would pass against a gateway
// that published 2 MiB and enforced a tenth of it.
func publishedLimits(t *testing.T, key string) map[string]any {
	t.Helper()
	body := suite.client.do(t, http.MethodGet, "/v1/meta", key, nil).JSON(t)
	limits, ok := body["limits"].(map[string]any)
	if !ok {
		t.Fatalf("limits is %T, want an object", body["limits"])
	}
	return limits
}

// limitInt reads one published limit, failing the test when it is missing or
// not a positive number. A limit of zero is not a looser limit, it is a gateway
// that has stopped publishing what it enforces.
func limitInt(t *testing.T, limits map[string]any, name string) int {
	t.Helper()
	n, ok := limits[name].(float64)
	if !ok || n < 1 {
		t.Fatalf("limits.%s is %v, want a positive number", name, limits[name])
	}
	return int(n)
}

// --- provisioning -----------------------------------------------------------

// provision creates everything this run owns: one tenant, one data key, one
// provider pointing at the mock. Names carry the run id so a second suite
// running against the same deployment collides with nothing.
func provision(runID string) error {
	c := suite.client

	tenant, err := c.try(http.MethodPost, "/admin/v1/tenants", suite.cfg.AdminKey,
		map[string]any{"name": "conformance-" + runID})
	if err != nil {
		return fmt.Errorf("creating the tenant: %w", err)
	}
	if tenant.Status != http.StatusCreated {
		return fmt.Errorf("creating the tenant: status %d\n%s\n"+
			"CONF_ADMIN_KEY must be a root-scoped admin credential; in the reference "+
			"deployment that is the value of ADMIN_BOOTSTRAP_KEY", tenant.Status, truncate(tenant.Body))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(tenant.Body, &created); err != nil || created.ID == "" {
		return fmt.Errorf("the created tenant has no id: %s", truncate(tenant.Body))
	}
	suite.tenantID = created.ID

	key, err := c.try(http.MethodPost, "/admin/v1/tenants/"+created.ID+"/keys", suite.cfg.AdminKey,
		map[string]any{"name": "conformance", "plane": "data"})
	if err != nil {
		return fmt.Errorf("minting a data key: %w", err)
	}
	var minted struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(key.Body, &minted); err != nil || minted.Secret == "" {
		return fmt.Errorf("minting a data key: status %d, no secret in %s", key.Status, truncate(key.Body))
	}
	suite.dataKey = minted.Secret

	providerURL := startMock()
	suite.providerURL = providerURL
	prov, err := c.try(http.MethodPost, "/admin/v1/tenants/"+created.ID+"/providers", suite.cfg.AdminKey,
		map[string]any{
			"name":     "mock",
			"kind":     "openai",
			"base_url": providerURL,
			// Two keys so a test can prove the gateway rotates within a
			// provider's pool before it gives up on the provider (GW-3).
			"keys": []string{"mock-key-primary", "mock-key-secondary"},
		})
	if err != nil {
		return fmt.Errorf("registering the mock provider: %w", err)
	}
	if prov.Status != http.StatusCreated {
		return fmt.Errorf("registering the mock provider: status %d\n%s", prov.Status, truncate(prov.Body))
	}

	return awaitCatalog(providerURL)
}

// awaitCatalog is the fail-fast the whole harness turns on.
//
// The URL the gateway dials for the mock is not the URL this process dials, and
// getting that wrong is the single easiest way to misconfigure a run. Left
// undetected it surfaces as forty unrelated tests failing on 503, so it is
// worth one explicit check with a message that names the actual cause.
func awaitCatalog(providerURL string) error {
	deadline := time.Now().Add(30 * time.Second)
	var last string

	for time.Now().Before(deadline) {
		resp, err := suite.client.try(http.MethodGet, "/v1/models", suite.dataKey, nil)
		if err != nil {
			last = err.Error()
		} else {
			var body struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(resp.Body, &body); err == nil {
				for _, m := range body.Data {
					if strings.Contains(m.ID, "mock-chat-a") {
						return nil
					}
				}
			}
			last = fmt.Sprintf("status %d: %s", resp.Status, truncate(resp.Body))
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf(
		"the gateway never listed the mock's models, so it cannot reach the mock at %s.\n"+
			"CONF_MOCK_PROVIDER must be the mock's address *as the gateway dials it* — for a\n"+
			"containerised gateway that is a service name on its network, not a localhost port\n"+
			"on this machine. Set CONF_MOCK_CONTROL_URL when the suite reaches it by a different\n"+
			"name. Last response: %s", providerURL, last)
}

// deprovision removes everything provision created and reports whether the
// deployment is genuinely clean afterwards (GW-10.AC-4).
func deprovision() error {
	for i := len(suite.teardown) - 1; i >= 0; i-- {
		suite.teardown[i]()
	}
	if suite.tenantID == "" {
		return nil
	}

	del, err := suite.client.try(http.MethodDelete,
		"/admin/v1/tenants/"+suite.tenantID+"?confirm="+suite.tenantID, suite.cfg.AdminKey, nil)
	if err != nil {
		return fmt.Errorf("deleting the tenant: %w", err)
	}
	if del.Status != http.StatusNoContent && del.Status != http.StatusNotFound {
		return fmt.Errorf("deleting the tenant: status %d\n%s", del.Status, truncate(del.Body))
	}

	// Deleting is not the same as being gone. AC-4 is about what remains.
	check, err := suite.client.try(http.MethodGet, "/admin/v1/tenants/"+suite.tenantID, suite.cfg.AdminKey, nil)
	if err != nil {
		return fmt.Errorf("verifying the tenant is gone: %w", err)
	}
	if check.Status != http.StatusNotFound {
		return fmt.Errorf("the suite's tenant %s still exists after teardown (status %d)",
			suite.tenantID, check.Status)
	}
	return nil
}
