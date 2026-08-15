package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/store"
)

// pageOf is the GW-6 list envelope as a caller sees it.
type pageOf[T any] struct {
	Object  string `json:"object"`
	Data    []T    `json:"data"`
	HasMore bool   `json:"has_more"`
}

// authenticates probes the plane with a tenant's own admin key. It is the
// cheapest way to ask "does this credential still work" without depending on a
// reachable upstream, which the data plane would need.
func (h *harness) authenticates(tenantID, key string) int {
	h.t.Helper()
	return h.do(http.MethodGet, "/admin/v1/tenants/"+tenantID+"/keys", key, nil).status
}

// --- root admin keys --------------------------------------------------------

// TestAdminKeyMintListAndRevoke covers the credential a deployment rotates to
// once it wants to stop relying on the bootstrap key in its environment.
//
// The whole lifecycle is one test because the interesting property is the
// sequence: minted keys work, the secret is never readable again, and a revoked
// one stops working.
func TestAdminKeyMintListAndRevoke(t *testing.T) {
	h := newHarness(t)

	res := h.do(http.MethodPost, "/admin/v1/admin-keys", testBootstrapKey,
		map[string]any{"name": "ops"})
	if res.status != http.StatusCreated {
		t.Fatalf("minting admin key: status %d, body %s", res.status, res.body)
	}
	var minted struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	res.decode(t, &minted)

	if minted.Secret == "" {
		t.Fatal("no secret returned; the credential would be unusable")
	}
	if minted.Key.TenantID != "" {
		t.Errorf("root admin key belongs to tenant %q, want none", minted.Key.TenantID)
	}
	if minted.Key.Scope != store.ScopeRoot {
		t.Errorf("scope = %q, want %q", minted.Key.Scope, store.ScopeRoot)
	}

	// It really is a root credential: /admin/v1/tenants is root-only.
	if res := h.do(http.MethodGet, "/admin/v1/tenants", minted.Secret, nil); res.status != http.StatusOK {
		t.Fatalf("minted key on a root route: status %d, body %s", res.status, res.body)
	}

	// Listing shows the record and never the secret. This is the property
	// GW-6.AC-4 is about, and the check is on the raw bytes rather than the
	// decoded struct so that adding a field to APIKey cannot quietly start
	// leaking it.
	listed := h.do(http.MethodGet, "/admin/v1/admin-keys", testBootstrapKey, nil)
	if listed.status != http.StatusOK {
		t.Fatalf("listing admin keys: status %d, body %s", listed.status, listed.body)
	}
	if strings.Contains(string(listed.body), minted.Secret) {
		t.Error("the admin key listing contains the plaintext secret")
	}
	var page pageOf[store.APIKey]
	listed.decode(t, &page)
	found := false
	for _, k := range page.Data {
		if k.ID == minted.Key.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("minted key %s absent from the listing", minted.Key.ID)
	}

	if res := h.do(http.MethodDelete, "/admin/v1/admin-keys/"+minted.Key.ID, testBootstrapKey, nil); res.status != http.StatusNoContent {
		t.Fatalf("revoking admin key: status %d, body %s", res.status, res.body)
	}
	res = h.do(http.MethodGet, "/admin/v1/tenants", minted.Secret, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
}

// TestAdminKeyRoutesRequireRoot: minting root credentials from a tenant-scoped
// key would be a one-request privilege escalation, and the whole plane boundary
// rests on it being impossible.
//
// These stay 403 rather than joining the 404 rule. That rule is about not
// revealing whether another tenant exists; /admin/v1/admin-keys names no
// tenant, so there is nothing to conceal and an honest "your key is not
// permitted" is the more useful answer.
func TestAdminKeyRoutesRequireRoot(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/admin/v1/admin-keys", map[string]any{"name": "escalated"}},
		{http.MethodGet, "/admin/v1/admin-keys", nil},
		{http.MethodDelete, "/admin/v1/admin-keys/key_whatever", nil},
	} {
		res := h.do(tc.method, tc.path, tenant.adminKey, tc.body)
		h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
	}
}

// --- pagination -------------------------------------------------------------

