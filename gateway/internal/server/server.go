// Package server is the HTTP surface: both planes, every middleware, and the
// lifecycle around them.
//
// One listener serves both /v1 (data) and /admin/v1 (admin). They are separated
// by credential family rather than by port because the separation that matters
// is which key opens which door — a second port would suggest a network
// boundary the deployment does not actually provide.
package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/obs"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// bodyLimitSlack is how far fasthttp's own body limit sits above the configured
// max_request_bytes. It has to be large enough that a request just over the
// limit is still read to completion — otherwise limitBody never sees it and the
// caller gets a transport error instead of an answer — and small enough that a
// genuinely huge body is refused before it is buffered.
const bodyLimitSlack = 64 * 1024

// Emitter publishes an event from the GW-4/GW-1/GW-3 registry.
//
// It is an interface rather than a concrete type so the HTTP layer never waits
// on delivery: the implementation queues, signs and retries on its own
// goroutines, and Emit returns immediately.
type Emitter interface {
	Emit(ctx context.Context, tenantID, eventType string, data map[string]any)
}

// Deps is everything the HTTP layer needs, constructed by main. Passing them in
// rather than building them here is what lets the tests assemble a server over
// an in-memory store and a fake provider without touching the network.
type Deps struct {
	Config     config.Config
	Store      store.Store
	Catalog    *catalog.Catalog
	Resolver   *routing.Resolver
	Dispatcher *routing.Dispatcher
	Metrics    *obs.Metrics
	Telemetry  *obs.Telemetry
	// Events delivers the GW-4/GW-1/GW-3 event registry to a tenant's webhooks.
	// Nil disables emission entirely, which is a convenience for tests that do
	// not assert on it. Every real process wires it, --dev included: a dev
	// process that accepted a webhook registration on the admin plane and then
	// silently never delivered would make the admin API lie about what it had
	// just stored.
	Events  Emitter
	Logger  *slog.Logger
	Version string
	// Dev marks a `cognigate --dev` process. It only changes what /v1/meta
	// reports; the routes and their semantics are identical, which is the whole
	// point of GW-11's dev mode.
	Dev bool
}

// Server owns the Fiber app and the state that outlives a single request.
type Server struct {
	Deps

	app     *fiber.App
	limiter *concurrencyLimiter
	health  *healthCache
	quotas  *quotaCache

	// The Prometheus adaptor is built once and reused: constructing it per
	// scrape allocates a handler chain for no benefit.
	metricsOnce    sync.Once
	metricsHandler fasthttp.RequestHandler

	// draining flips at the start of a graceful shutdown. /healthz fails from
	// that moment so a load balancer stops sending new work, while requests
	// already in flight — including open SSE streams — run to completion.
	draining atomic.Bool
}

// New assembles the app. It never dials anything: a Server is usable the moment
// it returns.
func New(d Deps) *Server {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}

	s := &Server{
		Deps:    d,
		limiter: newConcurrencyLimiter(d.Config.Limits.MaxConcurrentPerKey),
		health:  &healthCache{ttl: d.Config.Health.CacheTTL},
		quotas:  newQuotaCache(quotaCacheTTL),
	}

	s.app = fiber.New(fiber.Config{
		DisableStartupMessage: true,
		// Fiber's own failures — a body past BodyLimit, a malformed request line
		// — arrive here. Routing them through httpx.Fail is what keeps GW-7's
		// envelope universal rather than "universal except for the errors Fiber
		// raises before a handler runs".
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return httpx.Fail(c, translateFiberError(err, d.Config.Limits))
		},
		// Deliberately above max_request_bytes: limitBody enforces the configured
		// limit as a GW-7 envelope, and this is only the wire-level backstop for a
		// body too large to be worth buffering at all.
		BodyLimit:    int(d.Config.Limits.MaxRequestBytes) + bodyLimitSlack,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // a stream may legitimately write for minutes
		IdleTimeout:  75 * time.Second,
		ServerHeader: "CogniGate",
		AppName:      "CogniGate",
		// Case-sensitive routing so /V1/Chat/Completions is a 404 rather than a
		// quietly-accepted second spelling of the documented path.
		CaseSensitive: true,
	})

	s.routes()
	return s
}

// App exposes the Fiber app so tests can drive it with app.Test and so main can
// register nothing further.
func (s *Server) App() *fiber.App { return s.app }

