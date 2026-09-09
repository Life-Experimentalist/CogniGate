package provider

import (
	"context"
	"strings"

	"github.com/cognigate/gateway/internal/store"
)

// Kinds for the two vendors that do not speak OpenAI natively but publish an
// endpoint that does.
//
// Neither needs a translating adapter. Google and Anthropic each host their own
// OpenAI-compatible surface, and on both of them a chat completion is a POST to
// /chat/completions with a Bearer key, returns choices[] and a usage block, and
// streams as server-sent events terminated by [DONE]. That is precisely what
// the OpenAI adapter already sends and reads, so the wire format is not what
// was missing.
//
// What was missing is that an operator had to know the compatibility URL
// (neither vendor's default endpoint is the one that works) and had to register
// the provider as kind "openai", which then reported "openai" in logs, metrics
// and the served-by attribution for traffic that went to Gemini. These kinds
// exist to fix both: they carry the right base URL so it need not be typed, and
// they carry the vendor's own name so the routing story is legible afterwards.
//
// The honest limitation is Anthropic's: it documents its compatibility layer as
// a way to evaluate Claude with existing OpenAI code rather than as a
// production API, and routes it through the same rate limits as /v1/messages.
// It silently ignores rather than rejects the fields it does not implement:
// response_format, seed, logprobs, presence_penalty and frequency_penalty among
// them, so a caller who depends on one of those gets a valid answer computed
// without it. Requests survive; guarantees do not. A native Messages-API
// adapter is the fix if that becomes load-bearing, and it belongs in this
// package as a third file rather than as a change to this one.
const (
	KindGemini    = "gemini"
	KindAnthropic = "anthropic"
)

// defaultBaseURLs are the endpoints each kind is registered against when a
// provider is created without one. Both are the vendor's documented
// OpenAI-compatibility base, not their native API root.
var defaultBaseURLs = map[string]string{
	KindGemini:    "https://generativelanguage.googleapis.com/v1beta/openai/",
	KindAnthropic: "https://api.anthropic.com/v1/",
}

// DefaultBaseURL reports the endpoint a kind is served from when the operator
// did not supply one. It is false for "openai" and for anything unregistered,
// because there is no such thing as the default address of a self-hosted vLLM
// or of whichever compatible provider someone is pointing at this week. Those
// have to be told where to go.
func DefaultBaseURL(kind string) (string, bool) {
	u, ok := defaultBaseURLs[strings.ToLower(strings.TrimSpace(kind))]
	return u, ok
}

// Compat is the OpenAI adapter under a vendor's name. It reuses the HTTP path
// wholesale: connection pooling, streaming hand-off, body limits, usage
// extraction and failure classification are all behaviour the gateway has
// already proven, and duplicating them per vendor would mean three copies to
// keep correct instead of one.
type Compat struct {
	*OpenAI
	kind string
}

// NewCompat builds an adapter for a vendor's OpenAI-compatible endpoint,
// sharing the given OpenAI adapter's HTTP client rather than opening a second
// connection pool per vendor.
func NewCompat(kind string, base *OpenAI) *Compat {
	return &Compat{OpenAI: base, kind: kind}
}

func (c *Compat) Kind() string { return c.kind }

// ListModels delegates to the OpenAI catalog call and then strips the "models/"
// prefix Google returns on some ids.
//
// The prefix is a resource path from Google's native API that survives into the
// compatibility listing, and it matters because the id is the routing
// identifier: a catalog holding "models/gemini-2.5-flash" makes callers ask for
// a model under a name the completions endpoint does not accept, since there
// the same model is plain "gemini-2.5-flash". Trimming it is a no-op for every
// other provider, so it is done here for all of them rather than being made
// conditional on a vendor check that would then need maintaining.
func (c *Compat) ListModels(ctx context.Context, cred Credential) ([]store.Model, error) {
	models, err := c.OpenAI.ListModels(ctx, cred)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].ID = strings.TrimPrefix(models[i].ID, "models/")
	}
	return models, nil
}
