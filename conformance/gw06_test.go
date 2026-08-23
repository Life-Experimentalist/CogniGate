package conformance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// GW-6 — admin/config API.
//
// Unconditional: there is no feature flag that excuses a gateway from having a
// control plane. Individual criteria still gate on the capability they
// configure — a deployment that does not claim aliases has no alias to save
// through this API — but the plane itself, its two-credential split, its scope
// rule, its show-once keys and its audit log are required of every target.
//
// Everything here goes through HTTP. The suite has no view of the target's
// store, which is the point: "no step requires editing a file or restarting a
// process" is a claim about the API surface, and a test that reached past it to
// confirm a write would not be testing that claim.

// TestGW6_AC1_EveryConfigurationIsReachableOverTheAPI covers GW-6.AC-1: tenant,
// key, provider, rule, alias and quota can each be created, read, updated and
// deleted with a root admin key, with no file edited and no process restarted.
//
// The tenant is created inside the test rather than reused from provisioning so
// the create half of "create and delete" is actually exercised, and so the
// delete half can be asserted without destroying what the rest of the run
// depends on.
func TestGW6_AC1_EveryConfigurationIsReachableOverTheAPI(t *testing.T) {
	c := begin(t)

	tn := newTenant(t, "gw6-ac1")
	addMockProvider(t, tn)
	base := "/admin/v1/tenants/" + tn.ID

	// Tenant: read back, then rename. A rename is the cheapest mutation that
	// proves PATCH reaches the store, and asserting the id survives it is what
	// separates an update from a delete-and-recreate.
	read := c.admin(t, http.MethodGet, base, nil)
	if read.Status != http.StatusOK {
		t.Fatalf("GET %s: status %d\n%s", base, read.Status, truncate(read.Body))
	}
	renamed := c.admin(t, http.MethodPatch, base, map[string]any{"name": uniqueName("gw6-ac1-renamed")})
	if renamed.Status != http.StatusOK {
		t.Fatalf("PATCH %s: status %d\n%s", base, renamed.Status, truncate(renamed.Body))
	}
	if id, _ := renamed.JSON(t)["id"].(string); id != tn.ID {
		t.Errorf("renaming the tenant changed its id from %s to %s", tn.ID, id)
	}

	// Keys.
	key := newDataKey(t, tn.ID, "gw6-ac1")
	if list := c.admin(t, http.MethodGet, base+"/keys", nil); list.Status != http.StatusOK {
		t.Errorf("GET %s/keys: status %d\n%s", base, list.Status, truncate(list.Body))
	}
	if del := c.admin(t, http.MethodDelete, base+"/keys/"+key.ID, nil); del.Status != http.StatusNoContent {
		t.Errorf("DELETE %s/keys/%s: status %d\n%s", base, key.ID, del.Status, truncate(del.Body))
	}

	// Providers. addMockProvider registered one; this reads it back, toggles it
	// off through PATCH and confirms the toggle stuck.
	providers := adminProviders(t, tn.ID)
	if len(providers) == 0 {
		t.Fatalf("GET %s/providers returns nothing for a tenant that has one", base)
	}
	provID := providers[0].ID
	off := c.admin(t, http.MethodPatch, base+"/providers/"+provID, map[string]any{"enabled": false})
	if off.Status != http.StatusOK {
		t.Fatalf("PATCH %s/providers/%s: status %d\n%s", base, provID, off.Status, truncate(off.Body))
	}
	if enabled, _ := off.JSON(t)["enabled"].(bool); enabled {
		t.Errorf("the provider reports enabled after being disabled\n%s", truncate(off.Body))
	}
	if on := c.admin(t, http.MethodPatch, base+"/providers/"+provID,
		map[string]any{"enabled": true}); on.Status != http.StatusOK {
		t.Errorf("re-enabling the provider: status %d\n%s", on.Status, truncate(on.Body))
	}

	// Aliases, rules and quotas are each configuration for a capability the
	// target may not claim. Skipping the whole criterion because one of them is
	// absent would lose the coverage of everything above, so each is gated on
	// its own.
	if suite.features["aliases"] {
		putAlias(t, tn.ID, "gw6-ac1", map[string]any{"pin": "mock-chat-a"})
		if list := c.admin(t, http.MethodGet, base+"/aliases", nil); list.Status != http.StatusOK {
			t.Errorf("GET %s/aliases: status %d\n%s", base, list.Status, truncate(list.Body))
		}
		if del := c.admin(t, http.MethodDelete, base+"/aliases/gw6-ac1", nil); del.Status != http.StatusNoContent {
			t.Errorf("DELETE %s/aliases/gw6-ac1: status %d\n%s", base, del.Status, truncate(del.Body))
		}
	}

	if suite.features["fallback_chains"] {
		rule := putRouteReturning(t, tn.ID, "gw6-ac1-*", "mock-chat-a", "mock-chat-b")
		if list := c.admin(t, http.MethodGet, base+"/routing-rules", nil); list.Status != http.StatusOK {
			t.Errorf("GET %s/routing-rules: status %d\n%s", base, list.Status, truncate(list.Body))
		}
		if del := c.admin(t, http.MethodDelete, base+"/routing-rules/"+rule,
			nil); del.Status != http.StatusNoContent {
			t.Errorf("DELETE %s/routing-rules/%s: status %d\n%s", base, rule, del.Status, truncate(del.Body))
		}
	}

	if suite.features["quotas"] {
		putQuota(t, tn.ID, map[string]any{"day": tokenCap(1_000_000, 80)})
		if read := c.admin(t, http.MethodGet, base+"/quota", nil); read.Status != http.StatusOK {
			t.Errorf("GET %s/quota: status %d\n%s", base, read.Status, truncate(read.Body))
		}
		if del := c.admin(t, http.MethodDelete, base+"/quota", nil); del.Status != http.StatusNoContent {
			t.Errorf("DELETE %s/quota: status %d\n%s", base, del.Status, truncate(del.Body))
		}
	}

	// Deleting the tenant is the last configuration verb, and the one the
	// specification puts a guard on.
	del := c.admin(t, http.MethodDelete, base+"?confirm="+tn.ID, nil)
	if del.Status != http.StatusNoContent {
		t.Fatalf("DELETE %s?confirm=: status %d\n%s", base, del.Status, truncate(del.Body))
	}
	if gone := c.admin(t, http.MethodGet, base, nil); gone.Status != http.StatusNotFound {
		t.Errorf("the tenant is still readable after deletion: status %d", gone.Status)
	}
}

