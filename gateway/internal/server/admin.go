package server

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/events"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// adminRoutes registers the GW-6 control plane.
//
// Every tenant-scoped route lives under /tenants/:tenant so authorisation is one
// rule applied in one place: a root key reaches any tenant, a tenant:<id> key
// reaches only its own. Flattening these into per-resource top-level routes would
// mean re-deriving the tenant — and re-checking the scope — in every handler.
func (s *Server) adminRoutes(g fiber.Router) {
	g.Get("/meta", s.adminMeta)

	g.Post("/catalog/refresh", s.refreshCatalog)

	g.Get("/audit", s.listAudit)

	// Root admin keys are not under /tenants because a root key belongs to no
	// tenant. Minting one through a tenant's collection would tie the only
	// credential that outranks every tenant to the lifetime of one of them —
	// deleting that tenant would take the operator's own access with it.
	g.Post("/admin-keys", s.createAdminKey)
	g.Get("/admin-keys", s.listAdminKeys)
	g.Delete("/admin-keys/:id", s.revokeAdminKey)

	g.Post("/tenants", s.createTenant)
	g.Get("/tenants", s.listTenants)
	g.Get("/tenants/:tenant", s.getTenant)
	g.Patch("/tenants/:tenant", s.updateTenant)
	g.Delete("/tenants/:tenant", s.deleteTenant)

	t := g.Group("/tenants/:tenant")

	t.Post("/keys", s.createKey)
	t.Get("/keys", s.listKeys)
	t.Delete("/keys/:id", s.revokeKey)

	t.Post("/providers", s.createProvider)
	t.Get("/providers", s.listProviders)
	t.Patch("/providers/:id", s.updateProvider)
	t.Delete("/providers/:id", s.deleteProvider)

	t.Put("/aliases/:name", s.upsertAlias)
	t.Get("/aliases", s.listAliases)
	t.Delete("/aliases/:name", s.deleteAlias)

	t.Put("/routing-rules", s.upsertRoute)
	t.Get("/routing-rules", s.listRoutes)
	t.Delete("/routing-rules/:id", s.deleteRoute)

	t.Put("/quota", s.setQuota)
	t.Get("/quota", s.getQuota)
	t.Delete("/quota", s.deleteQuota)

	// The same three handlers, addressed at one key. A key-level quota narrows
	// the tenant's; it is evaluated alongside it rather than instead of it, so
	// it can never widen what the tenant is allowed.
	t.Put("/keys/:id/quota", s.setQuota)
	t.Get("/keys/:id/quota", s.getQuota)
	t.Delete("/keys/:id/quota", s.deleteQuota)

	// A flush, not a delete: there is no cache resource to address, only a
	// pile of answers to be rid of. POST because it changes something, and
	// under the tenant because a cache belongs to exactly one (GW-12.AC-6).
	t.Post("/cache/flush", s.flushTenantCache)

	// The one place in the product where prompt content is served back out
	// (GW-14). Read-only, admin plane, tenant-scoped: there is no route that
	// creates a capture, because capture is a consequence of the tenant's
	// policy and traffic, not something an admin call can conjure.
	t.Get("/captures", s.listCaptures)

	t.Post("/webhooks", s.createWebhook)
	t.Get("/webhooks", s.listWebhooks)
	t.Delete("/webhooks/:id", s.deleteWebhook)

	// Read-only, and next to the webhooks because it is the same data by
	// another route: what a subscription would have pushed, a tenant can pull.
	// GW-8 requires both, since a webhook that was never registered — or whose
	// endpoint was down for all five attempts — must not be the difference
	// between an event happening and a tenant being able to find out.
	t.Get("/events", s.listEvents)

	t.Get("/usage", s.adminUsage)
	t.Get("/usage/breakdown", s.adminUsageBreakdown)
}

// --- authorisation ----------------------------------------------------------

// tenantScope resolves the path's tenant and checks the key may reach it.
//
// Reaching another tenant is 404, not 403. GW-6 requires it, and the reason is
// that 403 answers a question the caller was not entitled to ask: it confirms
// the tenant exists. Anyone holding one tenant's key could then enumerate the
// deployment's whole customer list by guessing ids and reading the status code.
//
// The refusal is built from the same constructor a genuinely missing tenant
// uses, so the two responses are identical down to the message. Distinguishing
// them by wording would restore the leak this closes.
func (s *Server) tenantScope(c *fiber.Ctx) (string, error) {
	id := strings.TrimSpace(param(c, "tenant"))
	if id == "" {
		return "", apierr.InvalidRequest("A tenant id is required.").WithParam("tenant")
	}
	key := httpx.Key(c)
	if key == nil {
		return "", apierr.InvalidAPIKey()
	}
	if key.Scope == store.ScopeRoot {
		return id, nil
	}
	if strings.TrimPrefix(key.Scope, "tenant:") == id {
		return id, nil
	}
	return "", apierr.ResourceNotFound("tenant", id)
}

