package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/events"
	"github.com/cognigate/gateway/internal/obs"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/server"
	"github.com/cognigate/gateway/internal/store"
)

// app is the assembled process: every long-lived component, wired together and
// ready to serve.
//
// Assembly lives here rather than in main so that it is testable. Everything
// interesting about a composition root is whether the pieces were connected to
// each other correctly, and that cannot be asserted on a function whose only
// exit is os.Exit.
type app struct {
	cfg       config.Config
	logger    *slog.Logger
	server    *server.Server
	store     store.Store
	events    *events.Dispatcher
	telemetry *obs.Telemetry

	// dev holds the credentials minted for `--dev`, for main to print. Nil in
	// every other mode: a production process mints nothing at boot.
	dev *devCredentials
}

// devCredentials are the throwaway keys a `--dev` process starts with, so the
// first request needs no admin call to set up (GW-11).
type devCredentials struct {
	TenantID string
	DataKey  string
	AdminKey string
}

// build assembles the process from configuration.
//
// The order below is the dependency order and cannot be shuffled: the catalog
// needs the store and the provider registry, the resolver needs the catalog,
// the dispatcher needs the resolver and the breaker, and the two event hooks
// need the dispatcher that delivers them.
func build(cfg config.Config, dev bool, logger *slog.Logger, version string) (*app, error) {
	a := &app{cfg: cfg, logger: logger}

	// GW-11: TLS is off unless a keypair is configured. Loading it here rather
	// than letting the listener open the files means an unreadable or malformed
	// certificate stops the process with a message naming it, instead of
	// surfacing from a goroutine after startup has already been announced.
	var cert *tls.Certificate
	if cfg.Gateway.TLSEnabled() {
		loaded, err := tls.LoadX509KeyPair(cfg.Gateway.TLSCertFile, cfg.Gateway.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading the TLS keypair from %s and %s: %w",
				cfg.Gateway.TLSCertFile, cfg.Gateway.TLSKeyFile, err)
		}
		cert = &loaded
	}

	// GW-11: with no analytics service configured the gateway runs entirely on
	// its in-memory store. That is what makes `--dev` a single process, and it
	// is also the honest behaviour for a misconfigured deployment — serving
	// from memory and saying so in /v1/health beats refusing to start.
	if cfg.Analytics.BaseURL != "" {
		return nil, fmt.Errorf(
			"analytics.base_url is set to %q but no durable store is implemented yet; "+
				"leave it empty to run on the in-memory store", cfg.Analytics.BaseURL)
	}
	mem := store.NewMemory(dev)
	a.store = mem

	metrics := obs.NewMetrics()
	a.telemetry = obs.NewTelemetry(mem, cfg.Telemetry.Buffer, logger, metrics)

	// Events are delivered in every mode, dev included. A dev process that
	// accepted webhook registrations on the admin plane and then silently never
	// delivered would make the admin API lie about what it had configured.
	a.events = events.New(mem, events.Options{
		MaxAttempts: cfg.Webhooks.MaxAttempts,
		Timeout:     cfg.Webhooks.Timeout,
		Logger:      logger,
	})

	registry := provider.NewRegistry(
		provider.NewOpenAI(cfg.Limits.UpstreamConnectTimeout, cfg.Limits.MaxResponseBytes),
	)

	cat := catalog.New(mem, registry, catalog.Options{
		TTL:             cfg.Catalog.TTL,
		StaleWarnAfter:  cfg.Catalog.StaleWarnAfter,
		ProviderTimeout: cfg.Catalog.ProviderTimeout,
		OnChange:        events.CatalogHook(a.events),
	})

	// GW-8's catalog age is answered at scrape time rather than written at
	// refresh. A gauge a refresh sets would be set to zero, and zero is what it
	// would then report for as long as refreshes kept failing — the exact
	// condition the series exists to reveal.
	metrics.SetCatalogAgeSource(func() []obs.CatalogAgeSample {
		var out []obs.CatalogAgeSample
		for _, age := range cat.Ages() {
			out = append(out, obs.CatalogAgeSample{
				Tenant:   age.TenantID,
				Provider: age.Provider,
				Seconds:  age.Age.Seconds(),
			})
		}
		return out
	})

	resolver := routing.NewResolver(mem, cat, cfg.Routing.MaxFallbackDepth)
	breaker := routing.NewBreaker(
		cfg.Routing.Breaker.ErrorThreshold,
		cfg.Routing.Breaker.Window,
		cfg.Routing.Breaker.OpenDuration,
		breakerObservers(metrics, events.BreakerHook(a.events)),
	)

	a.server = server.New(server.Deps{
		Config:         cfg,
		Store:          mem,
		Catalog:        cat,
		Resolver:       resolver,
		Dispatcher:     routing.NewDispatcher(resolver, breaker, registry, mem),
		Metrics:        metrics,
		Telemetry:      a.telemetry,
		Events:         a.events,
		Logger:         logger,
		Version:        version,
		Dev:            dev,
		TLSCertificate: cert,
	})

	if dev {
		creds, err := seedDev(mem)
		if err != nil {
			a.Close()
			return nil, err
		}
		a.dev = creds
	}

	return a, nil
}

