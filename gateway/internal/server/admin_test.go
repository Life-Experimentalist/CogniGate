package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/store"
)

// expectParam asserts the envelope names the field the caller has to fix.
//
// A 400 that does not say which parameter is wrong leaves the caller diffing
// their request against the documentation by hand, which is the failure mode
// GW-7's param field exists to prevent.
func expectParam(t *testing.T, body errorBody, want string) {
	t.Helper()
	if body.Error.Param == nil {
		t.Errorf("error envelope names no param, want %q", want)
		return
	}
	if *body.Error.Param != want {
		t.Errorf("error param = %q, want %q", *body.Error.Param, want)
	}
}

// --- GW-6 scope enforcement -------------------------------------------------

// TestAdminScopeReachesOwnTenant is the positive control for every refusal below
// it. Without it, a bug that refused everything would leave the whole scope
// suite passing.
func TestAdminScopeReachesOwnTenant(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, path := range []string{
		"/admin/v1/tenants/" + tenant.id,
		"/admin/v1/tenants/" + tenant.id + "/keys",
		"/admin/v1/tenants/" + tenant.id + "/providers",
		"/admin/v1/tenants/" + tenant.id + "/aliases",
		"/admin/v1/tenants/" + tenant.id + "/routes",
		"/admin/v1/tenants/" + tenant.id + "/webhooks",
		"/admin/v1/tenants/" + tenant.id + "/usage",
	} {
		res := h.do(http.MethodGet, path, tenant.adminKey, nil)
		if res.status != http.StatusOK {
			t.Errorf("GET %s with own admin key: status %d, want 200 (body %s)",
				path, res.status, res.body)
		}
	}
}

// TestAdminScopeRefusesForeignTenant is the boundary the admin plane exists to
// hold: a tenant's own administrator is still confined to that tenant.
func TestAdminScopeRefusesForeignTenant(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/admin/v1/tenants/" + other.id, nil},
		{http.MethodGet, "/admin/v1/tenants/" + other.id + "/keys", nil},
		{http.MethodPost, "/admin/v1/tenants/" + other.id + "/keys",
			map[string]any{"name": "stolen", "plane": "data"}},
		{http.MethodGet, "/admin/v1/tenants/" + other.id + "/providers", nil},
		{http.MethodGet, "/admin/v1/tenants/" + other.id + "/usage", nil},
		{http.MethodPut, "/admin/v1/tenants/" + other.id + "/quota",
			map[string]any{"period": "month", "token_limit": 1}},
		{http.MethodDelete, "/admin/v1/tenants/" + other.id + "/quota", nil},
	} {
		res := h.do(tc.method, tc.path, acme.adminKey, tc.body)
		h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
	}
}

// TestAdminScopeRefusesTenantLifecycle covers the routes guarded by requireRoot
// rather than by tenantScope. A tenant admin creating a second tenant would have
// escaped the boundary its key was issued inside — and deleting its own tenant
// is refused for the same reason, even though the scope nominally covers it.
func TestAdminScopeRefusesTenantLifecycle(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants", tenant.adminKey,
		map[string]any{"name": "sneaky"})
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)

	res = h.do(http.MethodGet, "/admin/v1/tenants", tenant.adminKey, nil)
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)

	res = h.do(http.MethodDelete, "/admin/v1/tenants/"+tenant.id, tenant.adminKey, nil)
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
}

// TestAdminRootReachesEveryTenant is the other half of the scope rule: the root
// credential is not confined, which is what makes an operator able to support a
// tenant without holding that tenant's keys.
func TestAdminRootReachesEveryTenant(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")

	for _, id := range []string{acme.id, other.id} {
		res := h.do(http.MethodGet, "/admin/v1/tenants/"+id, testBootstrapKey, nil)
		if res.status != http.StatusOK {
			t.Errorf("root reading tenant %s: status %d, body %s", id, res.status, res.body)
		}
	}
}