// TestGW6_AC2_CredentialsDoNotCrossPlanes covers GW-6.AC-2: a data key on the
// admin plane and an admin key on the data plane both fail 401 wrong_plane.
//
// The distinction from an invalid key matters to the caller. "This credential
// is not valid" sends someone to rotate a key that is working perfectly; the
// mistake is that they sent it to the wrong URL, and only a distinct code says
// so.
func TestGW6_AC2_CredentialsDoNotCrossPlanes(t *testing.T) {
	c := begin(t)

	for _, tc := range []struct {
		what   string
		method string
		path   string
		key    string
		body   any
	}{
		{"a data key on the admin plane", http.MethodGet, "/admin/v1/tenants", suite.dataKey, nil},
		{"an admin key on a completion", http.MethodPost, "/v1/chat/completions", suite.cfg.AdminKey,
			map[string]any{"model": "mock-chat-a", "messages": []any{
				map[string]any{"role": "user", "content": "hello"}}}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			resp := c.do(t, tc.method, tc.path, tc.key, tc.body)
			if resp.Status != http.StatusUnauthorized {
				t.Fatalf("%s %s with %s: status %d, want 401\n%s",
					tc.method, tc.path, tc.what, resp.Status, truncate(resp.Body))
			}
			if code := resp.ErrorCode(t); code != "wrong_plane" {
				t.Errorf("error.code = %q, want \"wrong_plane\" — a credential sent to the wrong "+
					"plane is not an invalid credential, and telling the caller it is sends them "+
					"to rotate a key that works\n%s", code, truncate(resp.Body))
			}
		})
	}
}