// TestAdminListPaginatesWithoutGapsOrRepeats walks a collection one short page
// at a time and reassembles it.
//
// The assertion is on the reassembled set, not on the page sizes: a cursor that
// skips a row or hands one back twice is the failure that matters, and it is
// invisible if the test only checks that each page came back with the right
// length.
func TestAdminListPaginatesWithoutGapsOrRepeats(t *testing.T) {
	h := newHarness(t)

	const total = 5
	want := map[string]bool{}
	for i := 0; i < total; i++ {
		want[h.newTenant(fmt.Sprintf("tenant-%d", i)).id] = true
	}

	got := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		path := "/admin/v1/tenants?limit=2"
		if cursor != "" {
			path += "&after=" + cursor
		}
		res := h.do(http.MethodGet, path, testBootstrapKey, nil)
		if res.status != http.StatusOK {
			t.Fatalf("listing tenants: status %d, body %s", res.status, res.body)
		}
		var page pageOf[store.Tenant]
		res.decode(t, &page)

		if len(page.Data) > 2 {
			t.Fatalf("page carries %d rows, want at most the requested 2", len(page.Data))
		}
		for _, tenant := range page.Data {
			if got[tenant.ID] {
				t.Errorf("tenant %s returned on more than one page", tenant.ID)
			}
			got[tenant.ID] = true
			cursor = tenant.ID
		}
		if !page.HasMore {
			break
		}
		if len(page.Data) == 0 {
			t.Fatal("has_more is set on an empty page; the cursor cannot advance")
		}
	}

	for id := range want {
		if !got[id] {
			t.Errorf("tenant %s was never returned", id)
		}
	}
	if len(got) != total {
		t.Errorf("walked %d tenants, want %d", len(got), total)
	}
}

// TestAdminListRefusesBadPagingArguments: every one of these is refused rather
// than clamped or ignored.
//
// A clamped limit returns a short page with has_more set, which a caller cannot
// tell from a genuine last page; a cursor treated as "start from the beginning"
// silently replays a page the caller already has. Both read as success while
// answering a different question than the one asked.
func TestAdminListRefusesBadPagingArguments(t *testing.T) {
	h := newHarness(t)
	h.newTenant("acme")

	for _, tc := range []struct{ query, param string }{
		{"limit=0", "limit"},
		{"limit=-1", "limit"},
		{"limit=201", "limit"},
		{"limit=many", "limit"},
		{"after=ten_nosuchtenant", "after"},
	} {
		res := h.do(http.MethodGet, "/admin/v1/tenants?"+tc.query, testBootstrapKey, nil)
		body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
		expectParam(t, body, tc.param)
	}
}

// --- audit ------------------------------------------------------------------

func auditEntries(t *testing.T, h *harness) []store.AuditEntry {
	t.Helper()
	res := h.do(http.MethodGet, "/admin/v1/audit?limit=200", testBootstrapKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("reading audit log: status %d, body %s", res.status, res.body)
	}
	var page pageOf[store.AuditEntry]
	res.decode(t, &page)
	return page.Data
}

// TestAuditRecordsMutationsAndIgnoresReads pins both halves of what the log is
// for: every write appears, and reads do not.
//
// Reads are excluded deliberately. The log answers "what changed", and one line
// per dashboard refresh would bury the handful of lines that did.
func TestAuditRecordsMutationsAndIgnoresReads(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	before := len(auditEntries(t, h))
	if res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id, testBootstrapKey, nil); res.status != http.StatusOK {
		t.Fatalf("reading tenant: status %d", res.status)
	}
	if res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/aliases", testBootstrapKey, nil); res.status != http.StatusOK {
		t.Fatalf("listing aliases: status %d", res.status)
	}
	if after := len(auditEntries(t, h)); after != before {
		t.Errorf("audit grew by %d entries across two reads, want 0", after-before)
	}

	path := "/admin/v1/tenants/" + tenant.id + "/aliases/audited"
	if res := h.do(http.MethodPut, path, testBootstrapKey, map[string]any{"pin": "gpt-4o"}); res.status != http.StatusOK {
		t.Fatalf("upserting alias: status %d, body %s", res.status, res.body)
	}

	entries := auditEntries(t, h)
	if len(entries) == 0 {
		t.Fatal("audit log is empty after a write")
	}
	// Newest first, so the write just made is the head.
	got := entries[0]
	if got.Resource != path {
		t.Errorf("resource = %q, want %q", got.Resource, path)
	}
	if got.Action != "upsert" {
		t.Errorf("action = %q, want %q", got.Action, "upsert")
	}
	if got.Status != http.StatusOK {
		t.Errorf("status = %d, want %d", got.Status, http.StatusOK)
	}
	if got.TenantID != tenant.id {
		t.Errorf("tenant_id = %q, want %q", got.TenantID, tenant.id)
	}
	if got.Actor == "" || got.ActorScope != store.ScopeRoot {
		t.Errorf("actor = %q scope = %q, want a named root actor", got.Actor, got.ActorScope)
	}
	if got.ID == "" || got.At.IsZero() {
		t.Errorf("entry has no id or timestamp: %+v", got)
	}
}