// TestAdminMetaReportsScope pins what a caller uses to discover what its own
// credential can do, and the closed event registry a webhook may subscribe to.
func TestAdminMetaReportsScope(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var meta struct {
		Scope  string   `json:"scope"`
		Events []string `json:"events"`
	}
	h.do(http.MethodGet, "/admin/v1/meta", testBootstrapKey, nil).decode(t, &meta)
	if meta.Scope != store.ScopeRoot {
		t.Errorf("bootstrap scope = %q, want %q", meta.Scope, store.ScopeRoot)
	}
	if len(meta.Events) != len(eventRegistry) {
		t.Errorf("meta advertises %d event types, want %d", len(meta.Events), len(eventRegistry))
	}

	h.do(http.MethodGet, "/admin/v1/meta", tenant.adminKey, nil).decode(t, &meta)
	if want := "tenant:" + tenant.id; meta.Scope != want {
		t.Errorf("tenant admin scope = %q, want %q", meta.Scope, want)
	}
}

// --- GW-6 key issuance ------------------------------------------------------

// TestCreateKeyRefusesPrivilegeEscalation is the single most important refusal
// on this plane: if a tenant admin could mint a root key, the tenant boundary
// would be advisory.
func TestCreateKeyRefusesPrivilegeEscalation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", tenant.adminKey,
		map[string]any{"name": "escalate", "plane": "admin", "scope": store.ScopeRoot})
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
}

// TestCreateKeyRejectsRootOnDataPlane covers the request that is incoherent
// rather than merely unauthorised: a data key carries no scope at all, so asking
// for a root one is a request that cannot be satisfied even by root.
func TestCreateKeyRejectsRootOnDataPlane(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey,
		map[string]any{"name": "confused", "plane": "data", "scope": store.ScopeRoot})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "scope")
}

// TestCreateKeyRejectsForeignScope pins the refusal rather than a silent
// downgrade. Quietly issuing a tenant-scoped key to a caller who asked for
// something else hands back a credential that does less than they believe, and
// the mistake surfaces later as an unexplained 403.
func TestCreateKeyRejectsForeignScope(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+acme.id+"/keys", testBootstrapKey,
		map[string]any{"name": "crosswired", "plane": "admin", "scope": "tenant:" + other.id})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "scope")
}

func TestCreateKeyRejectsUnknownPlane(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey,
		map[string]any{"name": "nope", "plane": "control"})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "plane")
}

// TestCreateKeyRejectsPastExpiry refuses a key that is dead on arrival. Storing
// it would produce a credential that authenticates nowhere and an operator
// hunting for why.
func TestCreateKeyRejectsPastExpiry(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey,
		map[string]any{"name": "expired", "plane": "data", "expires_at": "2020-01-01T00:00:00Z"})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "expires_at")
}

// TestRootCanMintRootKey is the positive control for the escalation refusal:
// root delegating root is legitimate, and a bug that refused it would leave the
// deployment unable to issue a second operator credential.
func TestRootCanMintRootKey(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey,
		map[string]any{"name": "operator", "plane": "admin", "scope": store.ScopeRoot})
	if res.status != http.StatusCreated {
		t.Fatalf("root minting a root key: status %d, body %s", res.status, res.body)
	}
	var out struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	res.decode(t, &out)
	if out.Key.Scope != store.ScopeRoot {
		t.Fatalf("minted key scope = %q, want %q", out.Key.Scope, store.ScopeRoot)
	}

	// The proof that the scope is real and not merely recorded.
	if got := h.do(http.MethodGet, "/admin/v1/tenants", out.Secret, nil); got.status != http.StatusOK {
		t.Errorf("minted root key listing tenants: status %d, body %s", got.status, got.body)
	}
}