// TestGW6_AC3_ATenantScopedKeyCannotSeeAnotherTenant covers GW-6.AC-3: a
// tenant-scoped admin key CRUDs its own tenant and receives 404 — not 403 —
// for another's.
//
// 404 is the requirement, and the reason is that 403 answers a question the
// caller was not entitled to ask. A holder of one tenant's admin key who could
// tell "forbidden" from "no such tenant" could enumerate the deployment's whole
// customer list by guessing ids.
func TestGW6_AC3_ATenantScopedKeyCannotSeeAnotherTenant(t *testing.T) {
	c := begin(t)

	mine := newTenant(t, "gw6-ac3-mine")
	theirs := newTenant(t, "gw6-ac3-theirs")
	scoped := newAdminKeyFor(t, mine.ID)

	// Its own tenant: reachable.
	own := c.do(t, http.MethodGet, "/admin/v1/tenants/"+mine.ID+"/keys", scoped, nil)
	if own.Status != http.StatusOK {
		t.Fatalf("a tenant-scoped admin key cannot read its own tenant's keys: status %d\n%s",
			own.Status, truncate(own.Body))
	}

	// A neighbour, and an id that belongs to nobody. Both must answer
	// identically: a difference between them is the leak, whichever way round.
	for _, tc := range []struct {
		what string
		id   string
	}{
		{"another tenant", theirs.ID},
		{"an id that does not exist", "ten_gw6ac3nosuchtenant"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			base := "/admin/v1/tenants/" + tc.id
			for _, call := range []struct {
				method string
				path   string
				body   any
			}{
				{http.MethodGet, base + "/keys", nil},
				{http.MethodGet, base + "/providers", nil},
				{http.MethodPut, base + "/aliases/stolen", map[string]any{"pin": "mock-chat-a"}},
			} {
				resp := c.do(t, call.method, call.path, scoped, call.body)
				if resp.Status != http.StatusNotFound {
					t.Errorf("%s %s with a key scoped to another tenant: status %d, want 404 — "+
						"403 confirms the tenant exists\n%s",
						call.method, call.path, resp.Status, truncate(resp.Body))
				}
			}
		})
	}
}

// TestGW6_AC4_AKeySecretIsShownOnceAndRevocationIsImmediate covers GW-6.AC-4.
//
// Two claims, and the second is the one with a deadline: the plaintext appears
// in the creation response and nowhere else, and a revoked key stops
// authenticating within ten seconds.
func TestGW6_AC4_AKeySecretIsShownOnceAndRevocationIsImmediate(t *testing.T) {
	c := begin(t)

	tn := newTenant(t, "gw6-ac4")
	key := newDataKey(t, tn.ID, "gw6-ac4")

	// The listing is checked as raw bytes rather than as a decoded field. A
	// gateway that grew a new field carrying the secret would pass a
	// field-by-field assertion and fail this one, which is the whole point.
	list := c.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID+"/keys", nil)
	if list.Status != http.StatusOK {
		t.Fatalf("listing keys: status %d\n%s", list.Status, truncate(list.Body))
	}
	if strings.Contains(string(list.Body), key.Secret) {
		t.Errorf("the key listing contains the plaintext secret; it is shown once, at creation, " +
			"and a control plane that will hand it back later is one a database read can drain")
	}

	if resp := c.do(t, http.MethodGet, "/v1/meta", key.Secret, nil); resp.Status != http.StatusOK {
		t.Fatalf("the minted key does not authenticate: status %d\n%s", resp.Status, truncate(resp.Body))
	}

	revoked := c.admin(t, http.MethodDelete, "/admin/v1/tenants/"+tn.ID+"/keys/"+key.ID, nil)
	if revoked.Status != http.StatusNoContent {
		t.Fatalf("revoking the key: status %d\n%s", revoked.Status, truncate(revoked.Body))
	}

	elapsed := allowSeconds(t, 10*time.Second)
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := c.do(t, http.MethodGet, "/v1/meta", key.Secret, nil)
		if resp.Status == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a revoked key still authenticates 30s later: status %d\n%s",
				resp.Status, truncate(resp.Body))
		}
		time.Sleep(250 * time.Millisecond)
	}
	elapsed("the revocation took effect")
}

