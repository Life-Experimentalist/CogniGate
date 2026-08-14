// Package httpx holds the conventions every CogniGate response obeys: the
// X-CogniGate-* header family, the request identifier that ties a response to
// its log line, and the single place errors become the GW-7 envelope.
package httpx

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/store"
)

// The extension header family. Every one is additive: a client that ignores all
// of them still sees an ordinary OpenAI-compatible response.
const (
	HeaderRequestID       = "X-CogniGate-Request-Id"
	HeaderServedBy        = "X-CogniGate-Served-By"
	HeaderFallbackDepth   = "X-CogniGate-Fallback-Depth"
	HeaderQuotaState      = "X-CogniGate-Quota-State"
	HeaderCache           = "X-CogniGate-Cache"
	HeaderDebugCapture    = "X-CogniGate-Debug-Capture"
	HeaderEventID         = "X-CogniGate-Event-Id"
	HeaderSignature       = "X-CogniGate-Signature"
	HeaderDeprecation     = "X-CogniGate-Deprecation"
	HeaderClientRequestID = "X-Client-Request-Id"
)

// MaxClientRequestID bounds the echoed correlation id. Without a bound, a
// caller could push arbitrary bytes into every log line and usage record.
const MaxClientRequestID = 128

// Quota states reported in HeaderQuotaState.
const (
	QuotaOK           = "ok"
	QuotaSoftExceeded = "soft-exceeded"
	QuotaHardExceeded = "hard-exceeded"
)

// Cache dispositions reported in HeaderCache.
const (
	CacheHit    = "hit"
	CacheMiss   = "miss"
	CacheBypass = "bypass"
)

// Fiber locals keys. Typed constants rather than bare strings so a typo is a
// compile error in the places that matter.
const (
	localRequestID       = "cg_request_id"
	localClientRequestID = "cg_client_request_id"
	localAPIKey          = "cg_api_key"
	localTenant          = "cg_tenant"
)

// NewRequestID mints the identifier that appears in the response header, the
// error body, the structured log line and the usage record — the one string a
// user can quote to have a request found.
func NewRequestID() string { return store.NewID(store.IDRequest) }

// SetRequestID records the identifier for this request and puts it on the
// response immediately, so it is present even if the handler later panics.
func SetRequestID(c *fiber.Ctx, id string) {
	c.Locals(localRequestID, id)
	c.Set(HeaderRequestID, id)
}

func RequestID(c *fiber.Ctx) string {
	id, _ := c.Locals(localRequestID).(string)
	return id
}

// SetClientRequestID stores the caller's own correlation id, truncated to
// MaxClientRequestID and stripped of control characters — it is echoed into
// logs, and an unsanitised value there is a log-injection vector.
func SetClientRequestID(c *fiber.Ctx, raw string) {
	v := sanitizeCorrelationID(raw)
	if v == "" {
		return
	}
	c.Locals(localClientRequestID, v)
	c.Set(HeaderClientRequestID, v)
}

func ClientRequestID(c *fiber.Ctx) string {
	id, _ := c.Locals(localClientRequestID).(string)
	return id
}

func sanitizeCorrelationID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxClientRequestID {
		raw = raw[:MaxClientRequestID]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
}

// SetAuth records the authenticated principal for downstream handlers.
func SetAuth(c *fiber.Ctx, key *store.APIKey, tenant *store.Tenant) {
	c.Locals(localAPIKey, key)
	c.Locals(localTenant, tenant)
}

// Key returns the authenticated credential, or nil on an unauthenticated route.
func Key(c *fiber.Ctx) *store.APIKey {
	k, _ := c.Locals(localAPIKey).(*store.APIKey)
	return k
}

// Tenant returns the authenticated tenant. It is nil for a root admin key,
// which belongs to no tenant.
func Tenant(c *fiber.Ctx) *store.Tenant {
	t, _ := c.Locals(localTenant).(*store.Tenant)
	return t
}

// TenantID is the convenience form for the common case.
func TenantID(c *fiber.Ctx) string {
	if t := Tenant(c); t != nil {
		return t.ID
	}
	return ""
}

// Fail renders any error as the GW-7 envelope. This is the only place in the
// gateway that writes an error body, which is what makes the envelope uniform
// across both planes.
func Fail(c *fiber.Ctx, err error) error {
	e := apierr.From(err)
	// The header is set again here rather than assumed: an error raised before
	// the request-id middleware ran would otherwise answer without one.
	id := RequestID(c)
	if id != "" {
		c.Set(HeaderRequestID, id)
	}
	// Every 429 leaves with a Retry-After, because a client that has to guess
	// will guess badly. A failure that knows how long the wait actually is says
	// so; one second is the fallback for the rest, which are the rejections a
	// caller can retry almost immediately.
	if e.Status == fiber.StatusTooManyRequests && c.Get(fiber.HeaderRetryAfter) == "" {
		seconds := e.RetryAfterSeconds
		if seconds < 1 {
			seconds = 1
		}
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(seconds))
	}
	return c.Status(e.Status).JSON(e.Envelope(id))
}
