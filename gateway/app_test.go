package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildDev assembles a dev-mode process, as `cognigate --dev` does.
func buildDev(t *testing.T) *app {
	t.Helper()
	cfg := config.Default()
	applyDevDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev configuration is invalid: %v", err)
	}
	a, err := build(cfg, true, testLogger(), "test")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(a.Close)
	return a
}

// call drives the assembled server the way a client would. -1 disables Fiber's
// own test timeout, which would otherwise cut short anything that blocks.
func call(t *testing.T, a *app, method, path, token string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding the request body: %v", err)
		}
		r = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := a.server.App().Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

func decode(t *testing.T, res *http.Response, into any) {
	t.Helper()
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
}

// --- GW-11 dev mode ---------------------------------------------------------

// TestDevMintsWorkingKeysOnBothPlanes is the load-bearing test for `--dev`: the
// promise is a single process that exercises the whole product, and a printed
// key that does not authenticate makes the mode useless.
func TestDevMintsWorkingKeysOnBothPlanes(t *testing.T) {
	a := buildDev(t)

	if a.dev == nil {
		t.Fatal("dev mode minted no credentials")
	}
	if !strings.HasPrefix(a.dev.DataKey, "cg-dev-") {
		t.Errorf("data key %q does not carry the dev marker", redact(a.dev.DataKey))
	}
	if !strings.HasPrefix(a.dev.AdminKey, "cga-dev-") {
		t.Errorf("admin key %q does not carry the dev marker", redact(a.dev.AdminKey))
	}

	if res := call(t, a, http.MethodGet, "/v1/models", a.dev.DataKey, nil); res.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/models with the dev data key = %d, want 200", res.StatusCode)
	}
	res := call(t, a, http.MethodGet, "/admin/v1/tenants/"+a.dev.TenantID, a.dev.AdminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET the dev tenant with the dev admin key = %d, want 200", res.StatusCode)
	}
}