// TestKeySecretIsReturnedExactlyOnce pins the property that makes a stolen
// database useless: the plaintext exists in one response and nowhere else, and
// the hash is never serialised outward.
func TestKeySecretIsReturnedExactlyOnce(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey,
		map[string]any{"name": "app", "plane": "data"})
	if res.status != http.StatusCreated {
		t.Fatalf("creating key: status %d, body %s", res.status, res.body)
	}
	var created struct {
		Secret string `json:"secret"`
	}
	res.decode(t, &created)
	if !strings.HasPrefix(created.Secret, store.DataKeyPrefix) {
		t.Fatalf("secret %q does not carry the data-plane prefix", created.Secret)
	}
	if got := h.do(http.MethodGet, "/v1/meta", created.Secret, nil); got.status != http.StatusOK {
		t.Fatalf("freshly minted key does not authenticate: status %d", got.status)
	}

	// Listing keys must never reproduce it — nor the hash, which would let an
	// admin-plane reader mount an offline attack on every key at once.
	listed := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/keys", testBootstrapKey, nil)
	if strings.Contains(string(listed.body), created.Secret) {
		t.Error("listing keys returned a key secret")
	}
	if strings.Contains(string(listed.body), `"hash"`) {
		t.Errorf("listing keys exposed the stored hash: %s", listed.body)
	}
}

// --- GW-6 secret material ---------------------------------------------------

// TestAdminAPINeverReturnsSecretMaterial sweeps the three places a secret is
// held. Each is tagged json:"-" in the store types; this is the test that
// notices when one of those tags is dropped in a refactor.
func TestAdminAPINeverReturnsSecretMaterial(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	base := "/admin/v1/tenants/" + tenant.id

	const providerKey = "upstream-key-should-never-be-echoed"
	res := h.do(http.MethodPost, base+"/providers", testBootstrapKey, map[string]any{
		"name": "openai", "kind": "openai",
		"base_url": "https://upstream.invalid/v1",
		"keys":     []string{providerKey},
	})
	if res.status != http.StatusCreated {
		t.Fatalf("creating provider: status %d, body %s", res.status, res.body)
	}
	if strings.Contains(string(res.body), providerKey) {
		t.Errorf("provider creation echoed the upstream credential: %s", res.body)
	}

	const hookSecret = "webhook-signing-secret-0123456789"
	res = h.do(http.MethodPost, base+"/webhooks", testBootstrapKey, map[string]any{
		"url":    "https://hooks.invalid/cognigate",
		"secret": hookSecret,
		"events": []string{"quota.hard_cap_reached"},
	})
	if res.status != http.StatusCreated {
		t.Fatalf("creating webhook: status %d, body %s", res.status, res.body)
	}
	if strings.Contains(string(res.body), hookSecret) {
		t.Errorf("webhook creation echoed the signing secret: %s", res.body)
	}

	for _, path := range []string{base + "/providers", base + "/webhooks"} {
		body := string(h.do(http.MethodGet, path, testBootstrapKey, nil).body)
		if strings.Contains(body, providerKey) || strings.Contains(body, hookSecret) {
			t.Errorf("GET %s leaked secret material: %s", path, body)
		}
	}
}

// --- GW-2 aliases -----------------------------------------------------------

// TestSeededAliasesExist covers the promise that a fresh tenant can route
// somewhere before anyone has configured anything.
func TestSeededAliasesExist(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var list struct {
		Data []store.Alias `json:"data"`
	}
	h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/aliases", tenant.adminKey, nil).
		decode(t, &list)

	seeded := map[string]bool{}
	for _, a := range list.Data {
		seeded[a.Name] = true
	}
	for _, want := range []string{"fast", "balanced", "best", "transcribe"} {
		if !seeded[want] {
			t.Errorf("new tenant is missing the seeded alias %q", want)
		}
	}
}

// TestAliasNameValidation keeps alias names to the portable shape in GW-2. They
// travel in the model field of an OpenAI request, so anything that needs
// escaping there is a name that will break somebody's client.
func TestAliasNameValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, name := range []string{
		"Fast",                  // upper case
		"1fast",                 // leading digit
		"f",                     // below the two-character floor
		"fast.model",            // dot is not in the character class
		strings.Repeat("a", 65), // past the 64-character ceiling
	} {
		res := h.do(http.MethodPut,
			"/admin/v1/tenants/"+tenant.id+"/aliases/"+name, tenant.adminKey,
			map[string]any{"cost_tier": "cheapest"})
		body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
		expectParam(t, body, "name")
	}
}

func TestAliasCostTierValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/aliases/thrifty",
		tenant.adminKey, map[string]any{"cost_tier": "cheap"})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "cost_tier")
}

// TestAliasCollidesWithModel pins the refusal that keeps a model id meaning one
// thing. An alias shadowing a real id would make the same request resolve
// differently depending on catalog state.
func TestAliasCollidesWithModel(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "openai")

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/aliases/test-small",
		tenant.adminKey, map[string]any{"cost_tier": "cheapest"})
	h.expectError(res, http.StatusConflict, apierr.CodeAliasCollides)
}

// TestAliasUpsertIsIdempotent covers PUT meaning what PUT means: the same name
// written twice is one alias with the second body, not a duplicate.
func TestAliasUpsertIsIdempotent(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	base := "/admin/v1/tenants/" + tenant.id + "/aliases"

	for _, tier := range []string{"cheapest", "best"} {
		res := h.do(http.MethodPut, base+"/house-default", tenant.adminKey,
			map[string]any{"cost_tier": tier})
		if res.status != http.StatusOK {
			t.Fatalf("upserting alias with tier %q: status %d, body %s", tier, res.status, res.body)
		}
	}

	var list struct {
		Data []store.Alias `json:"data"`
	}
	h.do(http.MethodGet, base, tenant.adminKey, nil).decode(t, &list)

	var found int
	for _, a := range list.Data {
		if a.Name != "house-default" {
			continue
		}
		found++
		if a.CostTier != "best" {
			t.Errorf("alias cost_tier = %q, want the second write's %q", a.CostTier, "best")
		}
	}
	if found != 1 {
		t.Errorf("alias written twice appears %d times, want 1", found)
	}
}

func TestDeleteUnknownAliasIsNotFound(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodDelete,
		"/admin/v1/tenants/"+tenant.id+"/aliases/never-existed", tenant.adminKey, nil)
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)
}

// --- GW-3 fallback chains ---------------------------------------------------

// TestRouteChainRejectsDuplicate refuses a chain that would retry a model that
// has already failed. The cascade would burn a fallback step on an upstream it
// has just seen fail, which is latency spent to reach the same answer.
func TestRouteChainRejectsDuplicate(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/routes", tenant.adminKey,
		map[string]any{"match": "gpt-4o", "chain": []string{"test-small", "test-large", "test-small"}})
	h.expectError(res, http.StatusBadRequest, apierr.CodeFallbackDuplicate)
}

// TestRouteChainRejectsOverDepth refuses at configuration time what the
// dispatcher would refuse at request time. Storing a chain the gateway will
// never walk to the end of is a rule that silently does less than it says.
func TestRouteChainRejectsOverDepth(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	chain := make([]string, h.srv.Config.Routing.MaxFallbackDepth+1)
	for i := range chain {
		chain[i] = "model-" + string(rune('a'+i))
	}

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/routes", tenant.adminKey,
		map[string]any{"match": "gpt-4o", "chain": chain})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "chain")
}

func TestRouteRequiresMatchAndChain(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	path := "/admin/v1/tenants/" + tenant.id + "/routes"

	res := h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"match": "  ", "chain": []string{"test-small"}})
	expectParam(t, h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest), "match")

	// An entry that is only whitespace is dropped, so this is an empty chain
	// rather than a one-entry one.
	res = h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"match": "gpt-4o", "chain": []string{"  "}})
	expectParam(t, h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest), "chain")
}

func TestRouteRoundTrips(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	path := "/admin/v1/tenants/" + tenant.id + "/routes"

	res := h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"match": "gpt-4o", "chain": []string{"test-large", "test-small"}})
	if res.status != http.StatusOK {
		t.Fatalf("creating route: status %d, body %s", res.status, res.body)
	}

	var list struct {
		Data []store.Route `json:"data"`
	}
	h.do(http.MethodGet, path, tenant.adminKey, nil).decode(t, &list)
	if len(list.Data) != 1 {
		t.Fatalf("listed %d routes, want 1", len(list.Data))
	}
	// Chain order is the whole meaning of a chain: reversing it changes which
	// upstream serves the request in the common case.
	if got := list.Data[0].Chain; len(got) != 2 || got[0] != "test-large" || got[1] != "test-small" {
		t.Errorf("stored chain = %v, want [test-large test-small]", got)
	}
}

