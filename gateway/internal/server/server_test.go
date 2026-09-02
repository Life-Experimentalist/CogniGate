package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/routing"
)

// --- GW-6 two-plane authentication ------------------------------------------

func TestAuthRejectsMissingCredential(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/v1/models", "/v1/usage", "/admin/v1/tenants"} {
		res := h.do(http.MethodGet, path, "", nil)
		h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
	}
}

func TestAuthRejectsUnknownCredential(t *testing.T) {
	h := newHarness(t)

	// Correctly shaped and completely fictitious. It must be indistinguishable
	// from a revoked or expired key: telling a caller which it is confirms the
	// key once existed.
	res := h.do(http.MethodGet, "/v1/models", "cg-not-a-real-key-at-all", nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
}

// TestAuthWrongPlane pins the distinction that makes the two-plane split usable:
// a real key pointed at the wrong plane says so, rather than being answered as
// if it were a typo.
func TestAuthWrongPlane(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/v1/models", tenant.adminKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeWrongPlane)

	res = h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id, tenant.dataKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeWrongPlane)
}

func TestAuthRejectsRevokedKey(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var keys struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey, nil).
		decode(t, &keys)

	// The data key is the one created first, but find it by asking which id
	// authenticates rather than by position.
	var dataKeyID string
	for _, k := range keys.Data {
		res := h.do(http.MethodDelete,
			"/admin/v1/tenants/"+tenant.id+"/keys/"+k.ID, testBootstrapKey, nil)
		if res.status != http.StatusNoContent {
			t.Fatalf("revoking key %s: status %d, body %s", k.ID, res.status, res.body)
		}
		dataKeyID = k.ID
	}
	if dataKeyID == "" {
		t.Fatal("tenant fixture created no keys")
	}

	res := h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
}

// --- GW-9 surface -----------------------------------------------------------

// TestUnimplementedRouteIsNotSupported covers the rule that keeps the surface
// knowable: an OpenAI path CogniGate does not implement is an explicit 404, not
// a silent proxy to whatever the upstream happens to serve.
func TestUnimplementedRouteIsNotSupported(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, path := range []string{"/v1/embeddings", "/v1/audio/speech", "/v1/assistants"} {
		res := h.do(http.MethodPost, path, tenant.dataKey, map[string]any{})
		h.expectError(res, http.StatusNotFound, apierr.CodeNotSupported)
	}
}

// TestRoutingIsCaseSensitive pins the choice in New(): a second spelling of a
// documented path would be a second, undocumented API.
func TestRoutingIsCaseSensitive(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/V1/Models", tenant.dataKey, nil)
	h.expectError(res, http.StatusNotFound, apierr.CodeNotSupported)
}

func TestRequestTooLarge(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	// MaxRequestBytes is 4096 in the harness. This body is over that but well
	// under bodyLimitSlack, so fasthttp reads it and limitBody is what refuses
	// it — which is the point: the caller gets the GW-7 envelope and a request
	// id, not a transport error on a closed connection.
	oversize := `{"model":"test-small","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 8192) + `"}]}`

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, oversize)
	h.expectError(res, http.StatusRequestEntityTooLarge, apierr.CodeRequestTooLarge)
}

// --- request identity -------------------------------------------------------

func TestRequestIDIsAlwaysPresent(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	// On a success…
	res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	if got := res.header.Get(httpx.HeaderRequestID); got == "" {
		t.Error("successful response carries no request id header")
	}

	// …and on a failure raised before any handler ran.
	res = h.do(http.MethodGet, "/v1/models", "", nil)
	if got := res.header.Get(httpx.HeaderRequestID); got == "" {
		t.Error("unauthenticated response carries no request id header")
	}
}

func TestClientRequestIDIsEchoedAndBounded(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	req := func(value string) string {
		t.Helper()
		res := h.doWithHeaders(http.MethodGet, "/v1/meta", tenant.dataKey, nil,
			map[string]string{httpx.HeaderClientRequestID: value})
		return res.header.Get(httpx.HeaderClientRequestID)
	}

	if got := req("job-4417"); got != "job-4417" {
		t.Errorf("client request id = %q, want %q", got, "job-4417")
	}

	// Over the bound it is truncated rather than rejected: the header is a
	// convenience, and failing a completion over a long correlation id would be
	// a poor trade.
	long := strings.Repeat("a", httpx.MaxClientRequestID+50)
	if got := req(long); len(got) != httpx.MaxClientRequestID {
		t.Errorf("client request id length = %d, want %d", len(got), httpx.MaxClientRequestID)
	}

	// Control characters are stripped: this value is echoed into log lines.
	if got := req("job\n4417"); strings.ContainsAny(got, "\n\r") {
		t.Errorf("client request id was echoed with control characters: %q", got)
	}
}

