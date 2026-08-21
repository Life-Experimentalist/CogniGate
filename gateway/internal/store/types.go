package store

import "time"

// Plane distinguishes the two credential families. A key is minted for exactly
// one plane and is never valid on the other — that separation is what lets an
// application key be handed to a service without also handing over the ability
// to mint more keys.
type Plane string

const (
	PlaneData  Plane = "data"  // cg-…  → /v1/*
	PlaneAdmin Plane = "admin" // cga-… → /admin/v1/*
)

// Key prefixes. The prefix is part of the credential, not decoration: it is how
// the gateway answers "wrong plane" without a store lookup, and how secret
// scanners recognise a leaked CogniGate key in a public repository.
const (
	DataKeyPrefix  = "cg-"
	AdminKeyPrefix = "cga-"
)

// ScopeRoot is the admin scope that reaches every tenant. Anything else is
// "tenant:<id>" and reaches only that one.
const ScopeRoot = "root"

// Tenant is the unit of isolation: keys, providers, aliases, routes, quotas and
// usage all hang off exactly one tenant, and nothing crosses between them.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // active | suspended
	CreatedAt time.Time `json:"created_at"`
	// Limits narrows the deployment's ceilings for this tenant (GW-13). A zero
	// field means "whatever the deployment says", which is why this is a value
	// rather than a pointer: a tenant with no overrides and a tenant whose
	// overrides are all unset are the same tenant, and giving them two
	// representations would only invite code that handles one of them.
	Limits TenantLimits `json:"limits"`
	// Cache is this tenant's GW-12 response-cache policy, a value for the same
	// reason Limits is.
	Cache TenantCache `json:"cache"`
	// DebugCapture is this tenant's GW-14 capture policy. Off is the zero
	// value, which is the only default the specification permits.
	DebugCapture TenantDebugCapture `json:"debug_capture"`
}

// TenantDebugCapture is GW-14's one exception to the content ban: while it is
// enabled, a sampled fraction of this tenant's requests have their request and
// response bodies retained, readable through the admin plane alone, until they
// are hard-deleted at TTLSeconds.
//
// It is per tenant and nothing else. There is deliberately no deployment-wide
// switch: turning retention on has to name whose content is being retained, and
// it has to be an admin action someone can find in the audit log afterwards.
//
// A zero TTLSeconds means the deployment's default, not zero seconds, the same
// convention TenantCache uses. A zero SampleRate is likewise the deployment's
// default rather than "capture nothing" — Enabled is the switch, and a policy
// that was enabled but silently captured nothing would be the worst of both.
type TenantDebugCapture struct {
	Enabled    bool    `json:"enabled,omitempty"`
	TTLSeconds int     `json:"ttl_seconds,omitempty"`
	SampleRate float64 `json:"sample_rate,omitempty"`
}

// TenantCache is the per-tenant half of GW-12's caching policy.
//
// Enabled opts every eligible request into the cache without the caller having
// to send X-CogniGate-Cache: prefer, which is what lets an operator turn
// caching on for a workload whose client they do not control. TTLSeconds
// narrows the deployment's default; the admin API rejects a value above
// cache.max_ttl, so a tenant cannot hold an answer longer than the deployment
// is willing to.
//
// A zero TTLSeconds means the deployment's default, not zero seconds.
type TenantCache struct {
	Enabled    bool `json:"enabled,omitempty"`
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
}

// TenantLimits are the per-tenant halves of GW-13's limit table. Each is a
// ceiling the operator may lower for one tenant, never raise: the admin API
// rejects a value above the deployment's, so a tenant cannot be configured into
// consuming more of the process than the process was sized for.
//
// Only the four limits that can be decided per request are here.
// max_response_bytes and upstream_connect_timeout are deliberately absent: both
// are baked into the provider adapter's HTTP transport when the process starts,
// so honouring them per tenant would mean a transport per tenant — a real cost
// in connection pools for a knob no acceptance criterion asks for.
type TenantLimits struct {
	MaxRequestBytes          int64 `json:"max_request_bytes,omitempty"`
	RequestTimeoutSeconds    int   `json:"request_timeout_seconds,omitempty"`
	StreamIdleTimeoutSeconds int   `json:"stream_idle_timeout_seconds,omitempty"`
	MaxConcurrentPerKey      int   `json:"max_concurrent_per_key,omitempty"`
	RequestsPerSecond        int   `json:"requests_per_second,omitempty"`
	BurstCapacity            int   `json:"burst_capacity,omitempty"`
}

