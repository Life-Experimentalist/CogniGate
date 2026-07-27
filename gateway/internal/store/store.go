package store

import (
	"context"
	"errors"
	"time"
)

// Sentinels. Handlers translate these into the GW-7 registry rather than each
// implementation inventing its own HTTP semantics.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Store is the gateway's whole persistence surface.
//
// The gateway owns the public /admin/v1 routes on both deployments; this
// interface is what changes underneath. In a compose deployment it is backed by
// the analytics service, which owns Postgres, the key vault and webhook
// delivery. Under `cognigate --dev` it is backed by Memory, which is why the
// dev binary needs no Redis and no database while still serving the full admin
// plane (GW-11).
type Store interface {
	// ResolveKey authenticates a plaintext credential. It returns ErrNotFound
	// for unknown, revoked and expired keys alike: distinguishing them tells a
	// caller holding a stolen key which of those it is.
	ResolveKey(ctx context.Context, plaintext string) (*APIKey, *Tenant, error)

	CreateTenant(ctx context.Context, name string) (*Tenant, error)
	GetTenant(ctx context.Context, id string) (*Tenant, error)
	ListTenants(ctx context.Context) ([]*Tenant, error)
	DeleteTenant(ctx context.Context, id string) error

	// CreateAPIKey returns the record and the plaintext credential. The
	// plaintext is the only copy: the store keeps a hash.
	CreateAPIKey(ctx context.Context, tenantID string, plane Plane, name, scope string, expiresAt *time.Time) (*APIKey, string, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]*APIKey, error)
	RevokeAPIKey(ctx context.Context, tenantID, keyID string) error

	CreateProvider(ctx context.Context, p *Provider) (*Provider, error)
	ListProviders(ctx context.Context, tenantID string) ([]*Provider, error)
	GetProvider(ctx context.Context, tenantID, id string) (*Provider, error)
	DeleteProvider(ctx context.Context, tenantID, id string) error

	UpsertAlias(ctx context.Context, a *Alias) (*Alias, error)
	ListAliases(ctx context.Context, tenantID string) ([]*Alias, error)
	DeleteAlias(ctx context.Context, tenantID, name string) error

	UpsertRoute(ctx context.Context, r *Route) (*Route, error)
	ListRoutes(ctx context.Context, tenantID string) ([]*Route, error)
	DeleteRoute(ctx context.Context, tenantID, id string) error

	SetQuota(ctx context.Context, q *Quota) (*Quota, error)
	GetQuota(ctx context.Context, tenantID string) (*Quota, error)
	DeleteQuota(ctx context.Context, tenantID string) error

	CreateWebhook(ctx context.Context, w *Webhook) (*Webhook, error)
	ListWebhooks(ctx context.Context, tenantID string) ([]*Webhook, error)
	DeleteWebhook(ctx context.Context, tenantID, id string) error

	// RecordUsage is called on the metering path, off the request's critical
	// path. It must not block: a slow store degrades the data plane.
	RecordUsage(ctx context.Context, rec *UsageRecord) error
	Usage(ctx context.Context, tenantID string, since, until time.Time) (UsageTotals, error)
	UsageBreakdown(ctx context.Context, tenantID string, since, until time.Time, groupBy string) ([]UsageBucket, error)

	// Ping reports whether the backing store is reachable, for GET /v1/health.
	Ping(ctx context.Context) error

	// Kind names the implementation for /v1/meta and for logs, so an operator
	// can tell a dev-mode process from a durable one at a glance.
	Kind() string
}
