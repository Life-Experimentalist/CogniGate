package server

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// identify mints the request id and captures the caller's own correlation id.
//
// It runs before everything else, including recovery, so that the one string a
// user can quote about a failure is on the response no matter how the request
// ends.
func (s *Server) identify() fiber.Handler {
	return func(c *fiber.Ctx) error {
		httpx.SetRequestID(c, httpx.NewRequestID())
		if v := header(c, httpx.HeaderClientRequestID); v != "" {
			httpx.SetClientRequestID(c, v)
		}
		return c.Next()
	}
}

// recover turns a panic into the GW-7 envelope.
//
// Fiber ships a recovery middleware, but it re-raises through the ErrorHandler
// as a bare 500 and logs the stack to stdout unstructured. Owning it here keeps
// the panic attributable to a request id and keeps the response shaped like
// every other error the gateway emits.
func (s *Server) recover() fiber.Handler {
	return func(c *fiber.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				s.Logger.Error("handler panicked",
					slog.String("request_id", httpx.RequestID(c)),
					slog.String("route", c.Route().Path),
					slog.Any("panic", r))
				// The panic value is deliberately not surfaced: it routinely
				// quotes the value being operated on, which under GW-14 may be
				// request content.
				err = httpx.Fail(c, apierr.From(fmt.Errorf("panic: %v", r)))
			}
		}()
		return c.Next()
	}
}

// observe records the request in the metrics and the structured log.
func (s *Server) observe() fiber.Handler {
	return func(c *fiber.Ctx) error {
		started := time.Now()
		err := c.Next()
		elapsed := time.Since(started)

		route := c.Route().Path
		if route == "" {
			route = "unmatched"
		}
		tenant := httpx.TenantID(c)

		if s.Metrics != nil {
			s.Metrics.Requests.WithLabelValues(tenant, route, statusClass(c.Response().StatusCode())).Inc()
			s.Metrics.RequestDuration.WithLabelValues(tenant, route).Observe(elapsed.Seconds())
		}

		level := slog.LevelInfo
		if c.Response().StatusCode() >= 500 {
			level = slog.LevelError
		}
		// Nothing derived from the body is logged — not the model, not a
		// parameter, and certainly not a message. Route, status and timing are
		// what an operator needs, and they are all GW-14 permits.
		s.Logger.Log(c.UserContext(), level, "request",
			slog.String("request_id", httpx.RequestID(c)),
			slog.String("client_request_id", httpx.ClientRequestID(c)),
			slog.String("tenant", tenant),
			slog.String("method", c.Method()),
			slog.String("route", route),
			slog.Int("status", c.Response().StatusCode()),
			slog.Int64("duration_ms", elapsed.Milliseconds()))

		return err
	}
}

// auth enforces the two-plane credential rule.
//
// The plane is read from the credential's own prefix before any store lookup,
// which is what lets a real-but-misdirected key be answered with wrong_plane
// instead of a flat rejection that sends the caller hunting for a typo.
func (s *Server) auth(want store.Plane) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := bearer(c)
		if raw == "" {
			return httpx.Fail(c, apierr.InvalidAPIKey())
		}

		// The bootstrap key is checked first and without a prefix requirement:
		// it is the credential that exists before the store has anything in it,
		// so it cannot be resolved through the store and cannot be assumed to
		// have been minted by it.
		if want == store.PlaneAdmin && s.bootstrapMatches(raw) {
			httpx.SetAuth(c, &store.APIKey{
				ID:     "bootstrap",
				Plane:  store.PlaneAdmin,
				Name:   "bootstrap",
				Prefix: "bootstrap",
				Scope:  store.ScopeRoot,
			}, nil)
			return c.Next()
		}

		plane, ok := store.PlaneOf(raw)
		if !ok {
			return httpx.Fail(c, apierr.InvalidAPIKey())
		}
		if plane != want {
			return httpx.Fail(c, apierr.WrongPlane(string(want)))
		}

		key, tenant, err := s.Store.ResolveKey(c.UserContext(), raw)
		if err != nil {
			return httpx.Fail(c, apierr.InvalidAPIKey().WithCause(err))
		}
		if key.Plane != want {
			// A key whose prefix and record disagree. Treat the record as
			// authoritative and refuse.
			return httpx.Fail(c, apierr.WrongPlane(string(want)))
		}
		if tenant != nil && tenant.Status == "suspended" {
			return httpx.Fail(c, apierr.InvalidAPIKey())
		}
		httpx.SetAuth(c, key, tenant)
		return c.Next()
	}
}

func (s *Server) bootstrapMatches(raw string) bool {
	want := s.Config.Admin.BootstrapKey
	// A short bootstrap key is worse than none: it is a root credential on an
	// unauthenticated-by-default surface. Refusing to honour it is safer than
	// letting a placeholder become the deployment's admin password.
	if len(want) < minBootstrapKeyLen {
		return false
	}
	return store.ConstantTimeEqual(raw, want)
}

