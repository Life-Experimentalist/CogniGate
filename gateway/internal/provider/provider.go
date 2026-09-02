// Package provider is the boundary between CogniGate and an upstream LLM API.
//
// Everything above this package works in terms of the Adapter interface, so
// adding a provider whose wire format differs is a new file here rather than a
// change to the router, the catalog, or the metering path.
package provider

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/cognigate/gateway/internal/store"
)

// Credential is one upstream account: where to reach it and which key to use.
// Keys are held per call rather than per adapter because GW-3 rotates within a
// provider's key pool before it gives up on the provider.
type Credential struct {
	BaseURL string
	APIKey  string
}

// FailureKind classifies an upstream outcome for the GW-3 cascade decision.
// This is the whole reason the classification lives here: only the adapter
// knows whether a given provider's 400 means "your request was malformed" or
// "this provider is unhappy", and routing must not cascade on the former.
type FailureKind int

const (
	// FailNone is a successful exchange.
	FailNone FailureKind = iota
	// FailTransport is a connection error, DNS failure or timeout. Cascade.
	FailTransport
	// FailServer is a provider 5xx. Cascade.
	FailServer
	// FailRateLimit is a provider 429. Rotate to the next key in the pool, and
	// cascade only once the pool is exhausted.
	FailRateLimit
	// FailClient is a 4xx the request itself caused — a malformed body, an
	// oversized prompt, a content-policy refusal. MUST NOT cascade: the same
	// request would fail identically on every other model, so a cascade would
	// burn the whole chain and multiply the caller's bill for one bad request.
	FailClient
)

func (f FailureKind) String() string {
	switch f {
	case FailNone:
		return "none"
	case FailTransport:
		return "transport"
	case FailServer:
		return "server"
	case FailRateLimit:
		return "rate_limit"
	case FailClient:
		return "client"
	default:
		return "unknown"
	}
}

// Retryable reports whether routing should try the next candidate.
func (f FailureKind) Retryable() bool {
	return f == FailTransport || f == FailServer || f == FailRateLimit
}

// Usage is the token accounting a provider reports back.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Request is one upstream call. Body is the already-canonicalised JSON with the
// resolved model substituted in, so the adapter neither parses nor rewrites it
// on the hot path.
type Request struct {
	Model  string
	Body   []byte
	Stream bool
	// Path is the upstream route relative to the base URL, e.g.
	// "/chat/completions". Carried explicitly so one adapter serves every
	// OpenAI-shaped endpoint.
	Path string
}

// Response is one upstream reply.
//
// Exactly one of Body and Stream is populated. Stream is an io.ReadCloser
// rather than a buffered []byte because a streamed completion must reach the
// caller token by token: buffering it here would defeat the only reason the
// caller asked for a stream.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Stream     io.ReadCloser
	Usage      *Usage
	// Failure is FailNone on success. On failure it is the cascade
	// classification, and Body holds the upstream error for logging.
	Failure FailureKind
	// Latency is the time to first byte for a stream, or to the full body
	// otherwise.
	Latency time.Duration
}

// Close releases the stream if there is one. Safe on a nil or non-streaming
// response, so callers can defer it unconditionally.
func (r *Response) Close() {
	if r != nil && r.Stream != nil {
		_ = r.Stream.Close()
	}
}

// Adapter speaks one upstream wire format.
type Adapter interface {
	// Kind is the adapter identifier used in provider configuration.
	Kind() string

	// ListModels enumerates what the account can reach, for the GW-1 catalog.
	ListModels(ctx context.Context, cred Credential) ([]store.Model, error)

	// Do issues one request. A non-nil error is reserved for failures with no
	// HTTP exchange at all; anything the upstream answered comes back as a
	// Response with Failure set, because the router needs the status to decide
	// whether to cascade.
	Do(ctx context.Context, cred Credential, req *Request) (*Response, error)
}

// Registry maps an adapter kind to its implementation.
type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: make(map[string]Adapter, len(adapters))}
	for _, a := range adapters {
		r.adapters[a.Kind()] = a
	}
	return r
}

// Get resolves an adapter kind. An unregistered kind falls back to the
// OpenAI-compatible adapter, because that is what the overwhelming majority of
// providers speak and refusing to route would be the less useful answer.
func (r *Registry) Get(kind string) (Adapter, bool) {
	if a, ok := r.adapters[kind]; ok {
		return a, true
	}
	a, ok := r.adapters[KindOpenAI]
	return a, ok
}

// Classify maps an HTTP status onto the cascade decision. Shared by every
// adapter that speaks HTTP, which is all of them so far.
func Classify(status int) FailureKind {
	switch {
	case status >= 200 && status < 300:
		return FailNone
	case status == http.StatusTooManyRequests:
		return FailRateLimit
	case status >= 500:
		return FailServer
	case status >= 400:
		return FailClient
	default:
		// 1xx and 3xx should never reach here: the client follows redirects and
		// does not surface informational responses. Treat as a provider fault
		// rather than blaming the caller's request.
		return FailServer
	}
}