// TestGW6_AC5_AProviderKeyIsNeverReadableBack covers GW-6.AC-5: a stored
// upstream credential is never returned by any read endpoint in a form longer
// than its prefix.
//
// The credential registered here is distinctive so its absence can be asserted
// on raw bytes rather than on a field name. Three reads are checked — the
// provider itself, the audit log that recorded the registration, and the error
// body from a rejected update — because a secret that leaks does not
// necessarily leak from the resource it belongs to.
func TestGW6_AC5_AProviderKeyIsNeverReadableBack(t *testing.T) {
	c := begin(t)

	const secret = "sk-gw6-ac5-provider-secret-never-readable"

	tn := newTenant(t, "gw6-ac5")
	created := c.admin(t, http.MethodPost, "/admin/v1/tenants/"+tn.ID+"/providers",
		map[string]any{
			"name":     "gw6-ac5",
			"kind":     "openai",
			"base_url": suite.providerURL,
			"keys":     []string{secret},
		})
	if created.Status != http.StatusCreated {
		t.Fatalf("registering a provider: status %d\n%s", created.Status, truncate(created.Body))
	}

	// A rejected update, so the error path is covered too: a validation message
	// that echoed the body back would carry the key with it.
	var provider struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body, &provider); err != nil || provider.ID == "" {
		t.Fatalf("the created provider has no id: %s", truncate(created.Body))
	}
	rejected := c.admin(t, http.MethodPatch, "/admin/v1/tenants/"+tn.ID+"/providers/"+provider.ID,
		map[string]any{"base_url": "not-a-url", "keys": []string{secret}})

	audit := c.admin(t, http.MethodGet, "/admin/v1/audit?limit=200", nil)

	for _, read := range []struct {
		what string
		body []byte
	}{
		{"the creation response", created.Body},
		{"the provider listing", c.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID+"/providers", nil).Body},
		{"a rejected update's error body", rejected.Body},
		{"the audit log", audit.Body},
	} {
		if strings.Contains(string(read.body), secret) {
			t.Errorf("%s contains the stored provider key in full", read.what)
		}
		// Half of it is still most of it. A gateway that returned a truncated
		// form long enough to be useful would pass the check above.
		if strings.Contains(string(read.body), "never-readable") {
			t.Errorf("%s contains the tail of the stored provider key", read.what)
		}
	}
}

// TestGW6_AC6_ARuleViolatingGW3IsRejectedHere covers GW-6.AC-6: saving a
// routing rule whose chain repeats a model is refused with 400
// fallback_duplicate_model — the same code GW-3 answers on the data plane.
//
// One validator, two entry points. A control plane that accepted a chain the
// router would later refuse to walk turns a configuration mistake into a
// production incident, discovered by a caller rather than by the operator who
// made it.
func TestGW6_AC6_ARuleViolatingGW3IsRejectedHere(t *testing.T) {
	c := begin(t)
	requireFeature(t, "fallback_chains")

	tn := newTenant(t, "gw6-ac6")
	addMockProvider(t, tn)

	resp := tryPutRoute(t, tn.ID, "gw6-ac6-*", []string{"mock-chat-a", "mock-chat-a"})
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("saving a chain that repeats a model: status %d, want 400\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "fallback_duplicate_model" {
		t.Errorf("error.code = %q, want \"fallback_duplicate_model\" — GW-6.AC-6 shares this code "+
			"with GW-3.AC-1 precisely so a caller cannot tell which entry point refused\n%s",
			code, truncate(resp.Body))
	}

	// The rule must not have been stored. A 400 that saved anyway would leave
	// the deployment in the state the refusal claimed to prevent.
	list := c.admin(t, http.MethodGet, "/admin/v1/tenants/"+tn.ID+"/routing-rules", nil)
	if strings.Contains(string(list.Body), "gw6-ac6-*") {
		t.Errorf("the refused rule was stored anyway\n%s", truncate(list.Body))
	}
}