// The seeded admin key is scoped to its own tenant, not root. Dev mode is only
// worth having if the credential it hands out behaves like one a deployment
// would issue, and a root key would quietly exercise a different code path from
// the one most operators run.
func TestDevAdminKeyIsScopedToItsOwnTenant(t *testing.T) {
	a := buildDev(t)

	res := call(t, a, http.MethodGet, "/admin/v1/tenants", a.dev.AdminKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("listing every tenant with a tenant-scoped key = %d, want 403", res.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, res, &body)
	if body.Error.Code != "insufficient_scope" {
		t.Errorf("error code = %q, want %q", body.Error.Code, "insufficient_scope")
	}
}

// The two planes must stay separated in dev exactly as in a deployment,
// otherwise dev mode is not exercising the product's authentication at all.
func TestDevKeysDoNotCrossPlanes(t *testing.T) {
	a := buildDev(t)

	res := call(t, a, http.MethodGet, "/admin/v1/tenants", a.dev.DataKey, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("data key on the admin plane = %d, want 401", res.StatusCode)
	}
	res = call(t, a, http.MethodGet, "/v1/models", a.dev.AdminKey, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("admin key on the data plane = %d, want 401", res.StatusCode)
	}
}

func TestDevStoreIsSelfIdentifying(t *testing.T) {
	a := buildDev(t)
	if got := a.store.Kind(); got != "memory-dev" {
		t.Errorf("store kind = %q, want %q", got, "memory-dev")
	}
}

// The dev bootstrap key is documented and fixed, so it must actually work —
// and it must be long enough to clear the gateway's own floor on bootstrap
// credentials, which silently rejects anything shorter.
func TestDevBootstrapKeyReachesTheAdminPlane(t *testing.T) {
	a := buildDev(t)

	res := call(t, a, http.MethodGet, "/admin/v1/tenants", devBootstrapKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("the documented dev bootstrap key = %d, want 200", res.StatusCode)
	}
}

// An operator's own bootstrap key must survive dev mode; silently replacing a
// configured credential would be surprising in the one direction that matters.
func TestDevDefaultsDoNotOverrideAConfiguredBootstrapKey(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.BootstrapKey = "an-operators-own-bootstrap-key"
	applyDevDefaults(&cfg)

	if cfg.Admin.BootstrapKey != "an-operators-own-bootstrap-key" {
		t.Errorf("bootstrap key = %q; dev defaults overwrote a configured value", cfg.Admin.BootstrapKey)
	}
}

func TestDevBannerDoesNotLeakIntoProduction(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.BootstrapKey = "a-long-enough-bootstrap-key"
	a, err := build(cfg, false, testLogger(), "test")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()

	if a.dev != nil {
		t.Error("a non-dev process minted credentials at boot")
	}
	if got := a.store.Kind(); got != "memory" {
		t.Errorf("store kind = %q, want %q", got, "memory")
	}
}

// --- wiring -----------------------------------------------------------------

// TestBreakerEventsReachASubscribedWebhook proves the composition root actually
// connected the breaker to the dispatcher. The hook and the dispatcher are each
// tested in their own package; what can only be tested here is whether main
// passed one to the other.
func TestBreakerEventsReachASubscribedWebhook(t *testing.T) {
	a := buildDev(t)

	delivered := make(chan []byte, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case delivered <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()

	if _, err := a.store.CreateWebhook(context.Background(), &store.Webhook{
		TenantID: a.dev.TenantID,
		URL:      endpoint.URL,
		Secret:   "a-test-webhook-secret",
		Events:   []string{"breaker.opened"},
		Enabled:  true,
	}); err != nil {
		t.Fatalf("registering the webhook: %v", err)
	}

	// Take a provider out of rotation, as a run of upstream failures would.
	breaker := a.server.Dispatcher.Breaker()
	key := routing.Key(a.dev.TenantID, "primary", "gpt-4o")
	for i := 0; i < a.cfg.Routing.Breaker.ErrorThreshold; i++ {
		breaker.Allow(key)
		breaker.Failure(key)
	}

	select {
	case body := <-delivered:
		var envelope struct {
			Type   string         `json:"type"`
			Tenant string         `json:"tenant"`
			Data   map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("the delivered body is not an event envelope: %v", err)
		}
		if envelope.Type != "breaker.opened" {
			t.Errorf("event type = %q, want %q", envelope.Type, "breaker.opened")
		}
		if envelope.Tenant != a.dev.TenantID {
			t.Errorf("tenant = %q, want %q", envelope.Tenant, a.dev.TenantID)
		}
		if got := envelope.Data["provider"]; got != "primary" {
			t.Errorf("data.provider = %v, want %q", got, "primary")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no breaker.opened delivery; the breaker's onChange is not wired to the dispatcher")
	}
}

// GW-11.AC-3: a configured analytics service moves the usage plane there. The
// gateway must not dial it at boot — a compose deployment starts both containers
// at once, and a gateway that waited would be down for as long as its dependency
// took to come up.
func TestBuildMovesUsageToAConfiguredAnalyticsService(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.BootstrapKey = "a-long-enough-bootstrap-key"
	// Deliberately not listening: build must succeed against a service that is
	// not up yet.
	cfg.Analytics.BaseURL = "http://analytics.invalid:8081"

	a, err := build(cfg, false, testLogger(), "test")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer a.Close()

	// "analytics" alone would tell an operator the deployment is durable when a
	// restart still loses every tenant and key.
	if got := a.store.Kind(); got != "memory+analytics" {
		t.Errorf("store kind = %q, want %q", got, "memory+analytics")
	}
}

// A base URL the client cannot use is a misconfiguration, not a degraded mode:
// there is no address to buffer towards, so the failure has to be at boot and it
// has to name the setting at fault.
func TestBuildRefusesAnUnusableAnalyticsBaseURL(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.BootstrapKey = "a-long-enough-bootstrap-key"
	cfg.Analytics.BaseURL = "analytics:8081"

	a, err := build(cfg, false, testLogger(), "test")
	if err == nil {
		a.Close()
		t.Fatal("build accepted an analytics base URL the client cannot use")
	}
	if !strings.Contains(err.Error(), "analytics.base_url") {
		t.Errorf("error %q does not name the setting at fault", err)
	}
}

// --- shutdown ---------------------------------------------------------------

func TestShutdownIsCleanAndRepeatable(t *testing.T) {
	a := buildDev(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// The deferred Close in buildDev runs after this and must not panic on an
	// already-stopped process; that is the whole assertion.
	a.Close()
}

// --- flags ------------------------------------------------------------------

func TestVersionFlagPrintsAndExits(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != version {
		t.Errorf("stdout = %q, want %q", stdout.String(), version)
	}
}

func TestUnknownFlagIsAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--not-a-flag"}, &stdout, &stderr); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// redact keeps a failing assertion's message from carrying a whole credential
// into CI output, even a throwaway one.
func redact(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "…"
}
