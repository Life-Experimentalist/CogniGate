package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/obs"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// testBootstrapKey is long enough to clear minBootstrapKeyLen. It is a literal
// in a test file and nowhere else: a value that looked plausible in an example
// is the kind of thing that ends up in a deployment.
const testBootstrapKey = "test-bootstrap-key-0123456789"

// errTestUpstreamDown is the failure a test injects to stand in for an
// unreachable provider.
var errTestUpstreamDown = errors.New("upstream is unreachable")

// fakeAdapter is an upstream that answers from memory. Registering it as kind
// "openai" means the registry's fallback also lands here, so a provider created
// with any kind at all is served by this adapter and no test can accidentally
// reach the network.
type fakeAdapter struct {
	models []store.Model
	do     func(ctx context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error)
	// listErr, when set, makes every catalog refresh fail — the state a fail-open
	// path has to be exercised in.
	listErr error
}

func (f *fakeAdapter) Kind() string { return provider.KindOpenAI }

func (f *fakeAdapter) ListModels(context.Context, provider.Credential) ([]store.Model, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.Model, len(f.models))
	copy(out, f.models)
	return out, nil
}

func (f *fakeAdapter) Do(ctx context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
	if f.do != nil {
		return f.do(ctx, cred, req)
	}
	return &provider.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       []byte(`{"object":"chat.completion","choices":[]}`),
		Failure:    provider.FailNone,
	}, nil
}

// defaultModels is a two-provider-shaped catalog: one cheap small-context model
// and one expensive large-context one, which is the minimum needed for the
// alias constraints to have anything to choose between.
var defaultModels = []store.Model{
	{
		ID:                "test-small",
		ContextWindow:     8192,
		MaxOutputTokens:   2048,
		Capabilities:      []string{"chat"},
		InputCostPerMTok:  0.15,
		OutputCostPerMTok: 0.60,
	},
	{
		ID:                "test-large",
		ContextWindow:     128000,
		MaxOutputTokens:   16384,
		Capabilities:      []string{"chat", "vision"},
		InputCostPerMTok:  2.50,
		OutputCostPerMTok: 10.00,
	},
}

type harness struct {
	t         *testing.T
	srv       *Server
	mem       *store.Memory
	adapter   *fakeAdapter
	events    *recordingEmitter
	telemetry *obs.Telemetry
}

// recordingEmitter stands in for the webhook dispatcher so a test can assert an
// event was raised without a delivery target.
type recordingEmitter struct {
	emitted []emittedEvent
}

type emittedEvent struct {
	tenant string
	typ    string
	data   map[string]any
}

func (r *recordingEmitter) Emit(_ context.Context, tenantID, eventType string, data map[string]any) {
	r.emitted = append(r.emitted, emittedEvent{tenant: tenantID, typ: eventType, data: data})
}

// newHarness assembles a server over the in-memory store and the fake upstream.
// Nothing it builds dials anything, so the whole package's tests run offline.
func newHarness(t *testing.T, mutate ...func(*config.Config)) *harness {
	t.Helper()

	cfg := config.Default()
	cfg.Admin.BootstrapKey = testBootstrapKey
	// A tight limit keeps the oversize-body test from allocating megabytes.
	cfg.Limits.MaxRequestBytes = 4096
	for _, m := range mutate {
		m(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config is invalid: %v", err)
	}

	adapter := &fakeAdapter{models: defaultModels}
	mem := store.NewMemory(false)
	registry := provider.NewRegistry(adapter)

	cat := catalog.New(mem, registry, catalog.Options{
		TTL:             cfg.Catalog.TTL,
		StaleWarnAfter:  cfg.Catalog.StaleWarnAfter,
		ProviderTimeout: cfg.Catalog.ProviderTimeout,
	})
	resolver := routing.NewResolver(mem, cat, cfg.Routing.MaxFallbackDepth)
	breaker := routing.NewBreaker(
		cfg.Routing.Breaker.ErrorThreshold,
		cfg.Routing.Breaker.Window,
		cfg.Routing.Breaker.OpenDuration,
		nil,
	)
	events := &recordingEmitter{}

	// Discarding logs keeps `go test` output readable; the tests assert on
	// responses, which is the contract, not on log lines.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := obs.NewMetrics()
	// Telemetry writes back into the same in-memory store the request path reads
	// from, so a test can serve a completion and then assert the usage row it
	// produced — which is the only way the streaming path's accounting is
	// observable at all.
	telemetry := obs.NewTelemetry(mem, cfg.Telemetry.Buffer, logger, metrics)
	t.Cleanup(telemetry.Close)

	srv := New(Deps{
		Config:     cfg,
		Store:      mem,
		Catalog:    cat,
		Resolver:   resolver,
		Dispatcher: routing.NewDispatcher(resolver, breaker, registry, mem),
		Metrics:    metrics,
		Telemetry:  telemetry,
		Events:     events,
		Logger:     logger,
		// Semver, because GW-9 makes the published version a value a client may
		// parse — a fixture that could not be a real deployment's would let a
		// build ship a version no client can compare.
		Version: "1.0.0-test",
	})

	return &harness{t: t, srv: srv, mem: mem, adapter: adapter, events: events, telemetry: telemetry}
}

// flushTelemetry blocks until every usage record queued so far has reached the
// store.
//
// It works by stopping the dispatcher, which drains the queue on its way out, so
// it is terminal: nothing recorded after it will ever be persisted. Call it once,
// after the last request a test cares about.
func (h *harness) flushTelemetry() {
	h.t.Helper()
	h.telemetry.Close()
}

// --- request helpers --------------------------------------------------------

type reply struct {
	status int
	header http.Header
	body   []byte
}

// decode unmarshals the body into dst, failing the test rather than returning an
// error: a response that will not parse is never the thing under test.
func (r reply) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("response body is not JSON: %v\nbody: %s", err, r.body)
	}
}

