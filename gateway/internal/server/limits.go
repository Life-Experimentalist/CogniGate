package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// effectiveLimits is what one request is actually held to: the deployment's
// configuration, narrowed by the calling tenant's overrides (GW-13).
//
// It is a value with no pointers so that resolving it allocates nothing and
// needs no lock — every field is read from a Config fixed at startup and a
// Tenant the auth middleware already loaded.
type effectiveLimits struct {
	MaxRequestBytes     int64
	RequestTimeout      time.Duration
	StreamIdleTimeout   time.Duration
	MaxConcurrentPerKey int
	RequestsPerSecond   int
	BurstCapacity       int
}

// limits resolves the limits in force for this request.
//
// It reads the tenant off the request rather than a value stashed by
// limitTenant, so both planes answer identically: GW-9 requires /admin/v1/meta
// to publish the same document /v1/meta does, and a limits block that depended
// on which middleware chain had run would publish two.
//
// An unauthenticated request — /healthz, /metrics, anything rejected before
// auth — gets the deployment's own numbers, which is the only answer available
// and the right one: those are the limits it was actually held to.
func (s *Server) limits(c *fiber.Ctx) effectiveLimits {
	return s.limitsFor(httpx.Tenant(c))
}

// limitsFor narrows the deployment ceilings by a tenant's overrides.
//
// Narrowing only. An override above the ceiling is rejected by the admin API
// with 400, and ignored here as well: validation and enforcement disagreeing is
// how a stored value from an older, laxer build turns into a tenant quietly
// consuming more of the process than it was sized for.
func (s *Server) limitsFor(t *store.Tenant) effectiveLimits {
	dep := s.Config.Limits
	eff := effectiveLimits{
		MaxRequestBytes:     dep.MaxRequestBytes,
		RequestTimeout:      dep.RequestTimeout,
		StreamIdleTimeout:   dep.StreamIdleTimeout,
		MaxConcurrentPerKey: dep.MaxConcurrentPerKey,
		RequestsPerSecond:   s.Config.RateLimit.RequestsPerSecond,
		BurstCapacity:       s.Config.RateLimit.BurstCapacity,
	}
	if t == nil {
		return eff
	}
	o := t.Limits
	eff.MaxRequestBytes = lowerInt64(eff.MaxRequestBytes, o.MaxRequestBytes)
	eff.RequestTimeout = lowerDuration(eff.RequestTimeout, o.RequestTimeoutSeconds)
	eff.StreamIdleTimeout = lowerDuration(eff.StreamIdleTimeout, o.StreamIdleTimeoutSeconds)
	eff.MaxConcurrentPerKey = lowerInt(eff.MaxConcurrentPerKey, o.MaxConcurrentPerKey)
	eff.RequestsPerSecond = lowerInt(eff.RequestsPerSecond, o.RequestsPerSecond)
	eff.BurstCapacity = lowerInt(eff.BurstCapacity, o.BurstCapacity)
	return eff
}

// The three below take the smaller of ceiling and override, treating zero — the
// encoding of "no override" throughout store.TenantLimits — as absent rather
// than as a limit of nothing.
func lowerInt64(ceiling, override int64) int64 {
	if override <= 0 || override > ceiling {
		return ceiling
	}
	return override
}

func lowerInt(ceiling, override int) int {
	if override <= 0 || override > ceiling {
		return ceiling
	}
	return override
}

func lowerDuration(ceiling time.Duration, overrideSeconds int) time.Duration {
	return time.Duration(lowerInt64(int64(ceiling), int64(overrideSeconds)*int64(time.Second)))
}

// validateTenantLimits rejects an override the deployment cannot honour.
//
// Above the ceiling is refused rather than clamped. Silently lowering it would
// leave the operator reading back a tenant they did not configure, and the
// mistake they made — sizing a tenant against a process that is smaller than
// they thought — is exactly the one worth being told about.
//
// Zero is absent, not "no limit". Negative is neither, and is refused: it is a
// typo or a unit confusion, and both deserve an error rather than a limit that
// silently reverts to the deployment's.
func (s *Server) validateTenantLimits(l store.TenantLimits) *apierr.Error {
	dep := s.Config.Limits
	for _, f := range []struct {
		param   string
		value   int64
		ceiling int64
	}{
		{"limits.max_request_bytes", l.MaxRequestBytes, dep.MaxRequestBytes},
		{"limits.request_timeout_seconds", int64(l.RequestTimeoutSeconds), int64(dep.RequestTimeout.Seconds())},
		{"limits.stream_idle_timeout_seconds", int64(l.StreamIdleTimeoutSeconds), int64(dep.StreamIdleTimeout.Seconds())},
		{"limits.max_concurrent_per_key", int64(l.MaxConcurrentPerKey), int64(dep.MaxConcurrentPerKey)},
		{"limits.requests_per_second", int64(l.RequestsPerSecond), int64(s.Config.RateLimit.RequestsPerSecond)},
		{"limits.burst_capacity", int64(l.BurstCapacity), int64(s.Config.RateLimit.BurstCapacity)},
	} {
		switch {
		case f.value < 0:
			return apierr.InvalidRequest(fmt.Sprintf(
				"%s must not be negative.", f.param)).WithParam(f.param)
		case f.value > f.ceiling:
			return apierr.InvalidRequest(fmt.Sprintf(
				"%s is %d, above the deployment ceiling of %d. A tenant limit may only narrow one.",
				f.param, f.value, f.ceiling)).WithParam(f.param)
		}
	}
	return nil
}