// --- GW-6 providers ---------------------------------------------------------

func TestProviderValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	path := "/admin/v1/tenants/" + tenant.id + "/providers"

	for _, tc := range []struct {
		name  string
		body  map[string]any
		param string
	}{
		{
			name:  "no name",
			body:  map[string]any{"base_url": "https://upstream.invalid/v1", "keys": []string{"k"}},
			param: "name",
		},
		{
			name:  "relative base_url",
			body:  map[string]any{"name": "openai", "base_url": "upstream.invalid/v1", "keys": []string{"k"}},
			param: "base_url",
		},
		{
			// A non-http scheme would have the dispatcher construct a request it
			// cannot send, turning a configuration mistake into a runtime 502.
			name:  "non-http base_url",
			body:  map[string]any{"name": "openai", "base_url": "ftp://upstream.invalid/v1", "keys": []string{"k"}},
			param: "base_url",
		},
		{
			name:  "no keys",
			body:  map[string]any{"name": "openai", "base_url": "https://upstream.invalid/v1", "keys": []string{" "}},
			param: "keys",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodPost, path, tenant.adminKey, tc.body)
			body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
			expectParam(t, body, tc.param)
		})
	}
}

// --- GW-1 catalog refresh ---------------------------------------------------

// catalogRefresh is the shape of a POST /admin/v1/catalog/refresh response. Per
// tenant rather than one verdict for the call, because refreshing ten tenants
// and reporting only the first failure would hide the other nine results.
type catalogRefresh struct {
	Object    string `json:"object"`
	Refreshed []struct {
		Tenant string            `json:"tenant"`
		OK     bool              `json:"ok"`
		Models int               `json:"models"`
		Stale  bool              `json:"stale"`
		Error  string            `json:"error"`
		Errors map[string]string `json:"errors"`
	} `json:"refreshed"`
}

// refreshCatalogAs posts the refresh and decodes it, asserting only what every
// success has in common so each test below is left saying one thing.
func refreshCatalogAs(h *harness, key string, body any) catalogRefresh {
	h.t.Helper()

	res := h.do(http.MethodPost, "/admin/v1/catalog/refresh", key, body)
	if res.status != http.StatusOK {
		h.t.Fatalf("refreshing the catalog: status %d, body %s", res.status, res.body)
	}
	var out catalogRefresh
	res.decode(h.t, &out)
	if out.Object != "catalog_refresh" {
		h.t.Errorf("object = %q, want %q", out.Object, "catalog_refresh")
	}
	return out
}

// catalogModelIDs is what a data-plane caller can currently address. It is the
// only honest way to assert a refresh landed: the refresh response reports what
// the poll found, /v1/models reports what was installed.
func catalogModelIDs(h *harness, dataKey string) map[string]bool {
	h.t.Helper()

	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	res := h.do(http.MethodGet, "/v1/models", dataKey, nil)
	if res.status != http.StatusOK {
		h.t.Fatalf("listing models: status %d, body %s", res.status, res.body)
	}
	res.decode(h.t, &list)

	ids := make(map[string]bool, len(list.Data))
	for _, m := range list.Data {
		ids[m.ID] = true
	}
	return ids
}

