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
	"github.com/cognigate/gateway/internal/config"
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
		status := c.Response().StatusCode()
		out := httpx.GetOutcome(c)

		if s.Metrics != nil {
			s.Metrics.Requests.
				WithLabelValues(tenant, out.Provider, out.Model, route, strconv.Itoa(status)).
				Inc()
			s.Metrics.RequestDuration.
				WithLabelValues(tenant, out.Provider, route).
				Observe(elapsed.Seconds())
		}

		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		// One line per request, carrying GW-8's full field list. Every field is
		// gateway metadata: the route, the identities, the model that was
		// routed to, and the counts. Nothing here is derived from the request or
		// response body — not a parameter, and certainly not a message — which
		// is the distinction GW-14 draws. A model id is a routing decision, and
		// an operator who cannot see which model served cannot answer the first
		// question anyone asks about a slow or failed request.
		s.Logger.Log(c.UserContext(), level, "request",
			slog.String("request_id", httpx.RequestID(c)),
			slog.String("client_request_id", httpx.ClientRequestID(c)),
			slog.String("tenant", tenant),
			slog.String("key_prefix", keyPrefix(c)),
			slog.String("method", c.Method()),
			slog.String("route", route),
			slog.Int("status", status),
			slog.String("error_code", out.ErrorCode),
			slog.String("provider", out.Provider),
			slog.String("model", out.Model),
			slog.String("alias", out.Alias),
			slog.Int("fallback_depth", out.FallbackDepth),
			slog.Int("prompt_tokens", out.PromptTokens),
			slog.Int("completion_tokens", out.CompletionTokens),
			slog.Int64("upstream_duration_ms", out.UpstreamMS),
			// Empty unless the deployment enabled GW-12 and the caller opted
			// in, which is the same silence the response header keeps.
			slog.String("cache", out.CacheStatus),
			slog.Int64("duration_ms", elapsed.Milliseconds()))

		return err
	}
}

// refuseWhenDraining answers a request that arrives after shutdown began with
// 503 and Connection: close (GW-11).
//
// Fiber stops accepting new connections the moment Shutdown is called, so the
// requests this catches are the ones arriving on connections that were already
// open — a client reusing a keep-alive socket, which has no way to know the
// process on the other end is going away. Connection: close is the half that
// makes the answer useful: without it the client keeps the socket and sends the
// next request down a connection that is about to be closed underneath it.
//
// In-flight requests never reach here. They are past this middleware before
// draining is set, and GW-11 requires them — open SSE streams included — to run
// to completion.
func (s *Server) refuseWhenDraining() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !s.draining.Load() {
			return c.Next()
		}
		// Two exemptions, both for endpoints whose job is to describe the drain
		// rather than to be refused by it. /healthz already answers 503 with
		// {"status":"draining"}, which is the signal GW-11 requires a load
		// balancer to steer on, and refusing it here would replace that with a
		// generic envelope. /metrics is how an operator watches the drain
		// happen; a gateway that stops reporting the moment it starts shutting
		// down is dark for exactly the interval worth observing.
		switch c.Path() {
		case "/healthz", metricsPath(s.Config.Metrics.Path):
			return c.Next()
		}
		c.Set(fiber.HeaderConnection, "close")
		c.Context().SetConnectionClose()
		return httpx.Fail(c, apierr.Unavailable("The gateway is shutting down and is not accepting new requests."))
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
		//
		// It is matched on both planes, not only the one it is good for. An
		// operator picks its value, so it carries no prefix for PlaneOf to
		// read, and a bootstrap key sent to the data plane would otherwise be
		// answered as an invalid credential — the flat rejection this whole
		// function exists to avoid, aimed at the one key whose holder is most
		// likely to be an operator midway through setting the deployment up.
		if s.bootstrapMatches(raw) {
			if want != store.PlaneAdmin {
				return httpx.Fail(c, apierr.WrongPlane(string(want)))
			}
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
	// Configuration validation rejects a short-but-non-empty value at startup, so
	// in a running process this only ever catches the empty case. It stays here
	// anyway: the check that a credential is long enough belongs beside the
	// comparison, not only in the code path that happened to run first.
	if len(want) < config.MinBootstrapKeyLen {
		return false
	}
	return store.ConstantTimeEqual(raw, want)
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
	return context.WithTimeout(c.UserContext(), s.limits(c).RequestTimeout)
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

// --- concurrency limiter ----------------------------------------------------

// concurrencyLimiter counts in-flight requests per credential.
//
// It is a counter map rather than a semaphore pool because the limit is a
// rejection threshold, not a queue: GW-13 requires 429 concurrency_exceeded,
// and a caller who wanted to wait would rather do so with their own backoff
// than inside an opaque gateway queue.
//
// limit is the deployment ceiling, used when a caller passes none. The limit is
// per acquire rather than fixed at construction because GW-13 lets a tenant be
// held to a lower one, and the counters are shared across every tenant.
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

func (l *concurrencyLimiter) acquire(key string, limit int) (func(), bool) {
	if limit < 1 {
		limit = l.limit
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.inUse[key] >= limit {
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