// seedDev creates the tenant and the two credentials a dev process starts with.
//
// Both planes are minted because GW-11 asks for a dev server that exercises the
// whole product, not just the data plane: the admin key is how a developer
// registers the provider that the data key then routes to.
func seedDev(mem *store.Memory) (*devCredentials, error) {
	ctx := context.Background()

	tenant, err := mem.CreateTenant(ctx, "dev")
	if err != nil {
		return nil, fmt.Errorf("seeding the dev tenant: %w", err)
	}

	_, dataKey, err := mem.CreateAPIKey(ctx, tenant.ID, store.PlaneData, "dev", "", nil)
	if err != nil {
		return nil, fmt.Errorf("minting the dev data key: %w", err)
	}
	// Scoped to this tenant rather than root: a dev key that could reach every
	// tenant would be a different credential from the one a deployment issues,
	// and dev mode is only useful if what it exercises is the real thing.
	_, adminKey, err := mem.CreateAPIKey(ctx, tenant.ID, store.PlaneAdmin, "dev", "tenant:"+tenant.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("minting the dev admin key: %w", err)
	}

	return &devCredentials{TenantID: tenant.ID, DataKey: dataKey, AdminKey: adminKey}, nil
}

// breakerObservers fans one breaker transition out to both things that care
// about it: the gauge GW-8 exports and the webhook GW-5 promises.
//
// They are combined here rather than the breaker growing a second callback,
// because from the breaker's point of view there is one event and any number of
// listeners. Both run while the breaker's lock is held, so neither may block —
// the metric write does not, and the event hook already spawns its own
// goroutine for exactly this reason.
func breakerObservers(metrics *obs.Metrics, hook func(key string, from, to routing.State)) func(string, routing.State, routing.State) {
	return func(key string, from, to routing.State) {
		if metrics != nil {
			tenantID, providerName, model := routing.SplitKey(key)
			metrics.BreakerState.
				WithLabelValues(tenantID, providerName, model).
				Set(to.Gauge())
		}
		if hook != nil {
			hook(key, from, to)
		}
	}
}

// Close releases what build acquired, in reverse order. It is safe to call on a
// partially built app, which is what makes it usable as build's own error path.
func (a *app) Close() {
	if a.events != nil {
		a.events.Close()
	}
	if a.telemetry != nil {
		a.telemetry.Close()
	}
}

// Shutdown drains the server and then stops the background workers.
//
// The order matters: the server drains first so that requests still in flight
// can record their usage and raise their events, and only then are the
// telemetry and webhook queues closed. Closing them first would silently
// discard the accounting for every request that was still running.
func (a *app) Shutdown(ctx context.Context) error {
	err := a.server.Shutdown(ctx)
	if a.events != nil {
		a.events.Close()
	}
	return err
}