// TestCatalogRefreshRevealsANewModelBeforeTheTTL is the reason the endpoint
// exists. The catalog TTL is an hour by default, so without an on-demand poll a
// model that appeared at the provider is unreachable for up to an hour after it
// is real — and there is no way to tell the difference between "not yet
// refreshed" and "the provider does not serve it".
func TestCatalogRefreshRevealsANewModelBeforeTheTTL(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "up")

	if !catalogModelIDs(h, tenant.dataKey)["test-small"] {
		t.Fatal("the catalog did not warm; the rest of this test proves nothing")
	}

	h.adapter.models = append(append([]store.Model(nil), defaultModels...), store.Model{
		ID:              "test-brand-new",
		ContextWindow:   4096,
		MaxOutputTokens: 1024,
		Capabilities:    []string{"chat"},
	})

	if catalogModelIDs(h, tenant.dataKey)["test-brand-new"] {
		t.Fatal("a new model appeared without a refresh; the TTL is not being honoured")
	}

	got := refreshCatalogAs(h, testBootstrapKey, map[string]any{"tenant": tenant.id})
	if len(got.Refreshed) != 1 || !got.Refreshed[0].OK {
		t.Fatalf("refresh did not report a clean poll: %+v", got.Refreshed)
	}
	if !catalogModelIDs(h, tenant.dataKey)["test-brand-new"] {
		t.Error("the new model is still not addressable after a refresh")
	}
}

// TestCatalogRefreshByRootCoversEveryTenant pins the shape that makes the
// endpoint worth having at the top level: a provider-side change is not a
// per-tenant event, and an operator should not have to enumerate tenants to
// react to one.
func TestCatalogRefreshByRootCoversEveryTenant(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")
	h.addProvider(acme.id, "up")
	h.addProvider(other.id, "up")

	got := refreshCatalogAs(h, testBootstrapKey, nil)
	if len(got.Refreshed) != 2 {
		t.Fatalf("refreshed %d tenants, want 2: %+v", len(got.Refreshed), got.Refreshed)
	}

	seen := map[string]bool{}
	for _, r := range got.Refreshed {
		seen[r.Tenant] = true
		if !r.OK {
			t.Errorf("tenant %s: ok = false (error %q)", r.Tenant, r.Error)
		}
		if r.Models != len(defaultModels) {
			t.Errorf("tenant %s: models = %d, want %d", r.Tenant, r.Models, len(defaultModels))
		}
	}
	for _, id := range []string{acme.id, other.id} {
		if !seen[id] {
			t.Errorf("tenant %s was not refreshed", id)
		}
	}
}

// TestCatalogRefreshNarrowsToTheNamedTenant is the other half: an operator
// fixing one tenant's provider should not pay for every other tenant's poll.
func TestCatalogRefreshNarrowsToTheNamedTenant(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")
	h.addProvider(acme.id, "up")
	h.addProvider(other.id, "up")

	got := refreshCatalogAs(h, testBootstrapKey, map[string]any{"tenant": acme.id})
	if len(got.Refreshed) != 1 || got.Refreshed[0].Tenant != acme.id {
		t.Fatalf("refreshed %+v, want only %s", got.Refreshed, acme.id)
	}
}

// TestCatalogRefreshByTenantKeyCoversOnlyItsOwn: a tenant administrator asking
// for "everything" gets everything it is allowed to see, which is one tenant.
// Refusing the bodyless form would make the endpoint unusable to exactly the
// caller most likely to need it after registering a provider.
func TestCatalogRefreshByTenantKeyCoversOnlyItsOwn(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	h.newTenant("other")
	h.addProvider(acme.id, "up")

	got := refreshCatalogAs(h, acme.adminKey, nil)
	if len(got.Refreshed) != 1 || got.Refreshed[0].Tenant != acme.id {
		t.Fatalf("refreshed %+v, want only %s", got.Refreshed, acme.id)
	}

	got = refreshCatalogAs(h, acme.adminKey, map[string]any{"tenant": acme.id})
	if len(got.Refreshed) != 1 || got.Refreshed[0].Tenant != acme.id {
		t.Fatalf("naming its own tenant refreshed %+v, want only %s", got.Refreshed, acme.id)
	}
}

// TestCatalogRefreshRefusesAForeignTenant closes the hole a top-level route
// opens: the tenant is named in the body rather than the path, so it is not
// covered by tenantScope and has to be checked here or not at all.
func TestCatalogRefreshRefusesAForeignTenant(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")

	res := h.do(http.MethodPost, "/admin/v1/catalog/refresh", acme.adminKey,
		map[string]any{"tenant": other.id})
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
}