// --- infrastructure endpoints -----------------------------------------------

func TestHealthzIsUnauthenticatedAndFailsWhileDraining(t *testing.T) {
	h := newHarness(t)

	res := h.do(http.MethodGet, "/healthz", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200 (body %s)", res.status, res.body)
	}

	// GW-11: a load balancer must stop sending work the moment a drain starts,
	// before in-flight requests have finished.
	h.srv.draining.Store(true)
	res = h.do(http.MethodGet, "/healthz", "", nil)
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("draining /healthz status = %d, want 503 (body %s)", res.status, res.body)
	}
	if !h.srv.Draining() {
		t.Error("Draining() disagrees with the drain flag")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	// A labelled Prometheus collector emits nothing at all — not even HELP and
	// TYPE — until a child with labels exists, so a scrape of a server that has
	// served no traffic is legitimately empty. One request is what makes the
	// request-scoped series real.
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)

	res := h.do(http.MethodGet, "/metrics", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", res.status)
	}
	// The normative series names from GW-8. A rename here is a breaking change
	// to every dashboard built on the gateway. The token and cost counters are
	// asserted where a completion actually records them, not here.
	for _, series := range []string{
		"cognigate_requests_total",
		"cognigate_request_duration_seconds",
	} {
		if !strings.Contains(string(res.body), series) {
			t.Errorf("/metrics does not expose %s", series)
		}
	}
}

func TestMetricsTokenIsEnforcedWhenConfigured(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Metrics.Token = "scrape-token"
	})

	res := h.do(http.MethodGet, "/metrics", "", nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)

	res = h.do(http.MethodGet, "/metrics", "scrape-token", nil)
	if res.status != http.StatusOK {
		t.Fatalf("/metrics with the configured token: status = %d, want 200", res.status)
	}
}

func TestMetricsCanBeDisabled(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Metrics.Enabled = false })

	// Not registered at all, so it falls through to the GW-9 catch-all.
	res := h.do(http.MethodGet, "/metrics", "", nil)
	h.expectError(res, http.StatusNotFound, apierr.CodeNotSupported)
}

// --- GW-9 /v1/meta ----------------------------------------------------------

func TestMetaDescribesTheImplementedSurface(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)

	if meta.Object != "meta" {
		t.Errorf("object = %q, want %q", meta.Object, "meta")
	}
	if meta.Mode != "server" {
		t.Errorf("mode = %q, want %q", meta.Mode, "server")
	}
	if meta.Store != "memory" {
		t.Errorf("store = %q, want %q", meta.Store, "memory")
	}

	// Every advertised endpoint must actually route. An endpoint listed here and
	// answered with not_supported is worse than one that was never advertised.
	for _, endpoint := range meta.Endpoints {
		method, path, ok := strings.Cut(endpoint, " ")
		if !ok {
			t.Errorf("endpoint %q is not \"METHOD /path\"", endpoint)
			continue
		}
		if strings.Contains(path, "{") {
			continue // templated; covered by the models tests
		}
		res := h.do(method, path, tenant.dataKey, map[string]any{})
		if res.status == http.StatusNotFound {
			var body errorBody
			res.decode(t, &body)
			if body.Error.Code == apierr.CodeNotSupported {
				t.Errorf("%s is advertised by /v1/meta but answers not_supported", endpoint)
			}
		}
	}
}

// --- GW-1 catalog over HTTP -------------------------------------------------

func TestModelsListsCatalogAndAliases(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "test")

	var list modelList
	h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil).decode(t, &list)

	if list.Object != "list" {
		t.Errorf("object = %q, want %q", list.Object, "list")
	}

	byID := map[string]modelObject{}
	for _, m := range list.Data {
		byID[m.ID] = m
	}

	for _, id := range []string{"test-small", "test-large"} {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("model %q missing from /v1/models: %+v", id, list.Data)
		}
		if m.Object != "model" {
			t.Errorf("%s object = %q, want %q", id, m.Object, "model")
		}
		if m.CogniGate.Alias {
			t.Errorf("%s is marked as an alias", id)
		}
		// GW-1.AC-1 names owned_by and cognigate.provider specifically: they are
		// what tells a caller which account a model is billed to.
		if m.OwnedBy == "" || m.CogniGate.Provider == "" {
			t.Errorf("%s: owned_by = %q, cognigate.provider = %q, want both set",
				id, m.OwnedBy, m.CogniGate.Provider)
		}
		if m.CogniGate.DiscoveredAt == "" {
			t.Errorf("%s has no cognigate.discovered_at", id)
		}
	}

	// The GW-2 seeded names are listed alongside real models, which is what makes
	// them discoverable to a caller choosing from a list.
	for _, name := range []string{"fast", "balanced", "best"} {
		m, ok := byID[name]
		if !ok {
			t.Fatalf("seeded alias %q missing from /v1/models", name)
		}
		if !m.CogniGate.Alias {
			t.Errorf("alias %q is not marked as an alias", name)
		}
		if m.CogniGate.ResolvesTo == "" {
			t.Errorf("alias %q resolved to nothing against a populated catalog", name)
		}
	}
}