// TenantPatch is a partial update. Every field is a pointer so that "absent"
// and "set to the zero value" stay distinguishable — without that, a PATCH
// carrying only a status change would silently blank the name.
//
// Limits replaces the whole block rather than merging field by field: a
// half-applied limit set is a configuration nobody asked for, and "send me the
// limits you want this tenant to have" is the only rule an operator has to
// remember. An empty object therefore clears every override.
type TenantPatch struct {
	Name         *string
	Status       *string
	Limits       *TenantLimits
	Cache        *TenantCache
	DebugCapture *TenantDebugCapture
}

// APIKey stores only a hash. The plaintext is returned once, at creation, and
// is then unrecoverable — a stolen database yields no working credential.
type APIKey struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	Plane     Plane      `json:"plane"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"` // display + usage-record attribution
	Hash      string     `json:"-"`
	Scope     string     `json:"scope,omitempty"` // admin keys only
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Active reports whether the key may authenticate right now.
func (k *APIKey) Active(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && now.After(*k.ExpiresAt) {
		return false
	}
	return true
}

// Provider is one upstream account. Keys is a pool: GW-3 rotates within the
// pool on a 429 before it gives up on the provider and cascades onward.
type Provider struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenant_id"`
	Name     string   `json:"name"` // openai, anthropic, … — the routing identifier
	Kind     string   `json:"kind"` // adapter to use; "openai" covers every compatible API
	BaseURL  string   `json:"base_url"`
	Enabled  bool     `json:"enabled"`
	Keys     []string `json:"-"` // plaintext only in memory, never serialised outward
	// KeyPrefixes mirrors Keys for display, so the admin API can show which
	// credentials are registered without ever returning one.
	KeyPrefixes []string  `json:"key_prefixes"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProviderPatch is a partial update, with the same pointer convention as
// TenantPatch. Keys is a slice rather than a pointer because a nil slice
// already means "absent" and an empty one is refused by the handler: a provider
// with no credentials could never serve a request, so accepting that write
// would only produce a provider that fails at dispatch time instead of at the
// moment someone made the mistake.
type ProviderPatch struct {
	BaseURL *string
	Enabled *bool
	Keys    []string
}

// AuditEntry is one line of the append-only admin log GW-6 requires.
//
// It records who did what to which resource and how it ended — never the
// request body. An admin write can carry provider credentials, and a log that
// captured them would hand an auditor a second copy of every secret the key
// vault exists to protect. GW-14 applies here like anywhere else.
type AuditEntry struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	// Actor is the credential's display prefix, not its id: the prefix is what
	// an operator sees when listing keys, so it is what makes the log legible.
	Actor      string `json:"actor"`
	ActorKeyID string `json:"actor_key_id"`
	ActorScope string `json:"actor_scope"`
	Action     string `json:"action"` // create | update | upsert | delete
	Resource   string `json:"resource"`
	TenantID   string `json:"tenant_id,omitempty"`
	// Status is the HTTP status the attempt produced. Refused writes are logged
	// too: an attempt to reach another tenant is exactly what this log is read
	// to find, and one that succeeded is not more interesting than one that did
	// not.
	Status    int    `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

// Event is one notification the gateway raised, as it was published.
//
// It is stored as well as delivered because GW-8 makes the two independent: a
// tenant with no webhook registered, or one whose endpoint was down for the
// five attempts a delivery gets, still has to be able to find out that its
// breaker opened. Polling is the floor under at-least-once delivery, not an
// alternative to it.
//
// Data is the same payload the webhook body carries, and is bound by the same
// rule: it holds gateway facts — a model id, a provider name, a quota window —
// and never request or response content (GW-14).
//
// The field tags match the webhook envelope's exactly, `data` included with no
// omitempty. A reader comparing what it polled against what it was delivered is
// the ordinary use of this endpoint, and a key that is present in one shape and
// absent in the other makes that comparison harder for no gain.
type Event struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Created  time.Time      `json:"created"`
	TenantID string         `json:"tenant"`
	Data     map[string]any `json:"data"`
}

