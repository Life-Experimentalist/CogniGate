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

	g.Post("/tenants", s.createTenant)
	g.Get("/tenants", s.listTenants)
	g.Get("/tenants/:tenant", s.getTenant)
	g.Delete("/tenants/:tenant", s.deleteTenant)

	t := g.Group("/tenants/:tenant")

	t.Post("/keys", s.createKey)
	t.Get("/keys", s.listKeys)
	t.Delete("/keys/:id", s.revokeKey)

	t.Post("/providers", s.createProvider)
	t.Get("/providers", s.listProviders)
	t.Delete("/providers/:id", s.deleteProvider)

	t.Put("/aliases/:name", s.upsertAlias)
	t.Get("/aliases", s.listAliases)
	t.Delete("/aliases/:name", s.deleteAlias)

	t.Put("/routes", s.upsertRoute)
	t.Get("/routes", s.listRoutes)
	t.Delete("/routes/:id", s.deleteRoute)

	t.Put("/quota", s.setQuota)
	t.Get("/quota", s.getQuota)
	t.Delete("/quota", s.deleteQuota)

	t.Post("/webhooks", s.createWebhook)
	t.Get("/webhooks", s.listWebhooks)
	t.Delete("/webhooks/:id", s.deleteWebhook)

	t.Get("/usage", s.adminUsage)
	t.Get("/usage/breakdown", s.adminUsageBreakdown)
}

// --- authorisation ----------------------------------------------------------

// tenantScope resolves the path's tenant and checks the key may reach it.
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
	return "", apierr.InsufficientScope()
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

func (s *Server) adminMeta(c *fiber.Ctx) error {
	key := httpx.Key(c)
	scope := ""
	if key != nil {
		scope = key.Scope
	}
	return c.JSON(fiber.Map{
		"object":  "admin_meta",
		"version": s.version(),
		"store":   s.Store.Kind(),
		"scope":   scope,
		"events":  eventRegistry,
	})
}

// eventRegistry is the closed list of event types a webhook may subscribe to.
// Subscribing to a type outside it is rejected at creation rather than silently
// accepted and never delivered. It is the dispatcher's own list, so what the
// admin API accepts and what delivery knows how to raise cannot drift apart.
var eventRegistry = events.Registry

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
	return c.JSON(fiber.Map{"object": "list", "data": tenants})
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

func (s *Server) deleteTenant(c *fiber.Ctx) error {
	if err := requireRoot(c); err != nil {
		return httpx.Fail(c, err)
	}
	id := param(c, "tenant")

	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteTenant(ctx, id); err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}
	s.Catalog.Invalidate(id)
	s.quotas.invalidate(id)
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

	// The plaintext is returned exactly once, here. The store keeps only a hash,
	// so there is no second chance to read it and no way for a database
	// compromise to yield a working credential.
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"key":    key,
		"secret": plaintext,
		"warning": "This is the only time the secret is shown. Store it now; " +
			"it cannot be retrieved again.",
	})
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
	return c.JSON(fiber.Map{"object": "list", "data": keys})
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
	return c.JSON(fiber.Map{"object": "list", "data": providers})
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
	return c.JSON(fiber.Map{"object": "list", "data": aliases})
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
	return c.JSON(fiber.Map{"object": "list", "data": routes})
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

func (s *Server) setQuota(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	var req struct {
		Period           string  `json:"period"`
		TokenLimit       int64   `json:"token_limit"`
		SpendLimitUSD    float64 `json:"spend_limit_usd"`
		SoftThresholdPct int     `json:"soft_threshold_pct"`
	}
	if err := parse(c, &req); err != nil {
		return httpx.Fail(c, err)
	}

	if req.Period == "" {
		req.Period = "month"
	}
	if req.Period != "day" && req.Period != "month" {
		return httpx.Fail(c, apierr.
			InvalidRequest(`period must be "day" or "month".`).WithParam("period"))
	}
	if req.TokenLimit < 0 || req.SpendLimitUSD < 0 {
		return httpx.Fail(c, apierr.InvalidRequest("Limits must not be negative."))
	}
	if req.SoftThresholdPct == 0 {
		req.SoftThresholdPct = s.Config.Quotas.DefaultSoftThresholdPct
	}
	if req.SoftThresholdPct < 1 || req.SoftThresholdPct > 100 {
		return httpx.Fail(c, apierr.
			InvalidRequest("soft_threshold_pct must be between 1 and 100.").
			WithParam("soft_threshold_pct"))
	}

	ctx, cancel := s.opContext(c)
	defer cancel()

	q, err := s.Store.SetQuota(ctx, &store.Quota{
		TenantID:         tenantID,
		Period:           req.Period,
		TokenLimit:       req.TokenLimit,
		SpendLimitUSD:    req.SpendLimitUSD,
		SoftThresholdPct: req.SoftThresholdPct,
	})
	if err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", tenantID))
	}
	// Without this, raising a limit to unblock a customer would leave them
	// rejected for the rest of the cache TTL.
	s.quotas.invalidate(tenantID)
	return c.JSON(q)
}

func (s *Server) getQuota(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	q, err := s.Store.GetQuota(ctx, tenantID)
	if err != nil {
		return httpx.Fail(c, storeErr(err, "quota", tenantID))
	}
	return c.JSON(q)
}

func (s *Server) deleteQuota(c *fiber.Ctx) error {
	tenantID, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}
	ctx, cancel := s.opContext(c)
	defer cancel()

	if err := s.Store.DeleteQuota(ctx, tenantID); err != nil {
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
	return c.JSON(fiber.Map{"object": "list", "data": hooks})
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