func TestGetModel(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "test")

	var model modelObject
	h.do(http.MethodGet, "/v1/models/test-large", tenant.dataKey, nil).decode(t, &model)
	if model.ID != "test-large" {
		t.Errorf("id = %q, want %q", model.ID, "test-large")
	}
	if model.CogniGate.ContextWindow != 128000 {
		t.Errorf("cognigate.context_window = %d, want 128000", model.CogniGate.ContextWindow)
	}

	// The provider-qualified form is addressable, so a caller can pin which
	// account serves a model id two providers both offer.
	h.do(http.MethodGet, "/v1/models/test/test-large", tenant.dataKey, nil).decode(t, &model)
	if model.ID != "test/test-large" {
		t.Errorf("qualified id = %q, want %q", model.ID, "test/test-large")
	}

	// An alias resolves here too: this is how a caller finds out what "fast"
	// currently means.
	h.do(http.MethodGet, "/v1/models/fast", tenant.dataKey, nil).decode(t, &model)
	if !model.CogniGate.Alias || model.CogniGate.ResolvesTo == "" {
		t.Errorf("GET /v1/models/fast did not resolve the alias: %+v", model)
	}

	res := h.do(http.MethodGet, "/v1/models/no-such-model", tenant.dataKey, nil)
	body := h.expectError(res, http.StatusNotFound, apierr.CodeModelNotFound)
	if body.Error.Param == nil || *body.Error.Param != "model" {
		t.Errorf("model_not_found should blame the model param, got %v", body.Error.Param)
	}
}

// TestModelsAreTenantIsolated is the isolation guarantee at its most visible: a
// provider registered by one tenant must not appear in another's catalog.
func TestModelsAreTenantIsolated(t *testing.T) {
	h := newHarness(t)
	withProvider := h.newTenant("acme")
	without := h.newTenant("globex")
	h.addProvider(withProvider.id, "test")

	var list modelList
	h.do(http.MethodGet, "/v1/models", without.dataKey, nil).decode(t, &list)

	for _, m := range list.Data {
		if !m.CogniGate.Alias {
			t.Errorf("tenant with no providers sees model %q", m.ID)
		}
	}
}

// --- GW-8 usage -------------------------------------------------------------

func TestUsageWindowValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, window := range []string{"day", "month"} {
		var out usageResponse
		h.do(http.MethodGet, "/v1/usage?window="+window, tenant.dataKey, nil).decode(t, &out)
		if out.Window != window {
			t.Errorf("window = %q, want %q", out.Window, window)
		}
		if out.Object != "usage" {
			t.Errorf("object = %q, want %q", out.Object, "usage")
		}
	}

	res := h.do(http.MethodGet, "/v1/usage?window=fortnight", tenant.dataKey, nil)
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	if body.Error.Param == nil || *body.Error.Param != "window" {
		t.Errorf("invalid window should blame the window param, got %v", body.Error.Param)
	}
}

func TestUsageBreakdownGroupBy(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.recordUsage(tenant.id, 300, 0.02)

	for _, groupBy := range []string{"model", "provider", "key"} {
		var out breakdownResponse
		h.do(http.MethodGet, "/v1/usage/breakdown?group_by="+groupBy, tenant.dataKey, nil).
			decode(t, &out)
		if out.GroupBy != groupBy {
			t.Errorf("group_by = %q, want %q", out.GroupBy, groupBy)
		}
		if len(out.Data) != 1 {
			t.Fatalf("group_by=%s returned %d buckets, want 1", groupBy, len(out.Data))
		}
		if out.Data[0].TotalTokens != 300 {
			t.Errorf("group_by=%s total_tokens = %d, want 300", groupBy, out.Data[0].TotalTokens)
		}
	}

	res := h.do(http.MethodGet, "/v1/usage/breakdown?group_by=colour", tenant.dataKey, nil)
	h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
}