// limitTenant applies the three GW-13 limits that can only be decided once the
// caller is known: the tenant's own body ceiling, its request rate, and its
// per-key concurrency.
//
// The checks run cheapest-first, and concurrency runs last because it is the
// only one that holds anything: its slot has to cover the handler, so it must be
// the last thing between authentication and the route.
//
// Each produces a different 429-family code, which GW-13.AC-7 requires to stay
// distinct — a caller with too many requests open right now needs to retry
// differently from one that has spent its budget for the month.
func (s *Server) limitTenant() fiber.Handler {
	return func(c *fiber.Ctx) error {
		key, tenant := httpx.Key(c), httpx.Tenant(c)
		if key == nil || tenant == nil {
			return c.Next()
		}
		lim := s.limitsFor(tenant)

		// The deployment ceiling was already applied before auth, so this fires
		// only in the band between a tenant's own limit and the process's. It is
		// two integer comparisons on a body fasthttp has finished reading either
		// way, which is why it is worth doing here rather than teaching limitBody
		// to look up a tenant it runs too early to have.
		if declared := c.Request().Header.ContentLength(); int64(declared) > lim.MaxRequestBytes {
			return httpx.Fail(c, apierr.RequestTooLarge(lim.MaxRequestBytes))
		}
		if int64(len(c.Body())) > lim.MaxRequestBytes {
			return httpx.Fail(c, apierr.RequestTooLarge(lim.MaxRequestBytes))
		}

		if wait, ok := s.rates.take(tenant.ID, lim.RequestsPerSecond, lim.BurstCapacity); !ok {
			return httpx.Fail(c, apierr.RateLimited().WithRetryAfter(wait))
		}

		release, ok := s.limiter.acquire(key.ID, lim.MaxConcurrentPerKey)
		if !ok {
			return httpx.Fail(c, apierr.ConcurrencyExceeded(lim.MaxConcurrentPerKey))
		}
		defer release()
		return c.Next()
	}
}

// --- rate limiter -----------------------------------------------------------

// rateLimiter is a per-tenant token bucket: requests_per_second refills it and
// burst_capacity bounds it, so a client may spend a burst at once and then
// settles to the sustained rate.
//
// Per tenant rather than per key, because the rate is what the tenant is paying
// for: splitting it across keys would let a tenant lift its own ceiling by
// minting more of them. Concurrency goes the other way and is per key — see
// concurrencyLimiter — because that limit exists to stop one integration
// starving another inside the same tenant.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	// now is injectable so a test can spend a bucket and refill it without
	// sleeping through the refill interval.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// sweepAbove is the bucket count past which take drops the ones it can prove are
// full. A full bucket admits every request, so forgetting it changes no answer;
// all the map buys is memory of a caller that is not currently spending. The
// threshold is high enough that an ordinary deployment never sweeps, and low
// enough that a gateway seeing a long tail of tenants does not keep one entry
// per tenant forever.
const sweepAbove = 4096

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: map[string]*bucket{}, now: time.Now}
}

// take spends one token. It reports how long to wait when there is none, which
// becomes the Retry-After header: a client told only "too many requests" has to
// guess, and guesses badly.
func (l *rateLimiter) take(id string, rps, burst int) (time.Duration, bool) {
	if rps < 1 || burst < 1 {
		// A deployment that configured the rate limit away is not rate limited.
		// Refusing everything is the other reading of zero, and it is not one
		// any operator who sets it intends.
		return 0, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[id]
	if !ok {
		b = &bucket{tokens: float64(burst)}
		l.buckets[id] = b
		if len(l.buckets) > sweepAbove {
			l.sweep(now, rps, burst)
		}
	} else {
		b.tokens += now.Sub(b.last).Seconds() * float64(rps)
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
	}
	b.last = now

	if b.tokens < 1 {
		// Time until one whole token exists. WithRetryAfter rounds it up to a
		// second, which is the finest unit the header has.
		return time.Duration((1 - b.tokens) / float64(rps) * float64(time.Second)), false
	}
	b.tokens--
	return 0, true
}

// sweep drops every bucket idle long enough to have refilled. Called with the
// lock held.
func (l *rateLimiter) sweep(now time.Time, rps, burst int) {
	full := time.Duration(float64(burst) / float64(rps) * float64(time.Second))
	for id, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, id)
		}
	}
}
