// Package apierr implements the GW-7 error contract.
//
// Every failure on either plane leaves the gateway as the same OpenAI-shaped
// envelope, so an existing OpenAI client can parse a CogniGate error without a
// special case:
//
//	{"error": {"message": ..., "type": ..., "code": ..., "param": ...}}
//
// upstream_exhausted adds one key, "attempts", because GW-3.AC-5 requires the
// cascade to be legible to the caller. Unknown keys are ignored by every SDK
// that parses this shape, so the compatibility claim survives it.
//
// The (status, code) pairs below are a closed registry. Handlers pick a
// constructor from this package rather than writing statuses inline, which is
// what keeps the registry closed as the surface grows.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

// Envelope types. These reuse OpenAI's vocabulary rather than inventing a
// parallel one — clients already branch on these strings.
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeAuthentication = "authentication_error"
	TypeRateLimit      = "rate_limit_error"
	TypeAPI            = "api_error"
)

// Codes. The registry is closed: adding a code means adding it here and to the
// GW-7 documentation table at the same time.
const (
	// 401
	CodeInvalidAPIKey = "invalid_api_key"
	CodeWrongPlane    = "wrong_plane"

	// 400
	CodeInvalidRequest       = "invalid_request"
	CodeFallbackDuplicate    = "fallback_duplicate_model"
	CodeCaptureTTLTooLong    = "capture_ttl_too_long"
	CodeUnsupportedParameter = "unsupported_parameter"

	// 403
	CodeInsufficientScope = "insufficient_scope"

	// 404
	CodeModelNotFound     = "model_not_found"
	CodeAliasUnresolvable = "alias_unresolvable"
	CodeNotSupported      = "not_supported"
	CodeResourceNotFound  = "resource_not_found"

	// 409
	CodeAliasCollides = "alias_collides_with_model"
	CodeConflict      = "conflict"

	// 413
	CodeRequestTooLarge = "request_too_large"

	// 429
	CodeRateLimited         = "rate_limited"
	CodeConcurrencyExceeded = "concurrency_exceeded"
	CodeQuotaExceeded       = "quota_exceeded"
	CodeBudgetExceeded      = "budget_exceeded"

	// 502
	CodeUpstreamExhausted = "upstream_exhausted"
	CodeResponseTooLarge  = "response_too_large"
	CodeUpstreamError     = "upstream_error"

	// 503
	CodeUnavailable = "service_unavailable"

	// 504
	CodeGatewayTimeout = "gateway_timeout"

	// Terminal SSE event only — never an HTTP status, because the status line
	// was already sent when the stream stalled.
	CodeUpstreamStreamStalled = "upstream_stream_stalled"
)

// Attempt is one entry of a routing cascade, as GW-3.AC-5 requires the
// upstream_exhausted body to enumerate them.
//
// The failure is the classification, never the upstream's own message: a
// caller needs to know whether the chain died of rate limits or of 5xx, and
// that is answerable from a closed vocabulary. Passing the upstream text
// through instead would leak provider detail into a body GW-14 governs, and
// would make the field unusable for branching.
type Attempt struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Failure is a provider.FailureKind rendered as a string, or "breaker_open"
	// for a candidate the circuit breaker skipped without dialling.
	Failure string `json:"failure"`
	// Status is the upstream's HTTP status, absent when there was never a
	// response to take one from.
	Status int `json:"status,omitempty"`
}

// Body is the wire shape. `param` is a pointer so it serialises as JSON null
// rather than "" when a failure is not attributable to one field, which is what
// OpenAI clients expect.
//
// `attempts` is CogniGate's one addition to OpenAI's shape. It is omitted from
// every error but upstream_exhausted, and an SDK that does not know about it
// ignores it, so GW-7's "stock error handling works" still holds.
type Body struct {
	Message   string    `json:"message"`
	Type      string    `json:"type"`
	Code      string    `json:"code"`
	Param     *string   `json:"param"`
	RequestID string    `json:"request_id,omitempty"`
	Attempts  []Attempt `json:"attempts,omitempty"`
}

// Envelope wraps Body under the single "error" key.
type Envelope struct {
	Error Body `json:"error"`
}