// TestAuditRecordsRefusedMutations: an attempt to reach another tenant is
// exactly what someone reads this log to find, so a log of successes only would
// be silent about the thing it exists for.
func TestAuditRecordsRefusedMutations(t *testing.T) {
	h := newHarness(t)
	acme := h.newTenant("acme")
	other := h.newTenant("other")

	path := "/admin/v1/tenants/" + other.id + "/aliases/stolen"
	res := h.do(http.MethodPut, path, acme.adminKey, map[string]any{"pin": "gpt-4o"})
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)

	entries := auditEntries(t, h)
	if len(entries) == 0 {
		t.Fatal("audit log is empty")
	}
	got := entries[0]
	if got.Resource != path || got.Status != http.StatusNotFound {
		t.Fatalf("head entry = %+v, want the refused write to %s with status 404", got, path)
	}
	// The actor is the key that tried, not the tenant it tried to reach.
	if got.ActorScope != "tenant:"+acme.id {
		t.Errorf("actor scope = %q, want %q", got.ActorScope, "tenant:"+acme.id)
	}
}

// TestAuditNeverStoresRequestBodies is the GW-14 guarantee applied to this
// store. A provider registration carries plaintext upstream credentials, and an
// audit log that captured request bodies would hold a second copy of every
// secret the key vault exists to keep out of reach.
func TestAuditNeverStoresRequestBodies(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	const canary = "sk-audit-canary-do-not-log"
	res := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/providers", testBootstrapKey,
		map[string]any{
			"name":     "canary-provider",
			"base_url": "https://upstream.invalid",
			"keys":     []string{canary},
		})
	if res.status != http.StatusCreated {
		t.Fatalf("registering provider: status %d, body %s", res.status, res.body)
	}

	log := h.do(http.MethodGet, "/admin/v1/audit?limit=200", testBootstrapKey, nil)
	if strings.Contains(string(log.body), canary) {
		t.Error("the audit log contains a provider credential from a request body")
	}
	// The canary's own tail, in case a future change stores a truncated form.
	if strings.Contains(string(log.body), "do-not-log") {
		t.Error("the audit log contains part of a provider credential")
	}
}

// TestAuditRequiresRoot: a tenant able to read the log would learn which of its
// writes an operator had reversed, and a log the people it describes can read
// on demand is a weaker instrument than one they cannot.
func TestAuditRequiresRoot(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/admin/v1/audit", tenant.adminKey, nil)
	h.expectError(res, http.StatusForbidden, apierr.CodeInsufficientScope)
}

// --- tenant and provider updates -------------------------------------------

// TestUpdateTenantRenames covers the plain half of GW-6.AC-1's "updated": a
// tenant can be renamed in place, without deleting and recreating it and
// orphaning every key hanging off it.
func TestUpdateTenantRenames(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"name": "acme-renamed"})
	if res.status != http.StatusOK {
		t.Fatalf("renaming tenant: status %d, body %s", res.status, res.body)
	}
	var got store.Tenant
	res.decode(t, &got)
	if got.Name != "acme-renamed" {
		t.Errorf("name = %q, want %q", got.Name, "acme-renamed")
	}
	if got.ID != tenant.id {
		t.Errorf("id changed to %q; a rename must not re-identify the tenant", got.ID)
	}

	// The key minted before the rename still authenticates: renaming is not a
	// disguised recreate.
	if status := h.authenticates(tenant.id, tenant.adminKey); status != http.StatusOK {
		t.Errorf("key stopped working after a rename: status %d", status)
	}
}

