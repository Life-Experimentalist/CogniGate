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
	UpdateTenant(ctx context.Context, id string, patch TenantPatch) (*Tenant, error)
	DeleteTenant(ctx context.Context, id string) error

	// CreateAPIKey returns the record and the plaintext credential. The
	// plaintext is the only copy: the store keeps a hash.
	CreateAPIKey(ctx context.Context, tenantID string, plane Plane, name, scope string, expiresAt *time.Time) (*APIKey, string, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]*APIKey, error)
	RevokeAPIKey(ctx context.Context, tenantID, keyID string) error

	CreateProvider(ctx context.Context, p *Provider) (*Provider, error)
	ListProviders(ctx context.Context, tenantID string) ([]*Provider, error)
	GetProvider(ctx context.Context, tenantID, id string) (*Provider, error)
	UpdateProvider(ctx context.Context, tenantID, id string, patch ProviderPatch) (*Provider, error)
	DeleteProvider(ctx context.Context, tenantID, id string) error

	UpsertAlias(ctx context.Context, a *Alias) (*Alias, error)
	ListAliases(ctx context.Context, tenantID string) ([]*Alias, error)
	DeleteAlias(ctx context.Context, tenantID, name string) error

	UpsertRoute(ctx context.Context, r *Route) (*Route, error)
	ListRoutes(ctx context.Context, tenantID string) ([]*Route, error)
	DeleteRoute(ctx context.Context, tenantID, id string) error

	// The quota methods take a key id alongside the tenant. An empty key id
	// addresses the tenant's own quota; a non-empty one addresses the quota
	// that further constrains that single key.
	SetQuota(ctx context.Context, q *Quota) (*Quota, error)
	GetQuota(ctx context.Context, tenantID, keyID string) (*Quota, error)
	DeleteQuota(ctx context.Context, tenantID, keyID string) error

	CreateWebhook(ctx context.Context, w *Webhook) (*Webhook, error)
	ListWebhooks(ctx context.Context, tenantID string) ([]*Webhook, error)
	DeleteWebhook(ctx context.Context, tenantID, id string) error

	// RecordUsage is called on the metering path, off the request's critical
	// path. It must not block: a slow store degrades the data plane.
	RecordUsage(ctx context.Context, rec *UsageRecord) error
	Usage(ctx context.Context, tenantID string, since, until time.Time) (UsageTotals, error)
	// KeyUsage is Usage narrowed to the records one key produced, for
	// evaluating a key-level quota. Keys are attributed by prefix because that
	// is what a usage record carries: the key material never reaches the store.
	KeyUsage(ctx context.Context, tenantID, keyPrefix string, since, until time.Time) (UsageTotals, error)
	UsageBreakdown(ctx context.Context, tenantID string, since, until time.Time, groupBy string) ([]UsageBucket, error)

	// RecordAudit appends one entry to the admin audit log. The log is
	// append-only by contract: there is deliberately no update or delete, since
	// a log an administrator can edit cannot answer the question it exists to
	// answer.
	RecordAudit(ctx context.Context, e *AuditEntry) error
	// ListAudit returns entries newest first. It takes no filter: the log is
	// root-scope-only, small, and read by a human looking for what changed.
	ListAudit(ctx context.Context) ([]*AuditEntry, error)

	// Ping reports whether the backing store is reachable, for GET /v1/health.
	Ping(ctx context.Context) error

	// Kind names the implementation for /v1/meta and for logs, so an operator
	// can tell a dev-mode process from a durable one at a glance.
	Kind() string
}