// TestCatalogRefreshOnAnUnknownTenantIsNotFound: a tenant with no providers and
// a tenant that does not exist both produce an empty catalog, so without an
// existence check a typed-wrong id would report success and refresh nothing.
func TestCatalogRefreshOnAnUnknownTenantIsNotFound(t *testing.T) {
	h := newHarness(t)
	h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/catalog/refresh", testBootstrapKey,
		map[string]any{"tenant": "tnt_does_not_exist"})
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)
}

// TestCatalogRefreshKeepsTheLastGoodCatalogWhenTheProviderIsDown is the
// property that made Refresh a separate method from Invalidate. GW-1 requires
// that a failed poll not evict the previous snapshot: a provider's listing
// endpoint being unreachable is not a reason to stop routing traffic that would
// otherwise succeed, and evicting here would turn one unreachable provider into
// a cold tenant and a 503 on the data plane.
func TestCatalogRefreshKeepsTheLastGoodCatalogWhenTheProviderIsDown(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "up")

	if !catalogModelIDs(h, tenant.dataKey)["test-small"] {
		t.Fatal("the catalog did not warm; the rest of this test proves nothing")
	}

	h.adapter.listErr = errTestUpstreamDown

	got := refreshCatalogAs(h, testBootstrapKey, map[string]any{"tenant": tenant.id})
	if len(got.Refreshed) != 1 {
		t.Fatalf("refreshed %+v, want one entry", got.Refreshed)
	}
	entry := got.Refreshed[0]
	if entry.OK || !entry.Stale {
		t.Errorf("ok = %v, stale = %v; want a failed poll reported as stale", entry.OK, entry.Stale)
	}
	if entry.Models != len(defaultModels) {
		t.Errorf("models = %d, want the previous %d retained", entry.Models, len(defaultModels))
	}

	// The refresh response saying the data was kept is not the same as the data
	// plane still serving it, and it is the data plane that matters.
	if !catalogModelIDs(h, tenant.dataKey)["test-small"] {
		t.Error("a failed refresh evicted the previous catalog")
	}
}

// TestCatalogRefreshOnAColdTenantReportsTheProviderFailure: with nothing to fall
// back to there is no stale data to serve, and the operator has to be told why
// rather than handed an empty catalog that looks like a misconfiguration.
func TestCatalogRefreshOnAColdTenantReportsTheProviderFailure(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "up")
	h.adapter.listErr = errTestUpstreamDown

	got := refreshCatalogAs(h, testBootstrapKey, map[string]any{"tenant": tenant.id})
	if len(got.Refreshed) != 1 {
		t.Fatalf("refreshed %+v, want one entry", got.Refreshed)
	}
	entry := got.Refreshed[0]
	if entry.OK {
		t.Error("ok = true for a tenant whose only provider is unreachable")
	}
	if entry.Error == "" {
		t.Error("the failure is reported without saying what went wrong")
	}
}

// --- GW-4 quota -------------------------------------------------------------

func TestQuotaValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	path := "/admin/v1/tenants/" + tenant.id + "/quota"

	res := h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"period": "week", "token_limit": 1000})
	expectParam(t, h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest), "period")

	res = h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"period": "month", "token_limit": 1000, "soft_threshold_pct": 150})
	expectParam(t, h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest), "soft_threshold_pct")

	res = h.do(http.MethodPut, path, tenant.adminKey,
		map[string]any{"period": "month", "token_limit": -1})
	h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
}

// TestQuotaDefaultsSoftThreshold covers the omitted field taking the configured
// default rather than zero, which would mean "warn at 0%" — a soft warning on
// the tenant's first request.
func TestQuotaDefaultsSoftThreshold(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenant.id+"/quota", tenant.adminKey,
		map[string]any{"period": "month", "token_limit": 1000})
	if res.status != http.StatusOK {
		t.Fatalf("setting quota: status %d, body %s", res.status, res.body)
	}
	var q store.Quota
	res.decode(t, &q)
	if want := h.srv.Config.Quotas.DefaultSoftThresholdPct; q.SoftThresholdPct != want {
		t.Errorf("soft_threshold_pct = %d, want the configured default %d", q.SoftThresholdPct, want)
	}
}