// Error is a failure carrying everything needed to render the envelope. It
// implements error so it can travel up through ordinary Go control flow and be
// rendered once, at the HTTP boundary.
type Error struct {
	Status int
	Code   string
	Type   string
	Msg    string
	Param  string

	// Wrapped keeps the underlying cause for logging. It is deliberately not
	// serialised: GW-14 forbids leaking upstream detail to callers, and an
	// upstream error string can contain fragments of the prompt.
	Wrapped error

	// Attempts is the routing cascade, rendered only for upstream_exhausted.
	Attempts []Attempt
}

func (e *Error) Error() string {
	if e.Wrapped != nil {
		return fmt.Sprintf("%s (%d %s): %v", e.Msg, e.Status, e.Code, e.Wrapped)
	}
	return fmt.Sprintf("%s (%d %s)", e.Msg, e.Status, e.Code)
}

func (e *Error) Unwrap() error { return e.Wrapped }

// Envelope renders the public body. The request id is threaded in at the HTTP
// boundary rather than stored on the Error, so that constructing an Error never
// needs the request context.
func (e *Error) Envelope(requestID string) Envelope {
	var param *string
	if e.Param != "" {
		p := e.Param
		param = &p
	}
	return Envelope{Error: Body{
		Message:   e.Msg,
		Type:      e.Type,
		Code:      e.Code,
		Param:     param,
		RequestID: requestID,
		Attempts:  e.Attempts,
	}}
}

// WithParam names the offending field. Returns a copy so the shared sentinel
// values below are never mutated.
func (e *Error) WithParam(param string) *Error {
	c := *e
	c.Param = param
	return &c
}

// WithCause attaches the underlying error for logs. Returns a copy.
func (e *Error) WithCause(err error) *Error {
	c := *e
	c.Wrapped = err
	return &c
}

// WithAttempts attaches the routing cascade for rendering. Returns a copy.
func (e *Error) WithAttempts(attempts []Attempt) *Error {
	c := *e
	c.Attempts = attempts
	return &c
}

func newErr(status int, typ, code, msg string) *Error {
	return &Error{Status: status, Type: typ, Code: code, Msg: msg}
}

// --- 401 -------------------------------------------------------------------

// InvalidAPIKey covers an absent, malformed, unknown, revoked, or expired key.
// The four cases are deliberately indistinguishable to the caller: telling an
// attacker that a key was "revoked" rather than "unknown" confirms it once
// existed.
func InvalidAPIKey() *Error {
	return newErr(http.StatusUnauthorized, TypeAuthentication, CodeInvalidAPIKey,
		"Invalid API key provided.")
}

// WrongPlane is a well-formed key of the wrong kind — a cg- key on /admin/v1,
// or a cga- key on /v1. Distinct from InvalidAPIKey because the caller holds a
// real credential and needs to know it is pointed at the wrong plane.
func WrongPlane(want string) *Error {
	return newErr(http.StatusUnauthorized, TypeAuthentication, CodeWrongPlane,
		fmt.Sprintf("This endpoint requires a %s key.", want))
}

// --- 400 -------------------------------------------------------------------

func InvalidRequest(msg string) *Error {
	return newErr(http.StatusBadRequest, TypeInvalidRequest, CodeInvalidRequest, msg)
}

func FallbackDuplicate(model string) *Error {
	return newErr(http.StatusBadRequest, TypeInvalidRequest, CodeFallbackDuplicate,
		fmt.Sprintf("Model %q appears more than once in the fallback chain.", model))
}

func CaptureTTLTooLong(maxHours int) *Error {
	return newErr(http.StatusBadRequest, TypeInvalidRequest, CodeCaptureTTLTooLong,
		fmt.Sprintf("Debug capture ttl must not exceed %d hours.", maxHours))
}

func UnsupportedParameter(param string) *Error {
	return newErr(http.StatusBadRequest, TypeInvalidRequest, CodeUnsupportedParameter,
		fmt.Sprintf("Parameter %q is not supported by the resolved model.", param)).
		WithParam(param)
}

// --- 403 -------------------------------------------------------------------

// InsufficientScope is a valid admin key whose scope does not reach the target
// tenant — tenant:<id> keys are confined to their own tenant.
func InsufficientScope() *Error {
	return newErr(http.StatusForbidden, TypeInvalidRequest, CodeInsufficientScope,
		"This admin key is not scoped to the requested tenant.")
}

// --- 404 -------------------------------------------------------------------