func (s *Server) routes() {
	// Recovery and request identity come first and in this order: the request id
	// must already be on the response when a panic unwinds, or the one thing a
	// user can quote about the failure is missing from it.
	s.app.Use(s.identify())
	s.app.Use(s.recover())
	s.app.Use(s.observe())
	s.app.Use(s.limitBody())

	s.app.Get("/healthz", s.handleHealthz)
	if s.Config.Metrics.Enabled {
		s.app.Get(metricsPath(s.Config.Metrics.Path), s.metricsAuth(), s.handleMetrics)
	}

	v1 := s.app.Group("/v1", s.auth(store.PlaneData), s.limitConcurrency())
	v1.Post("/chat/completions", s.handleChatCompletions)
	v1.Get("/models", s.handleListModels)
	v1.Get("/models/*", s.handleGetModel)
	v1.Get("/usage", s.handleUsage)
	v1.Get("/usage/breakdown", s.handleUsageBreakdown)
	v1.Get("/health", s.handleHealth)
	v1.Get("/meta", s.handleMeta)

	s.adminRoutes(s.app.Group("/admin/v1", s.auth(store.PlaneAdmin)))

	// Anything unmatched is an explicit 404 with code not_supported. GW-9 forbids
	// passing an unknown OpenAI path through to a provider: a gateway whose
	// surface is "whatever the upstream happens to implement" cannot be
	// documented, versioned, or conformance-tested.
	s.app.Use(func(c *fiber.Ctx) error {
		return httpx.Fail(c, apierr.NotSupported(c.Method()+" "+c.Path()))
	})
}

// Listen serves until Shutdown is called.
func (s *Server) Listen(addr string) error { return s.app.Listen(addr) }

// Shutdown drains gracefully: /healthz starts failing immediately so traffic is
// steered away, then in-flight requests are given drain_timeout to finish.
//
// GW-11 requires open SSE streams to survive this. They do, because Fiber waits
// on the connection rather than cancelling the handler — a caller mid-completion
// is not disconnected just because the process was asked to stop.
func (s *Server) Shutdown(ctx context.Context) error {
	s.draining.Store(true)

	timeout := s.Config.Shutdown.DrainTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		timeout = time.Second
	}

	s.Logger.Info("draining", slog.Duration("timeout", timeout))
	err := s.app.ShutdownWithTimeout(timeout)
	if s.Telemetry != nil {
		s.Telemetry.Close()
	}
	return err
}

// Draining reports whether a shutdown is under way.
func (s *Server) Draining() bool { return s.draining.Load() }

// --- infrastructure endpoints ----------------------------------------------

// handleHealthz is the liveness probe. It is deliberately unauthenticated and
// dependency-free: a probe that checks the database turns a database blip into a
// rolling restart of every healthy gateway.
func (s *Server) handleHealthz(c *fiber.Ctx) error {
	if s.draining.Load() {
		return c.Status(fiber.StatusServiceUnavailable).
			JSON(fiber.Map{"status": "draining"})
	}
	return c.JSON(fiber.Map{"status": "ok", "version": s.version()})
}

func (s *Server) handleMetrics(c *fiber.Ctx) error {
	s.metricsOnce.Do(func() {
		h := promhttp.HandlerFor(s.Metrics.Registry(), promhttp.HandlerOpts{})
		s.metricsHandler = fasthttpadaptor.NewFastHTTPHandler(h)
	})
	s.metricsHandler(c.Context())
	return nil
}

// metricsAuth guards /metrics only when a token is configured. Unauthenticated
// by default is the right default for a scrape endpoint on a private network,
// and the series carry no request content for the same reason no label does.
func (s *Server) metricsAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		want := s.Config.Metrics.Token
		if want == "" {
			return c.Next()
		}
		if !store.ConstantTimeEqual(bearer(c), want) {
			return httpx.Fail(c, apierr.InvalidAPIKey())
		}
		return c.Next()
	}
}

func (s *Server) version() string {
	if s.Version == "" {
		return "dev"
	}
	return s.Version
}

// metricsPath normalises the configured path so a value without a leading slash
// does not silently register an unreachable route.
func metricsPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/metrics"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// translateFiberError maps the framework's own failures onto the GW-7 registry.
//
// Without this, a body over BodyLimit would leave the gateway as fasthttp's
// plain-text "body size exceeds the given limit" — a response no OpenAI client
// can parse, from an endpoint that promises it always can.
func translateFiberError(err error, limits config.Limits) error {
	var already *apierr.Error
	if errors.As(err, &already) {
		return err
	}

	var fe *fiber.Error
	if !errors.As(err, &fe) {
		return err
	}
	switch fe.Code {
	case fiber.StatusRequestEntityTooLarge:
		return apierr.RequestTooLarge(limits.MaxRequestBytes).WithCause(err)
	case fiber.StatusNotFound, fiber.StatusMethodNotAllowed:
		return apierr.NotSupported("the requested route").WithCause(err)
	case fiber.StatusRequestTimeout:
		return apierr.GatewayTimeout(limits.RequestTimeout.Seconds()).WithCause(err)
	default:
		return err
	}
}