// requireRoot guards the routes that create or destroy tenants themselves. A
// tenant-scoped key must not be able to mint another tenant, which would
// escape the boundary it was issued inside.
func requireRoot(c *fiber.Ctx) error {
	key := httpx.Key(c)
	if key == nil || key.Scope != store.ScopeRoot {
		return apierr.InsufficientScope()
	}
	return nil
}

// storeErr maps the store's sentinels onto the GW-7 registry so no handler has
// to decide what a missing row means in HTTP terms.
func storeErr(err error, kind, id string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return apierr.ResourceNotFound(kind, id).WithCause(err)
	case errors.Is(err, store.ErrConflict):
		return apierr.Conflict(err.Error()).WithCause(err)
	default:
		return err
	}
}

// parse reads a JSON body into dst, answering 400 rather than 500 on malformed
// input.
func parse(c *fiber.Ctx, dst any) error {
	if err := c.BodyParser(dst); err != nil {
		return apierr.InvalidRequest("Request body is not valid JSON.").WithCause(err)
	}
	return nil
}

// --- meta -------------------------------------------------------------------

// adminMetaResponse is /v1/meta's document with the two things only an admin key
// can be told: which scope the calling key holds, and the closed list of event
// types a webhook may subscribe to.
//
// The embedded document is the point. GW-9 requires an admin key to be able to
// feature-detect exactly as a data key does, so every field it names is the same
// value here, produced by the same builder. The additions are additions; a
// client reading either plane's response for `capabilities` finds the same list.
type adminMetaResponse struct {
	metaResponse
	Scope  string   `json:"scope"`
	Events []string `json:"events"`
}

func (s *Server) adminMeta(c *fiber.Ctx) error {
	key := httpx.Key(c)
	scope := ""
	if key != nil {
		scope = key.Scope
	}
	meta := s.meta(c)
	// The one field that differs, and it is not one GW-9 names: it says which
	// route answered, which is worth keeping now that the two bodies are
	// otherwise indistinguishable.
	meta.Object = "admin_meta"
	return c.JSON(adminMetaResponse{metaResponse: meta, Scope: scope, Events: eventRegistry})
}

// eventRegistry is the closed list of event types a webhook may subscribe to.
// Subscribing to a type outside it is rejected at creation rather than silently
// accepted and never delivered. It is the dispatcher's own list, so what the
// admin API accepts and what delivery knows how to raise cannot drift apart.
var eventRegistry = events.Registry

// --- catalog ----------------------------------------------------------------