// TestGW6_AC7_AQuotaChangeReachesTheDataPlane covers GW-6.AC-7: a quota written
// through the admin plane governs data-plane enforcement within ten seconds,
// with no restart.
//
// This is GW-4.AC-6 read from the other end. GW-4 asks whether the quota engine
// honours a new cap; this asks whether the admin plane is a real way to set
// one — the criterion that makes config files a convenience rather than the
// only interface.
func TestGW6_AC7_AQuotaChangeReachesTheDataPlane(t *testing.T) {
	begin(t)
	requireFeature(t, "quotas")
	requireEnforcement(t, true)

	tn := quotaTenant(t, "gw6-ac7")

	// Unlimited to start with, so the rejection that follows can only have come
	// from the write this test makes.
	if resp := chat(t, tn.Key, "mock-chat-a"); resp.Status != http.StatusOK {
		t.Fatalf("a completion fails before any quota is set: status %d\n%s",
			resp.Status, truncate(resp.Body))
	}

	putQuota(t, tn.ID, map[string]any{"day": tokenCap(1, 80)})

	elapsed := allowSeconds(t, 10*time.Second)
	rejected := awaitChat(t, tn.Key, "mock-chat-a",
		func(r *response) bool { return r.Status == http.StatusTooManyRequests },
		"was rejected for the cap written through the admin plane")
	elapsed("the quota written through the admin plane took effect")

	if state := rejected.Header.Get(headerQuotaState); state != quotaHardExceeded {
		t.Errorf("%s = %q on the rejection, want %q", headerQuotaState, state, quotaHardExceeded)
	}
}