func ModelNotFound(model string) *Error {
	return newErr(http.StatusNotFound, TypeInvalidRequest, CodeModelNotFound,
		fmt.Sprintf("The model %q does not exist or you do not have access to it.", model)).
		WithParam("model")
}

func AliasUnresolvable(alias string) *Error {
	return newErr(http.StatusNotFound, TypeInvalidRequest, CodeAliasUnresolvable,
		fmt.Sprintf("The alias %q resolved to no available model.", alias)).
		WithParam("model")
}

// NotSupported answers an OpenAI route CogniGate does not implement. GW-9
// requires this to be explicit: silently passing an unknown path through to a
// provider would make the gateway's surface unknowable.
func NotSupported(path string) *Error {
	return newErr(http.StatusNotFound, TypeInvalidRequest, CodeNotSupported,
		fmt.Sprintf("%s is not implemented by this gateway.", path))
}

func ResourceNotFound(kind, id string) *Error {
	return newErr(http.StatusNotFound, TypeInvalidRequest, CodeResourceNotFound,
		fmt.Sprintf("No %s with id %q.", kind, id))
}

// --- 409 -------------------------------------------------------------------

func AliasCollides(name string) *Error {
	return newErr(http.StatusConflict, TypeInvalidRequest, CodeAliasCollides,
		fmt.Sprintf("The alias %q collides with a real model id in the catalog.", name)).
		WithParam("name")
}

func Conflict(msg string) *Error {
	return newErr(http.StatusConflict, TypeInvalidRequest, CodeConflict, msg)
}

// --- 413 -------------------------------------------------------------------

func RequestTooLarge(limit int64) *Error {
	return newErr(http.StatusRequestEntityTooLarge, TypeInvalidRequest, CodeRequestTooLarge,
		fmt.Sprintf("Request body exceeds the %d byte limit.", limit))
}

// --- 429 -------------------------------------------------------------------

func RateLimited() *Error {
	return newErr(http.StatusTooManyRequests, TypeRateLimit, CodeRateLimited,
		"Rate limit exceeded. Retry after the interval in the Retry-After header.")
}

func ConcurrencyExceeded(limit int) *Error {
	return newErr(http.StatusTooManyRequests, TypeRateLimit, CodeConcurrencyExceeded,
		fmt.Sprintf("Too many concurrent requests for this key (limit %d).", limit))
}

func QuotaExceeded() *Error {
	return newErr(http.StatusTooManyRequests, TypeRateLimit, CodeQuotaExceeded,
		"Token quota exhausted for the current period.")
}

func BudgetExceeded() *Error {
	return newErr(http.StatusTooManyRequests, TypeRateLimit, CodeBudgetExceeded,
		"Spend budget exhausted for the current period.")
}

// --- 502 -------------------------------------------------------------------

// UpstreamExhausted is the end of a fallback cascade: every candidate was tried
// and none produced a response. Callers pair it with WithAttempts, which is
// what GW-3.AC-5 requires the body to enumerate.
func UpstreamExhausted(attempts int) *Error {
	return newErr(http.StatusBadGateway, TypeAPI, CodeUpstreamExhausted,
		fmt.Sprintf("All %d routing candidates failed.", attempts))
}

func ResponseTooLarge(limit int64) *Error {
	return newErr(http.StatusBadGateway, TypeAPI, CodeResponseTooLarge,
		fmt.Sprintf("Upstream response exceeds the %d byte limit.", limit))
}

func UpstreamError(msg string) *Error {
	return newErr(http.StatusBadGateway, TypeAPI, CodeUpstreamError, msg)
}

// --- 503 -------------------------------------------------------------------

func Unavailable(msg string) *Error {
	return newErr(http.StatusServiceUnavailable, TypeAPI, CodeUnavailable, msg)
}

// --- 504 -------------------------------------------------------------------

func GatewayTimeout(seconds float64) *Error {
	return newErr(http.StatusGatewayTimeout, TypeAPI, CodeGatewayTimeout,
		fmt.Sprintf("The request exceeded the %.0fs gateway timeout.", seconds))
}

// From maps an arbitrary error onto the registry. Anything not already an
// *Error becomes an opaque 500: an unclassified error is a gateway bug, and its
// text may quote a request body GW-14 forbids echoing.
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{
		Status:  http.StatusInternalServerError,
		Type:    TypeAPI,
		Code:    "internal_error",
		Msg:     "The gateway encountered an internal error.",
		Wrapped: err,
	}
}