// minBootstrapKeyLen is the shortest admin bootstrap credential the gateway will
// accept. It is a floor on entropy, not a policy: anything shorter is almost
// certainly a placeholder copied out of an example.
const minBootstrapKeyLen = 16

// limitConcurrency caps simultaneous in-flight requests per credential (GW-13).
//
// Per key rather than per tenant: a runaway job holding one key should not be
// able to starve the tenant's other integrations, and the caller who needs to
// fix it is the one holding that key.
func (s *Server) limitConcurrency() fiber.Handler {
	return func(c *fiber.Ctx) error {
		key := httpx.Key(c)
		if key == nil {
			return c.Next()
		}
		release, ok := s.limiter.acquire(key.ID)
		if !ok {
			return httpx.Fail(c, apierr.ConcurrencyExceeded(s.Config.Limits.MaxConcurrentPerKey))
		}
		defer release()
		return c.Next()
	}
}

// limitBody enforces max_request_bytes as a GW-7 error rather than as a
// transport failure.
//
// Fiber's own BodyLimit is applied by fasthttp while the request is still being
// read, which is before any handler — including the framework's ErrorHandler —
// can shape the response. A caller over the limit therefore gets a plain-text
// "body size exceeds the given limit" and a closed connection, which no OpenAI
// client can parse and which carries no request id to quote in a support
// ticket. So the configured limit is enforced here, and BodyLimit is left
// higher: it stays as the backstop against a body large enough to be an attack,
// where refusing to buffer it at all is the correct answer and a pretty error is
// not worth the memory.
func (s *Server) limitBody() fiber.Handler {
	limit := s.Config.Limits.MaxRequestBytes
	return func(c *fiber.Ctx) error {
		// Content-Length first: rejecting on the declared size avoids touching a
		// body the gateway has already decided not to accept.
		if declared := c.Request().Header.ContentLength(); int64(declared) > limit {
			return httpx.Fail(c, apierr.RequestTooLarge(limit))
		}
		// Chunked requests declare no length, so the buffered body is the only
		// authority for them.
		if int64(len(c.Body())) > limit {
			return httpx.Fail(c, apierr.RequestTooLarge(limit))
		}
		return c.Next()
	}
}

// opContext gives a handler a deadline of its own.
//
// Streaming completions deliberately do not use this: a stream that is still
// producing tokens is working, and killing it at request_timeout would cap every
// long generation at two minutes. GW-13 governs those with stream_idle_timeout
// instead, which measures silence rather than duration.
func (s *Server) opContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.UserContext(), s.Config.Limits.RequestTimeout)
}

// bearer extracts the credential from the Authorization header.
func bearer(c *fiber.Ctx) string {
	h := header(c, fiber.HeaderAuthorization)
	if h == "" {
		return ""
	}
	const scheme = "bearer "
	if len(h) > len(scheme) && strings.EqualFold(h[:len(scheme)], scheme) {
		return strings.TrimSpace(h[len(scheme):])
	}
	// A bare token is accepted because enough client libraries send one, and
	// rejecting it would fail the request with an authentication error that
	// tells the caller nothing about the real problem.
	return strings.TrimSpace(h)
}

// statusClass buckets a status for the metrics label. The class rather than the
// code keeps cardinality flat while still separating the three cases anyone
// alerts on.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// --- concurrency limiter ----------------------------------------------------

// concurrencyLimiter counts in-flight requests per credential.
//
// It is a counter map rather than a semaphore pool because the limit is a
// rejection threshold, not a queue: GW-13 requires 429 concurrency_exceeded,
// and a caller who wanted to wait would rather do so with their own backoff
// than inside an opaque gateway queue.
type concurrencyLimiter struct {
	mu    sync.Mutex
	inUse map[string]int
	limit int
}

func newConcurrencyLimiter(limit int) *concurrencyLimiter {
	if limit < 1 {
		limit = 32
	}
	return &concurrencyLimiter{inUse: map[string]int{}, limit: limit}
}

func (l *concurrencyLimiter) acquire(key string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.inUse[key] >= l.limit {
		return nil, false
	}
	l.inUse[key]++

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.inUse[key] <= 1 {
				// Deleting rather than storing zero keeps the map proportional
				// to active keys instead of to every key ever seen.
				delete(l.inUse, key)
				return
			}
			l.inUse[key]--
		})
	}, true
}

// --- small parsing helpers --------------------------------------------------

// queryInt reads a bounded integer query parameter.
func queryInt(c *fiber.Ctx, name string, def, min, max int) int {
	raw := strings.TrimSpace(query(c, name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return def
	}
	return n
}