// refreshCatalog is the on-demand poll GW-1 requires, so that a model added or
// retired at a provider becomes visible without waiting out the TTL or
// restarting anything.
//
// It is deliberately not under /tenants/:tenant. A root operator refreshing
// after a provider-side change wants every tenant, and making them enumerate
// tenants to do it would be the wrong shape for the one case that matters.
// Naming a tenant narrows it; a tenant-scoped key is narrowed to its own.
func (s *Server) refreshCatalog(c *fiber.Ctx) error {
	var req struct {
		Tenant string `json:"tenant"`
	}
	if len(c.Body()) > 0 {
		if err := parse(c, &req); err != nil {
			return httpx.Fail(c, err)
		}
	}
	req.Tenant = strings.TrimSpace(req.Tenant)

	key := httpx.Key(c)
	if key == nil {
		return httpx.Fail(c, apierr.InvalidAPIKey())
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	var targets []string
	if key.Scope == store.ScopeRoot {
		if req.Tenant != "" {
			targets = []string{req.Tenant}
		} else {
			tenants, err := s.Store.ListTenants(ctx)
			if err != nil {
				return httpx.Fail(c, apierr.From(err))
			}
			for _, t := range tenants {
				targets = append(targets, t.ID)
			}
		}
	} else {
		own := strings.TrimPrefix(key.Scope, "tenant:")
		if req.Tenant != "" && req.Tenant != own {
			// 404 for the same reason tenantScope answers 404: naming another
			// tenant must not reveal whether it exists.
			return httpx.Fail(c, apierr.ResourceNotFound("tenant", req.Tenant))
		}
		targets = []string{own}
	}

	// A named tenant that does not exist is a 404, not an empty success. Without
	// this check a typo would refresh nothing and report that it worked, since a
	// tenant with no providers and an unknown tenant produce the same empty
	// catalog.
	if req.Tenant != "" {
		if _, err := s.Store.GetTenant(ctx, req.Tenant); err != nil {
			return httpx.Fail(c, storeErr(err, "tenant", req.Tenant))
		}
	}

	// A provider that cannot be reached is reported per tenant rather than
	// failing the call: refreshing ten tenants and telling the operator only
	// about the first failure would hide the other nine results.
	refreshed := make([]fiber.Map, 0, len(targets))
	for _, id := range targets {
		entry := fiber.Map{"tenant": id}
		snap, err := s.Catalog.Refresh(ctx, id)
		switch {
		case err != nil:
			entry["ok"] = false
			entry["error"] = err.Error()
		default:
			entry["ok"] = !snap.Stale
			entry["models"] = len(snap.Models)
			entry["stale"] = snap.Stale
			if len(snap.Errors) > 0 {
				entry["errors"] = snap.Errors
			}
		}
		refreshed = append(refreshed, entry)
	}

	return c.JSON(fiber.Map{"object": "catalog_refresh", "refreshed": refreshed})
}

// --- tenants ----------------------------------------------------------------

func (s *Server) createTenant(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	if strings.TrimSpace(req.Name) == "" {
		return httpx.Fail(c, apierr.InvalidRequest("A tenant name is required.").WithParam("name"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	tenant, err := s.Store.CreateTenant(ctx, req.Name)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", req.Name))
	}
	s.seedAliases(ctx, tenant.ID)
	return c.Status(fiber.StatusCreated).JSON(tenant)
}

// seedAliases gives a new tenant the portable names from GW-2, so a caller has
// something to route to before anyone has configured anything. A failure here is
// logged rather than returned: the tenant exists, and refusing to report that
// because a convenience alias did not save would be the worse answer.
func (s *Server) seedAliases(ctx context.Context, tenantID string) {
	for _, seed := range routing.SeededAliases {
		a := seed
		a.TenantID = tenantID
		if _, err := s.Store.UpsertAlias(ctx, &a); err != nil {
			s.Logger.Warn("could not seed alias",
				slog.String("tenant", tenantID),
				slog.String("alias", a.Name),
				slog.String("error", err.Error()))
		}
	}
}

func (s *Server) listTenants(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	tenants, err := s.Store.ListTenants(ctx)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, tenants, func(t *store.Tenant) string { return t.ID })
}

func (s *Server) getTenant(c *fiber.Ctx) error {
	id, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	tenant, err := s.Store.GetTenant(ctx, id)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}
	return c.JSON(tenant)
}

// updateTenant renames a tenant or suspends it.
//
// Suspension is not new enforcement: authentication already refuses every key
// belonging to a suspended tenant. Until now nothing could set the field, so
// the check could never fire — this is the half of that feature that was
// missing, not a new one.
//
// Root only. A tenant able to un-suspend itself would make suspension
// advisory, which is not what an operator reaching for it wants.
func (s *Server) updateTenant(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "tenant")

	var req struct {
		Name         *string                   `json:"name"`
		Status       *string                   `json:"status"`
		Limits       *store.TenantLimits       `json:"limits"`
		Cache        *store.TenantCache        `json:"cache"`
		DebugCapture *store.TenantDebugCapture `json:"debug_capture"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	if req.Name == nil && req.Status == nil && req.Limits == nil &&
		req.Cache == nil && req.DebugCapture == nil {
		return httpx.Fail(c, apierr.
			InvalidRequest("A tenant update must change name, status, limits, cache or debug_capture."))
	}
	if req.Status != nil {
		switch *req.Status {
		case "active", "suspended":
		default:
			return httpx.Fail(c, apierr.
				InvalidRequest(`status must be "active" or "suspended".`).WithParam("status"))
		}
	}
	if req.Limits != nil {
		if err := s.validateTenantLimits(*req.Limits); err != nil {
			return httpx.Fail(c, err)
		}
	}
	if req.Cache != nil {
		if err := s.validateTenantCache(*req.Cache); err != nil {
			return httpx.Fail(c, err)
		}
	}
	if req.DebugCapture != nil {
		if err := s.validateTenantDebugCapture(*req.DebugCapture); err != nil {
			return httpx.Fail(c, err)
		}
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	// Read before the write, so the capture event can say whether this call
	// actually changed anything. Emitting on every PATCH that mentions
	// debug_capture would make the event history say "retention was turned on"
	// once per unrelated tenant rename that happened to echo the block back.
	var was bool
	if req.DebugCapture != nil {
		if prev, err := s.Store.GetTenant(ctx, id); err == nil {
			was = prev.DebugCapture.Enabled
		}
	}

	tenant, err := s.Store.UpdateTenant(ctx, id, store.TenantPatch{
		Name:         req.Name,
		Status:       req.Status,
		Limits:       req.Limits,
		Cache:        req.Cache,
		DebugCapture: req.DebugCapture,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}

	if req.DebugCapture != nil && was != tenant.DebugCapture.Enabled {
		s.emitCaptureChange(ctx, tenant)
	}

	// The tenant, plus whatever GW-14 insists the caller be told about what
	// they just asked for. Warnings is omitempty, so the ordinary response is
	// byte-for-byte the one every other build has returned.
	return c.JSON(tenantResponse{
		Tenant:   tenant,
		Warnings: s.captureWarnings(tenant.DebugCapture),
	})
}

// tenantResponse is a tenant with room for advice about it.
//
// The tenant is embedded so its fields stay at the top level: a caller that
// reads .id and .status keeps working, and GW-9 counts an added optional field
// as MINOR rather than the breaking change a nested {"tenant": …} would be.
type tenantResponse struct {
	*store.Tenant
	Warnings []string `json:"warnings,omitempty"`
}

// emitCaptureChange announces that retention started or stopped, carrying the
// policy but of course none of what it captures.
func (s *Server) emitCaptureChange(ctx context.Context, t *store.Tenant) {
	if s.Events == nil {
		return
	}
	typ := events.DebugCaptureDisabled
	data := map[string]any{"tenant": t.ID}
	if t.DebugCapture.Enabled {
		typ = events.DebugCaptureEnabled
		data["ttl_seconds"] = int(s.captureTTL(t).Seconds())
		data["sample_rate"] = s.captureSampleRate(t)
	}
	s.Events.Emit(ctx, t.ID, typ, data)
}

// deleteTenant destroys a tenant and everything under it.
//
// GW-6 requires ?confirm=<id> to match the path. This is the one admin route
// whose damage cannot be undone from the API — keys, providers, aliases, rules
// and quotas all go with it — and the id is long and opaque enough that no one
// types it twice by accident. The check is deliberately the whole id rather
// than a bare ?confirm=true, which would be satisfied by a mis-pasted URL.
func (s *Server) deleteTenant(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "tenant")

	if query(c, "confirm") != id {
		return httpx.Fail(c, apierr.
			InvalidRequest("deleting a tenant requires ?confirm=<id> matching the tenant in the path.").
			WithParam("confirm"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteTenant(ctx, id); err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}
	s.Catalog.Invalidate(id)
	s.quotas.invalidate(id)
	// Both of these hold request and response content for a tenant that no
	// longer exists (GW-14). Neither could ever be served again — a cache key
	// and a capture list are both reached through a tenant id whose credentials
	// have just gone — so leaving them would be retention with no reader, which
	// is the shape of retention nobody remembers to look for.
	s.cache.Flush(id)
	s.captures.Flush(id)
	return c.SendStatus(fiber.StatusNoContent)
}

// --- keys -------------------------------------------------------------------

func (s *Server) createKey(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Name      string     `json:"name"`
		Plane     string     `json:"plane"`
		Scope     string     `json:"scope"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}

	plane := store.Plane(strings.TrimSpace(req.Plane))
	if plane == "" {
		plane = store.PlaneData
	}
	if plane != store.PlaneData && plane != store.PlaneAdmin {
		return httpx.Fail(c, apierr.
			InvalidRequest(`plane must be "data" or "admin".`).WithParam("plane"))
	}

	// An admin key minted through a tenant is confined to that tenant. Honouring
	// a caller-supplied scope here would let any tenant admin issue itself a root
	// credential, which is the whole boundary this plane exists to hold.
	//
	// A non-root caller asking for root is refused rather than quietly given a
	// tenant-scoped key: silently downgrading a privilege request hands back a
	// credential that does less than the caller believes, and the mistake only
	// surfaces later as an unexplained 403 from whatever was built on it.
	scope := ""
	if plane == store.PlaneAdmin {
		scope = "tenant:" + tenantID
	}
	if req.Scope == store.ScopeRoot {
		if plane != store.PlaneAdmin {
			return httpx.Fail(c, apierr.
				InvalidRequest(`Only an admin-plane key can carry the "root" scope.`).
				WithParam("scope"))
		}
		if httpx.Key(c).Scope != store.ScopeRoot {
			return httpx.Fail(c, apierr.InsufficientScope())
		}
		scope = store.ScopeRoot
	} else if req.Scope != "" && req.Scope != scope {
		return httpx.Fail(c, apierr.
			InvalidRequest(`scope must be "root" or omitted; a tenant key's scope is derived from its tenant.`).
			WithParam("scope"))
	}

	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return httpx.Fail(c, apierr.
			InvalidRequest("expires_at must be in the future.").WithParam("expires_at"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	key, plaintext, err := s.Store.CreateAPIKey(ctx, tenantID, plane, req.Name, scope, req.ExpiresAt)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", tenantID))
	}

	return mintedKey(c, key, plaintext)
}

// mintedKey is the show-once response shared by both key-creation routes.
//
// The plaintext is returned exactly once, here. The store keeps only a hash, so
// there is no second chance to read it and no way for a database compromise to
// yield a working credential. Both routes answer in one shape so a caller
// cannot come to depend on one of them being readable later.
func mintedKey(c *fiber.Ctx, key *store.APIKey, plaintext string) error {
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"key":    key,
		"secret": plaintext,
		"warning": "This is the only time the secret is shown. Store it now; " +
			"it cannot be retrieved again.",
	})
}

