package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// Attempt records one candidate's outcome, for the structured log line and for
// the X-CogniGate-Fallback-Depth accounting.
type Attempt struct {
	Candidate Candidate
	Failure   provider.FailureKind
	Status    int
	Skipped   bool // breaker was open: no upstream call was made
	Latency   time.Duration
	Err       error
}

// Result is a successful dispatch.
type Result struct {
	Response  *provider.Response
	Candidate Candidate
	// Depth is how many candidates were tried and failed before this one. Zero
	// means the primary served the request.
	Depth    int
	Attempts []Attempt
	Snapshot *catalog.Snapshot
}

// CostUSD prices the exchange from the catalog's rates. Zero when the provider
// publishes no pricing — a wrong number would be worse than no number, since it
// would flow into billing.
func (r *Result) CostUSD(usage *provider.Usage) float64 {
	if usage == nil {
		return 0
	}
	in := r.Candidate.Entry.InputCostPerMTok
	out := r.Candidate.Entry.OutputCostPerMTok
	if in == 0 && out == 0 {
		return 0
	}
	return (float64(usage.PromptTokens)/1e6)*in + (float64(usage.CompletionTokens)/1e6)*out
}

// Dispatcher runs the GW-3 cascade: try candidates in order, rotate keys within
// a provider on a 429, and stop the moment the failure is the caller's fault.
type Dispatcher struct {
	resolver *Resolver
	breaker  *Breaker
	registry *provider.Registry
	store    store.Store
}

func NewDispatcher(r *Resolver, b *Breaker, reg *provider.Registry, s store.Store) *Dispatcher {
	return &Dispatcher{resolver: r, breaker: b, registry: reg, store: s}
}

// Breaker exposes the breaker for /v1/health and the state gauge.
func (d *Dispatcher) Breaker() *Breaker { return d.breaker }

// Dispatch sends one request, cascading on failure.
//
// `body` is the caller's JSON; the resolved model id is substituted into a copy
// for each attempt, so a request that falls back to a different model reaches
// the upstream naming the model that will actually serve it.
func (d *Dispatcher) Dispatch(ctx context.Context, tenantID, requested string, body []byte, stream bool, path string) (*Result, error) {
	candidates, snap, err := d.resolver.Resolve(ctx, tenantID, requested)
	if err != nil {
		return nil, err
	}

	providers, err := d.store.ListProviders(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*store.Provider, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}

	attempts := make([]Attempt, 0, len(candidates))

	for depth, cand := range candidates {
		if !d.breaker.Allow(cand.Key()) {
			attempts = append(attempts, Attempt{Candidate: cand, Skipped: true})
			continue
		}

		p := byID[cand.ProviderID]
		if p == nil || !p.Enabled || len(p.Keys) == 0 {
			attempts = append(attempts, Attempt{
				Candidate: cand,
				Failure:   provider.FailTransport,
				Err:       fmt.Errorf("provider %s has no usable credential", cand.Provider),
			})
			continue
		}

		adapter, ok := d.registry.Get(p.Kind)
		if !ok {
			attempts = append(attempts, Attempt{
				Candidate: cand,
				Failure:   provider.FailTransport,
				Err:       fmt.Errorf("no adapter for provider kind %q", p.Kind),
			})
			continue
		}

		attemptBody, err := withModel(body, cand.Model)
		if err != nil {
			// The caller's JSON is unparseable. That is a 400, and no amount of
			// falling back to another model will change it.
			return nil, apierr.InvalidRequest("Request body is not valid JSON.").WithCause(err)
		}

		resp, attempt, fatal := d.tryCandidate(ctx, adapter, p, cand, &provider.Request{
			Model:  cand.Model,
			Body:   attemptBody,
			Stream: stream,
			Path:   path,
		})
		attempts = append(attempts, attempt)

		if fatal != nil {
			// A request-caused failure. Surfacing it now, unchanged, is the
			// whole point: cascading would charge the caller once per model in
			// the chain to be told the same thing each time.
			return nil, fatal
		}
		if resp != nil {
			d.breaker.Success(cand.Key())
			return &Result{
				Response:  resp,
				Candidate: cand,
				Depth:     depth,
				Attempts:  attempts,
				Snapshot:  snap,
			}, nil
		}
		d.breaker.Failure(cand.Key())
	}

	return nil, apierr.UpstreamExhausted(len(attempts)).
		WithAttempts(renderAttempts(attempts)).
		WithCause(fmt.Errorf("no candidate succeeded: %s", summarize(attempts)))
}