func TestGetQuotaWhenUnsetIsNotFound(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/quota", tenant.adminKey, nil)
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)
}

// --- GW-4 webhooks ----------------------------------------------------------

func TestWebhookValidation(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	path := "/admin/v1/tenants/" + tenant.id + "/webhooks"

	const goodSecret = "webhook-signing-secret-0123456789"

	for _, tc := range []struct {
		name  string
		body  map[string]any
		param string
	}{
		{
			name:  "relative url",
			body:  map[string]any{"url": "hooks.invalid/x", "secret": goodSecret, "events": []string{"breaker.opened"}},
			param: "url",
		},
		{
			// Sixteen characters is a floor on entropy: a signature computed with
			// a guessable key authenticates nothing.
			name:  "short secret",
			body:  map[string]any{"url": "https://hooks.invalid/x", "secret": "tooshort", "events": []string{"breaker.opened"}},
			param: "secret",
		},
		{
			name:  "no events",
			body:  map[string]any{"url": "https://hooks.invalid/x", "secret": goodSecret, "events": []string{}},
			param: "events",
		},
		{
			// Accepting a type outside the registry would create a subscription
			// that is never delivered and never explains why.
			name:  "unknown event",
			body:  map[string]any{"url": "https://hooks.invalid/x", "secret": goodSecret, "events": []string{"quota.exceeded"}},
			param: "events",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodPost, path, tenant.adminKey, tc.body)
			body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
			expectParam(t, body, tc.param)
		})
	}
}

// TestWebhookAcceptsEveryRegisteredEvent guards against the registry and the
// validator drifting apart: an event the gateway can emit but no webhook may
// subscribe to is an event that reaches nobody.
func TestWebhookAcceptsEveryRegisteredEvent(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/webhooks", tenant.adminKey,
		map[string]any{
			"url":    "https://hooks.invalid/cognigate",
			"secret": "webhook-signing-secret-0123456789",
			"events": eventRegistry,
		})
	if res.status != http.StatusCreated {
		t.Fatalf("subscribing to the full registry: status %d, body %s", res.status, res.body)
	}
}

// --- GW-6 resource lifecycle ------------------------------------------------

// TestDeleteUnknownResourceIsNotFound pins 404 resource_not_found across the
// admin plane. A delete that answered 204 for something that never existed would
// let a broken cleanup script report success forever.
func TestDeleteUnknownResourceIsNotFound(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	base := "/admin/v1/tenants/" + tenant.id

	for _, path := range []string{
		base + "/keys/key_does_not_exist",
		base + "/providers/prv_does_not_exist",
		base + "/routes/rt_does_not_exist",
		base + "/webhooks/whk_does_not_exist",
		base + "/quota",
	} {
		res := h.do(http.MethodDelete, path, tenant.adminKey, nil)
		h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)
	}
}

// TestDeleteTenantIsInvisibleToItsKeys covers the deletion actually taking
// effect on the data plane rather than only in the store: a key belonging to a
// deleted tenant must stop authenticating.
func TestDeleteTenantIsInvisibleToItsKeys(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	if res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil); res.status != http.StatusOK {
		t.Fatalf("data key does not work before deletion: status %d", res.status)
	}

	res := h.do(http.MethodDelete, "/admin/v1/tenants/"+tenant.id, testBootstrapKey, nil)
	if res.status != http.StatusNoContent {
		t.Fatalf("deleting tenant: status %d, body %s", res.status, res.body)
	}

	res = h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
}

// --- GW-7 malformed input ---------------------------------------------------

// TestAdminRejectsMalformedJSON answers 400 rather than 500. A body that will
// not parse is the caller's mistake, and reporting it as a server error sends
// them looking in the wrong place.
func TestAdminRejectsMalformedJSON(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, path := range []string{
		"/admin/v1/tenants/" + tenant.id + "/keys",
		"/admin/v1/tenants/" + tenant.id + "/providers",
		"/admin/v1/tenants/" + tenant.id + "/webhooks",
	} {
		res := h.do(http.MethodPost, path, tenant.adminKey, `{"name": `)
		h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	}
}