// errorBody is the GW-7 envelope as a caller sees it.
type errorBody struct {
	Error struct {
		Message   string           `json:"message"`
		Type      string           `json:"type"`
		Code      string           `json:"code"`
		Param     *string          `json:"param"`
		RequestID string           `json:"request_id"`
		Attempts  []apierr.Attempt `json:"attempts"`
	} `json:"error"`
}

func (h *harness) do(method, path, token string, body any) reply {
	h.t.Helper()
	return h.doWithHeaders(method, path, token, body, nil)
}

func (h *harness) doWithHeaders(method, path, token string, body any, headers map[string]string) reply {
	h.t.Helper()

	var payload io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			payload = bytes.NewReader(v)
		case string:
			payload = bytes.NewReader([]byte(v))
		default:
			raw, err := json.Marshal(v)
			if err != nil {
				h.t.Fatalf("marshalling request body: %v", err)
			}
			payload = bytes.NewReader(raw)
		}
	}

	req := httptest.NewRequest(method, path, payload)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// -1 disables Fiber's test timeout. A handler that hangs should fail the
	// package's own deadline with a stack, not a bare timeout error here.
	res, err := h.srv.App().Test(req, -1)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("reading %s %s response: %v", method, path, err)
	}
	return reply{status: res.StatusCode, header: res.Header, body: raw}
}

// --- fixtures ---------------------------------------------------------------

// tenantFixture is a tenant with a data key, an admin key scoped to itself, and
// a provider whose models are in the catalog — the state most tests start from.
type tenantFixture struct {
	id       string
	dataKey  string
	adminKey string
}

func (h *harness) newTenant(name string) tenantFixture {
	h.t.Helper()

	res := h.do(http.MethodPost, "/admin/v1/tenants", testBootstrapKey,
		map[string]any{"name": name})
	if res.status != http.StatusCreated {
		h.t.Fatalf("creating tenant %q: status %d, body %s", name, res.status, res.body)
	}
	var tenant store.Tenant
	res.decode(h.t, &tenant)

	return tenantFixture{
		id:       tenant.ID,
		dataKey:  h.newKey(tenant.ID, store.PlaneData, ""),
		adminKey: h.newKey(tenant.ID, store.PlaneAdmin, ""),
	}
}

// newKey mints a key through the admin API and returns the plaintext, which is
// the only place it is ever readable.
func (h *harness) newKey(tenantID string, plane store.Plane, scope string) string {
	h.t.Helper()

	body := map[string]any{"name": "test", "plane": string(plane)}
	if scope != "" {
		body["scope"] = scope
	}
	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenantID+"/keys", testBootstrapKey, body)
	if res.status != http.StatusCreated {
		h.t.Fatalf("creating %s key: status %d, body %s", plane, res.status, res.body)
	}

	var out struct {
		Secret string `json:"secret"`
	}
	res.decode(h.t, &out)
	if out.Secret == "" {
		h.t.Fatal("key creation returned no secret")
	}
	return out.Secret
}

// addProvider registers an upstream so the tenant's catalog is non-empty.
func (h *harness) addProvider(tenantID, name string) {
	h.t.Helper()
	h.addProviderWithKeys(tenantID, name, "upstream-key-1")
}

// addProviderWithKeys is addProvider with an explicit credential pool, for the
// tests that care what happens when one key in the pool is rate limited.
func (h *harness) addProviderWithKeys(tenantID, name string, keys ...string) {
	h.t.Helper()

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenantID+"/providers", testBootstrapKey,
		map[string]any{
			"name":     name,
			"kind":     provider.KindOpenAI,
			"base_url": "https://upstream.invalid/v1",
			"keys":     keys,
		})
	if res.status != http.StatusCreated {
		h.t.Fatalf("creating provider %q: status %d, body %s", name, res.status, res.body)
	}
}

// recordUsage writes a usage row directly, bypassing the request path. Quota
// tests need usage to exist without having to serve traffic to create it.
func (h *harness) recordUsage(tenantID string, tokens int, costUSD float64) {
	h.t.Helper()

	err := h.mem.RecordUsage(context.Background(), &store.UsageRecord{
		RequestID:   store.NewID(store.IDRequest),
		TenantID:    tenantID,
		KeyPrefix:   "cg-testkey",
		Provider:    "test",
		Model:       "test-small",
		TotalTokens: tokens,
		CostUSD:     costUSD,
		StatusCode:  http.StatusOK,
		RecordedAt:  time.Now().UTC(),
	})
	if err != nil {
		h.t.Fatalf("recording usage: %v", err)
	}
}

// --- assertions -------------------------------------------------------------

// expectError asserts the status and the GW-7 code, and that the envelope is
// well formed. Every failure path in the gateway is supposed to look like this,
// so every negative test goes through here rather than checking a status alone.
func (h *harness) expectError(res reply, status int, code string) errorBody {
	h.t.Helper()

	var body errorBody
	res.decode(h.t, &body)

	if res.status != status {
		h.t.Fatalf("status = %d, want %d (code %q, body %s)", res.status, status, code, res.body)
	}
	if body.Error.Code != code {
		h.t.Fatalf("error code = %q, want %q (body %s)", body.Error.Code, code, res.body)
	}
	if body.Error.Message == "" {
		h.t.Errorf("error envelope has an empty message: %s", res.body)
	}
	if body.Error.Type == "" {
		h.t.Errorf("error envelope has an empty type: %s", res.body)
	}
	if body.Error.RequestID == "" {
		h.t.Errorf("error envelope carries no request id: %s", res.body)
	}
	return body
}