// renderAttempts turns the cascade into the public form GW-3.AC-5 requires the
// 502 body to carry.
//
// Only the classification crosses the boundary. The upstream's own error text
// stays in Attempt.Err, which goes to the log line and no further: it is the one
// field here that can quote a request body, and GW-14 puts that out of bounds.
func renderAttempts(attempts []Attempt) []apierr.Attempt {
	out := make([]apierr.Attempt, 0, len(attempts))
	for _, a := range attempts {
		failure := a.Failure.String()
		if a.Skipped {
			// Not a failure the provider reported — the gateway never asked it.
			// Naming that distinctly is what lets a caller tell "the provider is
			// down" from "the gateway has already decided it is down".
			failure = "breaker_open"
		}
		out = append(out, apierr.Attempt{
			Provider: a.Candidate.Provider,
			Model:    a.Candidate.Model,
			Failure:  failure,
			Status:   a.Status,
		})
	}
	return out
}

// tryCandidate issues one candidate's request, rotating through the provider's
// key pool while the answer is 429.
//
// Returns (response, attempt, nil) on success, (nil, attempt, nil) when the
// caller should cascade, and (nil, attempt, err) when it must not.
func (d *Dispatcher) tryCandidate(
	ctx context.Context,
	adapter provider.Adapter,
	p *store.Provider,
	cand Candidate,
	req *provider.Request,
) (*provider.Response, Attempt, error) {
	attempt := Attempt{Candidate: cand}

	for _, key := range p.Keys {
		resp, err := adapter.Do(ctx, provider.Credential{BaseURL: p.BaseURL, APIKey: key}, req)
		if resp != nil {
			attempt.Status = resp.StatusCode
			attempt.Failure = resp.Failure
			attempt.Latency = resp.Latency
		}
		attempt.Err = err

		switch {
		case errors.Is(err, provider.ErrResponseTooLarge):
			// A single oversized response is a limit breach, not a provider
			// fault: retrying elsewhere would produce the same oversized answer.
			return nil, attempt, apierr.ResponseTooLarge(0).WithCause(err)

		case err != nil && (resp == nil || resp.Failure == provider.FailTransport):
			// Never reached the provider. Cascade.
			attempt.Failure = provider.FailTransport
			return nil, attempt, nil

		case resp == nil:
			attempt.Failure = provider.FailTransport
			return nil, attempt, nil

		case resp.Failure == provider.FailNone:
			return resp, attempt, nil

		case resp.Failure == provider.FailClient:
			// The request itself is bad. Pass the upstream's own status and
			// message through rather than inventing one, so the caller sees
			// what the provider actually objected to.
			return nil, attempt, clientError(resp)

		case resp.Failure == provider.FailRateLimit:
			// Try the next key in this provider's pool before giving up on the
			// provider: a per-key quota is not a provider outage.
			continue

		default: // FailServer
			return nil, attempt, nil
		}
	}

	// Every key in the pool was rate limited. Now the provider counts as
	// failing, and the cascade moves on.
	return nil, attempt, nil
}

// clientError converts an upstream 4xx into the gateway's envelope, preserving
// the upstream's message where it parses as an OpenAI error.
func clientError(resp *provider.Response) error {
	msg := "The upstream provider rejected the request."
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(resp.Body, &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}

	e := &apierr.Error{
		Status: resp.StatusCode,
		Type:   apierr.TypeInvalidRequest,
		Code:   apierr.CodeInvalidRequest,
		Msg:    msg,
		Param:  parsed.Error.Param,
	}
	if parsed.Error.Code != "" {
		e.Code = parsed.Error.Code
	}
	return e
}

// withModel returns a copy of the request body with "model" set to resolved.
//
// The body is decoded into a map of raw messages rather than a typed struct so
// that provider-specific fields the gateway does not model survive the round
// trip untouched — a caller using a provider extension must not lose it just
// because CogniGate is in the path.
func withModel(body []byte, resolved string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, err
	}
	fields["model"] = encoded
	return json.Marshal(fields)
}

// summarize renders the cascade for one log line: which candidates were tried
// and why each one did not serve the request.
func summarize(attempts []Attempt) string {
	parts := make([]string, 0, len(attempts))
	for _, a := range attempts {
		reason := a.Failure.String()
		if a.Skipped {
			reason = "breaker_open"
		}
		parts = append(parts, a.Candidate.ServedBy()+"="+reason)
	}
	return strings.Join(parts, ", ")
}