// TestGW6_AC8_EveryMutationIsAudited covers GW-6.AC-8: each mutation appears in
// GET /admin/v1/audit with its actor, action and resource.
//
// The log is read once, after all four writes, and each is looked for in it.
// Reading after each write instead would pass on a gateway that kept only the
// most recent entry.
func TestGW6_AC8_EveryMutationIsAudited(t *testing.T) {
	c := begin(t)

	tn := newTenant(t, "gw6-ac8")
	before := auditIDs(t)

	key := newDataKey(t, tn.ID, "gw6-ac8")
	c.admin(t, http.MethodPatch, "/admin/v1/tenants/"+tn.ID, map[string]any{"name": uniqueName("gw6-ac8-renamed")})
	c.admin(t, http.MethodDelete, "/admin/v1/tenants/"+tn.ID+"/keys/"+key.ID, nil)
	// A refused write. This is the entry an operator investigating an incident
	// is looking for, and a log that recorded only what succeeded would be
	// silent about exactly the attempts worth reading it for.
	c.admin(t, http.MethodPut, "/admin/v1/tenants/ten_gw6ac8nosuchtenant/aliases/stolen",
		map[string]any{"model": "mock-chat-a"})

	entries := auditEntries(t)
	fresh := entries[:0]
	for _, e := range entries {
		if !before[e.ID] {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		t.Fatalf("no new audit entries after four admin-plane writes")
	}

	for _, want := range []struct {
		what     string
		action   string
		resource string
	}{
		{"minting a key", "create", "/admin/v1/tenants/" + tn.ID + "/keys"},
		{"renaming the tenant", "update", "/admin/v1/tenants/" + tn.ID},
		{"revoking the key", "delete", "/admin/v1/tenants/" + tn.ID + "/keys/" + key.ID},
		{"a refused cross-tenant write", "upsert", "/admin/v1/tenants/ten_gw6ac8nosuchtenant/aliases/stolen"},
	} {
		found := false
		for _, e := range fresh {
			if e.Action == want.action && e.Resource == want.resource {
				found = true
				if e.Actor == "" {
					t.Errorf("the entry for %s names no actor", want.what)
				}
				break
			}
		}
		if !found {
			t.Errorf("%s (%s %s) is not in the audit log", want.what, want.action, want.resource)
		}
	}

	// Reads are not audited. A log that grew by a line per dashboard refresh
	// would be unreadable for the one question it exists to answer.
	c.admin(t, http.MethodGet, "/admin/v1/tenants", nil)
	c.admin(t, http.MethodGet, "/admin/v1/audit", nil)
	after := auditIDs(t)
	if len(after) > len(before)+len(fresh)+1 {
		t.Errorf("the audit log grew by more than the mutations made: %d entries before, "+
			"%d writes, %d after — reads must not be recorded",
			len(before), len(fresh), len(after))
	}
}

// --- GW-6 helpers -----------------------------------------------------------

// adminProvider is a provider as the admin plane returns it. Only the fields
// this file asserts on are named; the suite deliberately does not mirror the
// gateway's own struct.
type adminProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func adminProviders(t *testing.T, tenantID string) []adminProvider {
	t.Helper()
	resp := suite.client.admin(t, http.MethodGet, "/admin/v1/tenants/"+tenantID+"/providers", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("listing providers for %s: status %d\n%s", tenantID, resp.Status, truncate(resp.Body))
	}
	var page struct {
		Data []adminProvider `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("the provider listing is not the GW-6 list envelope: %v\n%s", err, truncate(resp.Body))
	}
	return page.Data
}

// newAdminKeyFor mints an admin key confined to one tenant. GW-6.AC-3 is a
// statement about that credential and cannot be written without one.
func newAdminKeyFor(t *testing.T, tenantID string) string {
	t.Helper()
	created := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tenantID+"/keys",
		map[string]any{"name": "gw6-scoped", "plane": "admin"})
	if created.Status != http.StatusCreated {
		t.Fatalf("minting a tenant-scoped admin key for %s: status %d\n%s",
			tenantID, created.Status, truncate(created.Body))
	}
	var minted struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created.Body, &minted); err != nil || minted.Secret == "" {
		t.Fatalf("the minted admin key has no secret: %s", truncate(created.Body))
	}
	return minted.Secret
}

// putRouteReturning saves a rule and hands back its id, which DELETE needs and
// the existing putRoute does not return.
func putRouteReturning(t *testing.T, tenantID, match string, chain ...string) string {
	t.Helper()
	resp := tryPutRoute(t, tenantID, match, chain)
	if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
		t.Fatalf("saving a rule for %s: status %d\n%s", tenantID, resp.Status, truncate(resp.Body))
	}
	var rule struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &rule); err != nil || rule.ID == "" {
		t.Fatalf("the saved rule has no id: %s", truncate(resp.Body))
	}
	return rule.ID
}

// auditEntry is one line of the log, in the fields the criterion names.
type auditEntry struct {
	ID       string `json:"id"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

func auditEntries(t *testing.T) []auditEntry {
	t.Helper()
	resp := suite.client.admin(t, http.MethodGet, "/admin/v1/audit?limit=200", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /admin/v1/audit: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	var page struct {
		Data []auditEntry `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		t.Fatalf("the audit log is not the GW-6 list envelope: %v\n%s", err, truncate(resp.Body))
	}
	return page.Data
}

func auditIDs(t *testing.T) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, e := range auditEntries(t) {
		seen[e.ID] = true
	}
	return seen
}