// TestUpdateTenantSuspends completes a feature that was half-built: auth has
// always refused keys belonging to a suspended tenant, but until now nothing
// could set the field, so the check could never fire.
func TestUpdateTenantSuspends(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	if status := h.authenticates(tenant.id, tenant.adminKey); status != http.StatusOK {
		t.Fatalf("key does not work before suspension: status %d", status)
	}

	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"status": "suspended"})
	if res.status != http.StatusOK {
		t.Fatalf("suspending tenant: status %d, body %s", res.status, res.body)
	}

	// Both planes, because suspension is a property of the tenant rather than of
	// one credential: a suspended tenant whose data keys kept working would have
	// been suspended in the dashboard only.
	res = h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/keys", tenant.adminKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)
	res = h.do(http.MethodGet, "/v1/models", tenant.dataKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeInvalidAPIKey)

	// And back again, so suspension is a state rather than a one-way door.
	if res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"status": "active"}); res.status != http.StatusOK {
		t.Fatalf("reactivating tenant: status %d, body %s", res.status, res.body)
	}
	if status := h.authenticates(tenant.id, tenant.adminKey); status != http.StatusOK {
		t.Errorf("key still refused after reactivation: status %d", status)
	}
}

func TestUpdateTenantRejectsUnknownStatus(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{"status": "paused"})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "status")

	// An empty patch is refused too: it is always a mistake, and answering 200
	// to it would report that a change nobody made had been applied.
	res = h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id, testBootstrapKey,
		map[string]any{})
	h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
}

// TestUpdateProviderRotatesKeys is the rotation path. It has to be an update
// rather than delete-and-recreate: the provider id is what routing rules name,
// so recreating it would break every chain referencing it at the exact moment
// someone is responding to a leaked credential.
func TestUpdateProviderRotatesKeys(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	created := h.do(http.MethodPost, "/admin/v1/tenants/"+tenant.id+"/providers", testBootstrapKey,
		map[string]any{
			"name":     "rotating",
			"base_url": "https://upstream.invalid",
			"keys":     []string{"sk-original-aaaaaaaaaaaa"},
		})
	if created.status != http.StatusCreated {
		t.Fatalf("registering provider: status %d, body %s", created.status, created.body)
	}
	var before store.Provider
	created.decode(t, &before)

	const replacement = "sk-rotated-bbbbbbbbbbbb"
	res := h.do(http.MethodPatch, "/admin/v1/tenants/"+tenant.id+"/providers/"+before.ID, testBootstrapKey,
		map[string]any{"keys": []string{replacement}})
	if res.status != http.StatusOK {
		t.Fatalf("rotating keys: status %d, body %s", res.status, res.body)
	}
	var after store.Provider
	res.decode(t, &after)

	if after.ID != before.ID {
		t.Errorf("id changed from %q to %q; rotation must preserve it", before.ID, after.ID)
	}
	if len(after.KeyPrefixes) != 1 || after.KeyPrefixes[0] == before.KeyPrefixes[0] {
		t.Errorf("key_prefixes = %v, want one prefix that differs from %v",
			after.KeyPrefixes, before.KeyPrefixes)
	}
	// GW-6.AC-5: the stored credential is never returned in full, before or
	// after a rotation.
	if strings.Contains(string(res.body), replacement) {
		t.Error("the provider response contains the plaintext key")
	}
}

func TestUpdateProviderTogglesEnabledAndValidates(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	base := "/admin/v1/tenants/" + tenant.id + "/providers"

	created := h.do(http.MethodPost, base, testBootstrapKey, map[string]any{
		"name":     "toggled",
		"base_url": "https://upstream.invalid",
		"keys":     []string{"sk-toggle-cccccccccccc"},
	})
	if created.status != http.StatusCreated {
		t.Fatalf("registering provider: status %d, body %s", created.status, created.body)
	}
	var p store.Provider
	created.decode(t, &p)

	res := h.do(http.MethodPatch, base+"/"+p.ID, testBootstrapKey,
		map[string]any{"enabled": false})
	if res.status != http.StatusOK {
		t.Fatalf("disabling provider: status %d, body %s", res.status, res.body)
	}
	var disabled store.Provider
	res.decode(t, &disabled)
	if disabled.Enabled {
		t.Error("provider is still enabled after being disabled")
	}

	res = h.do(http.MethodPatch, base+"/"+p.ID, testBootstrapKey,
		map[string]any{"base_url": "not-a-url"})
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "base_url")

	// An explicitly empty pool is refused. A provider with no credentials could
	// never serve a request, so accepting it would only move the failure to the
	// next completion.
	res = h.do(http.MethodPatch, base+"/"+p.ID, testBootstrapKey,
		map[string]any{"keys": []string{}})
	body = h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectParam(t, body, "keys")

	res = h.do(http.MethodPatch, base+"/prv_nosuchprovider", testBootstrapKey,
		map[string]any{"enabled": true})
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)
}
