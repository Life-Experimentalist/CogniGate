package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/cache"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// newResponseCache builds the GW-12 cache, or nil when the deployment has not
// enabled it. Nil rather than an empty cache so that a disabled deployment
// cannot accumulate entries it will never serve.
func newResponseCache(c config.Cache) *cache.Cache {
	if !c.Enabled {
		return nil
	}
	return cache.New(c.MaxBytes, c.MaxEntryBytes)
}

// The three values X-CogniGate-Cache ever carries on a response (GW-12).
const (
	cacheHit    = "hit"
	cacheMiss   = "miss"
	cacheBypass = "bypass"
)

// setCacheStatus tells the caller and the log line the same thing at once.
//
// The header is what the caller reads; the outcome field is what an operator
// reads afterwards, and the two cannot disagree because nothing sets one
// without the other.
func setCacheStatus(c *fiber.Ctx, status string) {
	c.Set(httpx.HeaderCache, status)
	out := httpx.GetOutcome(c)
	out.CacheStatus = status
	httpx.SetOutcome(c, out)
}

// cachePlan is what one request's caching works out to, decided once so that
// the lookup, the store and the header cannot disagree.
//
// An empty plan is the silent one: no lookup, no header. That is the answer for
// a deployment with caching off and for a caller that never opted in, and the
// two are indistinguishable from outside on purpose — GW-12.AC-7 requires the
// header to be absent when the capability is, and a caller who did not ask
// about the cache is owed no commentary on it.
type cachePlan struct {
	lookup bool
	header string
	ttl    time.Duration
}

// planCache decides whether this request may be served from, and stored in, the
// cache.
//
// The order matters. The deployment switch comes first because it must be able
// to silence the header entirely; explicit bypass comes next because GW-12 makes
// it override policy rather than negotiate with it; and eligibility comes last
// because a request that cannot be cached still deserves to be told so when it
// asked to be.
func (s *Server) planCache(c *fiber.Ctx, env chatEnvelope) cachePlan {
	if !s.Config.Cache.Enabled || s.cache == nil {
		return cachePlan{}
	}

	tenant := httpx.Tenant(c)
	switch strings.ToLower(strings.TrimSpace(c.Get(httpx.HeaderCache))) {
	case "bypass":
		return cachePlan{header: cacheBypass}
	case "prefer":
	default:
		if tenant == nil || !tenant.Cache.Enabled {
			return cachePlan{}
		}
	}

	// Opted in. Whether it can be honoured is a property of the request.
	if env.Stream || !env.deterministic() {
		return cachePlan{header: cacheBypass}
	}
	return cachePlan{lookup: true, ttl: s.cacheTTL(tenant)}
}

// cacheTTL is how long this tenant's entries live.
//
// The deployment ceiling is applied here as well as in validateTenantCache, for
// the reason limitsFor gives: a value stored by an older, laxer build must not
// outlive the rule that would refuse it today.
func (s *Server) cacheTTL(t *store.Tenant) time.Duration {
	ttl := s.Config.Cache.DefaultTTL
	if t != nil && t.Cache.TTLSeconds > 0 {
		ttl = time.Duration(t.Cache.TTLSeconds) * time.Second
	}
	if max := s.Config.Cache.MaxTTL; ttl > max {
		return max
	}
	return ttl
}

// validateTenantCache rejects a policy the deployment cannot honour, refusing
// rather than clamping for the reason validateTenantLimits does.
func (s *Server) validateTenantCache(p store.TenantCache) *apierr.Error {
	switch {
	case p.TTLSeconds < 0:
		return apierr.InvalidRequest("cache.ttl_seconds must not be negative.").
			WithParam("cache.ttl_seconds")
	case time.Duration(p.TTLSeconds)*time.Second > s.Config.Cache.MaxTTL:
		return apierr.InvalidRequest(fmt.Sprintf(
			"cache.ttl_seconds is %d, above the deployment ceiling of %d.",
			p.TTLSeconds, int(s.Config.Cache.MaxTTL.Seconds()))).
			WithParam("cache.ttl_seconds")
	}
	return nil
}

// flushTenantCache empties one tenant's cache (GW-12.AC-6).
//
// Root or that tenant's own admin: a tenant clearing its own cache costs nobody
// else anything, and refusing it would leave an operator with a stale answer and
// no way to be rid of it short of calling whoever holds the root key.
func (s *Server) flushTenantCache(c *fiber.Ctx) error {
	id, err := s.tenantScope(c)
	if err != nil {
		return httpx.Fail(c, err)
	}

	ctx, cancel := s.opContext(c)
	defer cancel()
	if _, err := s.Store.GetTenant(ctx, id); err != nil {
		return httpx.Fail(c, storeErr(err, "tenant", id))
	}

	// Answered even when caching is off, where it is always zero: an operator
	// scripting a flush should not have to branch on the deployment's config to
	// know whether the call worked.
	return c.JSON(fiber.Map{"flushed": s.cache.Flush(id)})
}