// TestUsageIsTenantIsolated guards the number a customer is billed on.
func TestUsageIsTenantIsolated(t *testing.T) {
	h := newHarness(t)
	spender := h.newTenant("acme")
	other := h.newTenant("globex")
	h.recordUsage(spender.id, 1000, 1.25)

	var out usageResponse
	h.do(http.MethodGet, "/v1/usage", other.dataKey, nil).decode(t, &out)
	if out.TotalTokens != 0 || out.CostUSD != 0 {
		t.Errorf("tenant sees another tenant's usage: %+v", out.UsageTotals)
	}

	h.do(http.MethodGet, "/v1/usage", spender.dataKey, nil).decode(t, &out)
	if out.TotalTokens != 1000 {
		t.Errorf("total_tokens = %d, want 1000", out.TotalTokens)
	}
}

// --- GW-5 health ------------------------------------------------------------

func TestHealthReportsCatalogAndStore(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "test")

	var report healthReport
	h.do(http.MethodGet, "/v1/health", tenant.dataKey, nil).decode(t, &report)

	if report.Status != "ok" {
		t.Errorf("status = %q, want %q (report %+v)", report.Status, "ok", report)
	}
	if !report.Store.Reachable {
		t.Error("in-memory store reported unreachable")
	}
	if report.Catalog.Models != 2 {
		t.Errorf("catalog models = %d, want 2", report.Catalog.Models)
	}
	if len(report.Providers) != 1 || report.Providers[0].Name != "test" {
		t.Errorf("providers = %+v, want one named \"test\"", report.Providers)
	}
}

// TestHealthDegradesWhenProvidersFail pins the GW-5 rule that a degraded gateway
// still answers 200: returning 503 here would have every orchestrator restart a
// process whose only problem is one unreachable provider.
func TestHealthDegradesButStillAnswers200(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "test")

	// Populate the catalog, then break the upstream and force a refresh.
	h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil)
	h.adapter.listErr = errTestUpstreamDown
	h.srv.Catalog.Invalidate(tenant.id)

	res := h.do(http.MethodGet, "/v1/health", tenant.dataKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("/v1/health status = %d, want 200 even when degraded", res.status)
	}
	var report healthReport
	res.decode(t, &report)
	if report.Status != "degraded" {
		t.Errorf("status = %q, want %q (report %+v)", report.Status, "degraded", report)
	}
}

// TestHealthReportsOnlyTheCallersBreakers pins the tenant isolation of both the
// breaker and the health report.
//
// Provider names are chosen per tenant, so "test" here names two unrelated
// upstreams with different credentials. Two things must hold: one tenant's
// outage must not trip the other's breaker, and one tenant must not be able to
// read the other's provider names and failures out of a health response.
func TestHealthReportsOnlyTheCallersBreakers(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	globex := h.newTenant("globex")
	h.addProvider(acme.id, "test")
	h.addProvider(globex.id, "test")

	// Take acme's provider out of rotation, exactly as a run of upstream
	// failures on the request path would.
	breaker := h.srv.Dispatcher.Breaker()
	key := routing.Key(acme.id, "test", "test-small")
	for i := 0; i < h.srv.Config.Routing.Breaker.ErrorThreshold; i++ {
		breaker.Allow(key)
		breaker.Failure(key)
	}
	if got := breaker.State(key); got != routing.StateOpen {
		t.Fatalf("breaker state = %v, want open; the rest of this test proves nothing", got)
	}

	// The isolation itself, independent of how health reports it: globex named
	// its provider "test" too, and its breaker must be untouched.
	theirKey := routing.Key(globex.id, "test", "test-small")
	if got := breaker.State(theirKey); got != routing.StateClosed {
		t.Errorf("globex breaker state = %v, want closed; the two tenants share a breaker entry", got)
	}
	if !breaker.Allow(theirKey) {
		t.Error("globex is being skipped by a breaker that acme's failures opened")
	}

	var mine healthReport
	h.do(http.MethodGet, "/v1/health", acme.dataKey, nil).decode(t, &mine)
	if mine.Status != "degraded" {
		t.Errorf("acme status = %q, want %q", mine.Status, "degraded")
	}
	// Reported without the tenant segment, so the key reads the same way as
	// X-CogniGate-Served-By.
	if state, ok := mine.Breakers["test/test-small"]; !ok || state != "open" {
		t.Errorf("acme breakers = %v, want test/test-small open", mine.Breakers)
	}

	var theirs healthReport
	h.do(http.MethodGet, "/v1/health", globex.dataKey, nil).decode(t, &theirs)
	if len(theirs.Breakers) != 0 {
		t.Errorf("globex sees %v; another tenant's breakers must not be disclosed", theirs.Breakers)
	}
	if theirs.Status != "ok" {
		t.Errorf("globex status = %q, want %q; another tenant's outage must not degrade it",
			theirs.Status, "ok")
	}
}