// Model is one catalog entry, as discovered from a provider (GW-1).
type Model struct {
	ID                string   `json:"id"`
	Provider          string   `json:"provider"`
	ContextWindow     int      `json:"context_window,omitempty"`
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	InputCostPerMTok  float64  `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok float64  `json:"output_cost_per_mtok,omitempty"`
	Deprecated        bool     `json:"deprecated,omitempty"`
}

// Alias is a stable name a caller may use in place of a real model id (GW-2).
// A pin wins outright; otherwise the constraint fields select the best current
// catalog entry, so "fast" keeps meaning "fast" as providers ship new models.
type Alias struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	Name               string    `json:"name"`
	Pin                string    `json:"pin,omitempty"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	MinContextWindow   int       `json:"min_context_window,omitempty"`
	ProviderPreference []string  `json:"provider_preference,omitempty"`
	CostTier           string    `json:"cost_tier,omitempty"` // cheapest | balanced | best
	CreatedAt          time.Time `json:"created_at"`
}

// Route is an ordered fallback chain (GW-3). Match is the model or alias the
// caller asks for; Chain is tried left to right. A single-element chain is a
// pin with no fallback, which is a legitimate configuration.
type Route struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Match     string    `json:"match"`
	Chain     []string  `json:"chain"`
	CreatedAt time.Time `json:"created_at"`
}

// QuotaLimit is one cap slot: a hard ceiling, and the percentage of it at which
// the holder is warned before reaching it.
type QuotaLimit struct {
	Cap              float64 `json:"cap"`
	SoftThresholdPct int     `json:"soft_threshold_pct"`
}

// QuotaWindow carries the two unit limits for one window. Either may be absent,
// so a tenant can be capped on spend without also being capped on tokens.
type QuotaWindow struct {
	Tokens *QuotaLimit `json:"tokens,omitempty"`
	Cost   *QuotaLimit `json:"cost,omitempty"`
}

// Quota bounds consumption over two windows in two units (GW-4). The four slots
// are independent and any of them may be absent, which means unlimited for that
// slot — a pointer rather than a zero value, because a cap of zero is a
// meaningful configuration and "unset" has to be distinguishable from it.
//
// A quota with a KeyID constrains one key rather than the tenant. It can only
// narrow what the tenant's own quota already allows: both are evaluated, and a
// request is rejected if either says so, so a key-level cap can never raise a
// tenant past its own ceiling.
type Quota struct {
	TenantID  string      `json:"tenant_id"`
	KeyID     string      `json:"key_id,omitempty"`
	Day       QuotaWindow `json:"day"`
	Month     QuotaWindow `json:"month"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// Empty reports whether a quota constrains nothing, which is indistinguishable
// from having no quota at all and is treated as such rather than as a cap of
// zero.
func (q *Quota) Empty() bool {
	return q.Day.Tokens == nil && q.Day.Cost == nil &&
		q.Month.Tokens == nil && q.Month.Cost == nil
}

// Webhook is one delivery target for the GW-4/GW-1/GW-3 event registry.
type Webhook struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	URL       string    `json:"url"`
	Secret    string    `json:"-"` // HMAC key; never returned by the admin API
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// UsageRecord is one metered request (GW-8). It carries no prompt or completion
// content — GW-14 forbids that in any durable store — only the dimensions
// billing and debugging need.
type UsageRecord struct {
	RequestID       string    `json:"request_id"`
	ClientRequestID string    `json:"client_request_id,omitempty"`
	TenantID        string    `json:"tenant_id"`
	KeyPrefix       string    `json:"key_prefix"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	RequestedModel  string    `json:"requested_model"`
	FallbackDepth   int       `json:"fallback_depth"`
	PromptTokens    int       `json:"prompt_tokens"`
	CompletionToken int       `json:"completion_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	CostUSD         float64   `json:"cost_usd"`
	Cached          bool      `json:"cached"`
	Streamed        bool      `json:"streamed"`
	StatusCode      int       `json:"status_code"`
	DurationMS      int64     `json:"duration_ms"`
	RecordedAt      time.Time `json:"recorded_at"`
}

// UsageTotals is the aggregate behind GET /v1/usage.
type UsageTotals struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// UsageBucket is one row of GET /v1/usage/breakdown.
type UsageBucket struct {
	Key string `json:"key"`
	UsageTotals
}
