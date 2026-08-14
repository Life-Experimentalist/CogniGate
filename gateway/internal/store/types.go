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