// --- root admin keys --------------------------------------------------------

// createAdminKey mints a root admin credential, which belongs to no tenant.
//
// This is how a deployment rotates away from its bootstrap key. Without it the
// only root credential is the one in the environment, which cannot be revoked
// and cannot be replaced without a restart.
func (s *Server) createAdminKey(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return httpx.Fail(c, apierr.InvalidRequest("A key name is required.").WithParam("name"))
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return httpx.Fail(c, apierr.
			InvalidRequest("expires_at must be in the future.").WithParam("expires_at"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	// The plane and scope are not caller-supplied. This route exists only to
	// mint root credentials; a scope parameter here would be a second, less
	// guarded path to the privilege escalation createKey is careful to refuse.
	key, plaintext, err := s.Store.CreateAPIKey(ctx, "", store.PlaneAdmin, req.Name, store.ScopeRoot, req.ExpiresAt)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return mintedKey(c, key, plaintext)
}

func (s *Server) listAdminKeys(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	// The empty tenant id is what a root key is stored under, so this lists
	// exactly the tenant-less credentials and nothing else.
	keys, err := s.Store.ListAPIKeys(ctx, "")
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, keys, func(k *store.APIKey) string { return k.ID })
}

// revokeAdminKey retires a root credential.
//
// There is deliberately no guard against revoking the last one. The bootstrap
// key is checked at authentication time against the process environment rather
// than resolved through the store, so it survives any revocation here and
// remains the documented way back in.
func (s *Server) revokeAdminKey(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.RevokeAPIKey(ctx, "", id); err != nil {
		return httpx.Fail(c, storeErr(err, "key", id))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (s *Server) listKeys(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	keys, err := s.Store.ListAPIKeys(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, keys, func(k *store.APIKey) string { return k.ID })
}

func (s *Server) revokeKey(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.RevokeAPIKey(ctx, tenantID, id); err != nil {
		return httpx.Fail(c, storeErr(err, "key", id))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// --- providers --------------------------------------------------------------

func (s *Server) createProvider(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		BaseURL string   `json:"base_url"`
		Keys    []string `json:"keys"`
		Enabled *bool    `json:"enabled"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return httpx.Fail(c, apierr.InvalidRequest("A provider name is required.").WithParam("name"))
	}
	if err := validateBaseURL(req.BaseURL); err != nil {
		return httpx.Fail(c, err)
	}
	keys := nonEmpty(req.Keys)
	if len(keys) == 0 {
		return httpx.Fail(c, apierr.
			InvalidRequest("At least one provider API key is required.").WithParam("keys"))
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "openai"
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	p, err := s.Store.CreateProvider(ctx, &store.Provider{
		TenantID: tenantID,
		Name:     req.Name,
		Kind:     kind,
		BaseURL:  strings.TrimRight(req.BaseURL, "/"),
		Enabled:  enabled,
		Keys:     keys,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "provider", req.Name))
	}

	// A new provider changes what the tenant can route to, and waiting out the
	// catalog TTL to discover that would make the admin API feel broken.
	s.Catalog.Invalidate(tenantID)
	return c.Status(fiber.StatusCreated).JSON(p)
}

func (s *Server) listProviders(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	providers, err := s.Store.ListProviders(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, providers, func(p *store.Provider) string { return p.ID })
}

// updateProvider rotates a key pool, moves a base URL, or takes a provider out
// of rotation.
//
// Rotation has to be an update rather than a delete-and-recreate: the provider
// id is what routing rules and fallback chains name, so recreating it would
// silently break every chain that referenced it at the exact moment an operator
// was responding to a leaked credential.
//
// An explicitly empty keys array is refused. A provider with no credentials
// cannot serve anything, so accepting the write would only defer the failure to
// the next completion, where it surfaces as an upstream error rather than as
// the mistake it is.
func (s *Server) updateProvider(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	var req struct {
		BaseURL *string   `json:"base_url"`
		Enabled *bool     `json:"enabled"`
		Keys    *[]string `json:"keys"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	if req.BaseURL == nil && req.Enabled == nil && req.Keys == nil {
		return httpx.Fail(c, apierr.
			InvalidRequest("A provider update must change base_url, enabled or keys."))
	}

	patch := store.ProviderPatch{Enabled: req.Enabled}
	if req.BaseURL != nil {
		if err := validateBaseURL(*req.BaseURL); err != nil {
			return httpx.Fail(c, err)
		}
		patch.BaseURL = req.BaseURL
	}
	if req.Keys != nil {
		keys := nonEmpty(*req.Keys)
		if len(keys) == 0 {
			return httpx.Fail(c, apierr.
				InvalidRequest("At least one provider API key is required.").WithParam("keys"))
		}
		patch.Keys = keys
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	p, err := s.Store.UpdateProvider(ctx, tenantID, id, patch)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "provider", id))
	}

	// The catalog is keyed by what the provider was when it was last polled, so
	// a moved base URL or a rotated key has to invalidate it — otherwise the
	// gateway keeps dispatching against the old configuration until the TTL
	// expires, which is the opposite of what someone rotating a leaked key
	// needs.
	s.Catalog.Invalidate(tenantID)
	return c.JSON(p)
}

func (s *Server) deleteProvider(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteProvider(ctx, tenantID, id); err != nil {
		return httpx.Fail(c, storeErr(err, "provider", id))
	}
	s.Catalog.Invalidate(tenantID)
	return c.SendStatus(fiber.StatusNoContent)
}

func validateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return apierr.InvalidRequest("A provider base_url is required.").WithParam("base_url")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return apierr.
			InvalidRequest("base_url must be an absolute http or https URL.").
			WithParam("base_url")
	}
	return nil
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// --- aliases ----------------------------------------------------------------

func (s *Server) upsertAlias(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	name := strings.TrimSpace(param(c, "name"))
	if !routing.AliasNamePattern.MatchString(name) {
		return httpx.Fail(c, apierr.
			InvalidRequest("An alias name must match "+routing.AliasNamePattern.String()+".").
			WithParam("name"))
	}

	var req struct {
		Pin                string   `json:"pin"`
		Capabilities       []string `json:"capabilities"`
		MinContextWindow   int      `json:"min_context_window"`
		ProviderPreference []string `json:"provider_preference"`
		CostTier           string   `json:"cost_tier"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	switch req.CostTier {
	case "", "cheapest", "balanced", "best":
	default:
		return httpx.Fail(c, apierr.
			InvalidRequest(`cost_tier must be one of "cheapest", "balanced", "best".`).
			WithParam("cost_tier"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	// An alias that shadows a real model id would make the same request mean two
	// different things depending on catalog state. Refusing the alias is the only
	// resolution that leaves both names meaning what they say.
	//
	// A catalog that cannot be read skips the check rather than failing the write.
	// The collision is recoverable — deleting the alias restores the model id —
	// while refusing every alias change whenever a provider is unreachable would
	// make the admin API unusable during exactly the incident an operator is
	// trying to route around.
	if snap, cerr := s.Catalog.Get(ctx, tenantID); cerr == nil {
		if _, clash := snap.Lookup(name); clash {
			return httpx.Fail(c, apierr.AliasCollides(name))
		}
	}

	a, err := s.Store.UpsertAlias(ctx, &store.Alias{
		TenantID:           tenantID,
		Name:               name,
		Pin:                strings.TrimSpace(req.Pin),
		Capabilities:       req.Capabilities,
		MinContextWindow:   req.MinContextWindow,
		ProviderPreference: req.ProviderPreference,
		CostTier:           req.CostTier,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "alias", name))
	}
	return c.JSON(a)
}

func (s *Server) listAliases(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	aliases, err := s.Store.ListAliases(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, aliases, func(a *store.Alias) string { return a.ID })
}

func (s *Server) deleteAlias(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	name := param(c, "name")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteAlias(ctx, tenantID, name); err != nil {
		return httpx.Fail(c, storeErr(err, "alias", name))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// --- routes -----------------------------------------------------------------

func (s *Server) upsertRoute(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Match string   `json:"match"`
		Chain []string `json:"chain"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	req.Match = strings.TrimSpace(req.Match)
	if req.Match == "" {
		return httpx.Fail(c, apierr.
			InvalidRequest("A route match is required.").WithParam("match"))
	}
	chain := nonEmpty(req.Chain)
	if len(chain) == 0 {
		return httpx.Fail(c, apierr.
			InvalidRequest("A fallback chain needs at least one entry.").WithParam("chain"))
	}
	if len(chain) > s.Config.Routing.MaxFallbackDepth {
		return httpx.Fail(c, apierr.
			InvalidRequest("The fallback chain is longer than the configured max_fallback_depth.").
			WithParam("chain"))
	}
	if err := routing.ValidateChain(chain); err != nil {
		return httpx.Fail(c, err)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	r, err := s.Store.UpsertRoute(ctx, &store.Route{
		TenantID: tenantID,
		Match:    req.Match,
		Chain:    chain,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "route", req.Match))
	}
	return c.JSON(r)
}

func (s *Server) listRoutes(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	routes, err := s.Store.ListRoutes(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, routes, func(r *store.Route) string { return r.ID })
}

func (s *Server) deleteRoute(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteRoute(ctx, tenantID, id); err != nil {
		return httpx.Fail(c, storeErr(err, "route", id))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// --- quota ------------------------------------------------------------------

// quotaTarget resolves which quota a request addresses: the tenant's own, or
// the one narrowing a single key. The key is looked up rather than trusted, so
// a quota can never be written against a key id belonging to another tenant.
func (s *Server) quotaTarget(c *fiber.Ctx) (tenantID, keyID string, err error) {
	tenantID, err = s.tenantScope(c)
	if err != nil {
		return "", "", err
	}
	keyID = strings.TrimSpace(param(c, "id"))
	if keyID == "" {
		return tenantID, "", nil
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	keys, listErr := s.Store.ListAPIKeys(ctx, tenantID)
	if listErr != nil {
		return "", "", storeErr(listErr, "tenant", tenantID)
	}
	for _, k := range keys {
		if k.ID == keyID {
			return tenantID, keyID, nil
		}
	}
	return "", "", apierr.ResourceNotFound("key", keyID)
}

// quotaWindowRequest mirrors store.QuotaWindow on the wire. Both units are
// pointers so that omitting one leaves it unlimited rather than setting it to
// zero, which would be a cap nothing could pass.
type quotaWindowRequest struct {
	Tokens *store.QuotaLimit `json:"tokens"`
	Cost   *store.QuotaLimit `json:"cost"`
}

func (s *Server) setQuota(c *fiber.Ctx) error {
	tenantID, keyID, err := s.quotaTarget(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Day   quotaWindowRequest `json:"day"`
		Month quotaWindowRequest `json:"month"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}

	q := &store.Quota{
		TenantID: tenantID,
		KeyID:    keyID,
		Day:      store.QuotaWindow{Tokens: req.Day.Tokens, Cost: req.Day.Cost},
		Month:    store.QuotaWindow{Tokens: req.Month.Tokens, Cost: req.Month.Cost},
	}
	for _, slot := range []struct {
		param string
		limit *store.QuotaLimit
	}{
		{"day.tokens", q.Day.Tokens},
		{"day.cost", q.Day.Cost},
		{"month.tokens", q.Month.Tokens},
		{"month.cost", q.Month.Cost},
	} {
		if err := s.normaliseQuotaLimit(slot.limit, slot.param); err != nil {
			return httpx.Fail(c, err)
		}
	}
	if q.Empty() {
		return httpx.Fail(c, apierr.InvalidRequest(
			"A quota must configure at least one of day.tokens, day.cost, month.tokens or month.cost."))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	saved, err := s.Store.SetQuota(ctx, q)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", tenantID))
	}
	// Without this, raising a limit to unblock a customer would leave them
	// rejected for the rest of the cache TTL.
	s.quotas.invalidate(tenantID)
	return c.JSON(saved)
}

// normaliseQuotaLimit validates one slot in place and fills in the configured
// default threshold when the caller did not name one.
func (s *Server) normaliseQuotaLimit(limit *store.QuotaLimit, param string) error {
	if limit == nil {
		return nil
	}
	if limit.Cap < 0 {
		return apierr.InvalidRequest("A quota cap must not be negative.").
			WithParam(param + ".cap")
	}
	if limit.SoftThresholdPct == 0 {
		limit.SoftThresholdPct = s.Config.Quotas.DefaultSoftThresholdPct
	}
	if limit.SoftThresholdPct < 1 || limit.SoftThresholdPct > 100 {
		return apierr.InvalidRequest("soft_threshold_pct must be between 1 and 100.").
			WithParam(param + ".soft_threshold_pct")
	}
	return nil
}

func (s *Server) getQuota(c *fiber.Ctx) error {
	tenantID, keyID, err := s.quotaTarget(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	q, err := s.Store.GetQuota(ctx, tenantID, keyID)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "quota", tenantID))
	}
	return c.JSON(q)
}

func (s *Server) deleteQuota(c *fiber.Ctx) error {
	tenantID, keyID, err := s.quotaTarget(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteQuota(ctx, tenantID, keyID); err != nil {
		return httpx.Fail(c, storeErr(err, "quota", tenantID))
	}
	s.quotas.invalidate(tenantID)
	return c.SendStatus(fiber.StatusNoContent)
}

// --- webhooks ---------------------------------------------------------------

func (s *Server) createWebhook(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		URL     string   `json:"url"`
		Secret  string   `json:"secret"`
		Events  []string `json:"events"`
		Enabled *bool    `json:"enabled"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}
	if err := validateWebhookURL(req.URL); err != nil {
		return httpx.Fail(c, err)
	}
	if len(req.Secret) < 16 {
		return httpx.Fail(c, apierr.
			InvalidRequest("A webhook secret of at least 16 characters is required to sign deliveries.").
			WithParam("secret"))
	}
	events := nonEmpty(req.Events)
	if len(events) == 0 {
		return httpx.Fail(c, apierr.
			InvalidRequest("Subscribe to at least one event type.").WithParam("events"))
	}
	for _, e := range events {
		if !knownEvent(e) {
			return httpx.Fail(c, apierr.
				InvalidRequest("Unknown event type "+e+".").WithParam("events"))
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	w, err := s.Store.CreateWebhook(ctx, &store.Webhook{
		TenantID: tenantID,
		URL:      req.URL,
		Secret:   req.Secret,
		Events:   events,
		Enabled:  enabled,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", tenantID))
	}
	return c.Status(fiber.StatusCreated).JSON(w)
}

func (s *Server) listWebhooks(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	hooks, err := s.Store.ListWebhooks(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, hooks, func(w *store.Webhook) string { return w.ID })
}

func (s *Server) deleteWebhook(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "id")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteWebhook(ctx, tenantID, id); err != nil {
		return httpx.Fail(c, storeErr(err, "webhook", id))
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// listEvents serves GET /admin/v1/tenants/{id}/events: the tenant's recent
// event history, newest first.
//
// It is the delivery-independent half of GW-8's notification contract. Webhook
// delivery is at-least-once over five attempts, which is a promise about effort
// rather than about arrival, so the history is what makes "the gateway told you"
// true even when nothing was ever successfully posted anywhere. The bound is
// store.MaxTenantEvents; a poller that falls further behind than that loses the
// oldest, which is the trade a fixed-size backstop makes.
//
// Tenant-scoped like every other route under this group, so a tenant reads its
// own events and no one else's. The payloads carry gateway facts only — a model
// id, a provider name, a quota window — never request or response content
// (GW-14).
func (s *Server) listEvents(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	list, err := s.Store.ListEvents(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return sendPage(c, list, func(e *store.Event) string { return e.ID })
}

func knownEvent(name string) bool {
	for _, e := range eventRegistry {
		if e == name {
			return true
		}
	}
	return false
}

// validateWebhookURL rejects anything that is not an absolute http(s) URL.
//
// It does not attempt to block private address ranges: a self-hosted gateway is
// very often delivering to something on its own network, and a blocklist here
// would break the common case while a determined operator could defeat it with
// DNS anyway.
func validateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return apierr.InvalidRequest("A webhook url is required.").WithParam("url")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return apierr.
			InvalidRequest("url must be an absolute http or https URL.").WithParam("url")
	}
	return nil
}

// --- usage ------------------------------------------------------------------

// adminUsage is the same aggregate /v1/usage serves, reachable for any tenant
// the admin key's scope allows. Operators read usage for a tenant they are
// supporting far more often than they hold that tenant's data key.
func (s *Server) adminUsage(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	window, since, until, err := usageWindow(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	totals, err := s.Store.Usage(ctx, tenantID, since, until)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	return c.JSON(usageResponse{
		Object:      "usage",
		Window:      window,
		Since:       since.Format(time.RFC3339),
		Until:       until.Format(time.RFC3339),
		UsageTotals: totals,
	})
}

func (s *Server) adminUsageBreakdown(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	window, since, until, err := usageWindow(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	groupBy := strings.TrimSpace(query(c, "group_by", "model"))
	switch groupBy {
	case "model", "provider", "key":
	default:
		return httpx.Fail(c, apierr.
			InvalidRequest(`group_by must be one of "model", "provider", "key".`).
			WithParam("group_by"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	buckets, err := s.Store.UsageBreakdown(ctx, tenantID, since, until, groupBy)
	if err != nil {
		return httpx.Fail(c, apierr.From(err))
	}
	if buckets == nil {
		buckets = []store.UsageBucket{}
	}
	return c.JSON(breakdownResponse{
		Object:  "usage_breakdown",
		Window:  window,
		GroupBy: groupBy,
		Since:   since.Format(time.RFC3339),
		Until:   until.Format(time.RFC3339),
		Data:    buckets,
	})
}
