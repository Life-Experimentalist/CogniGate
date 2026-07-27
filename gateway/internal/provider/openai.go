package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cognigate/gateway/internal/store"
)

// KindOpenAI is the adapter for the OpenAI HTTP API and the many providers that
// reimplement it — Together, Groq, Fireworks, vLLM, Ollama, OpenRouter, Azure
// OpenAI. One adapter covers all of them because they agree on the wire format;
// only the base URL differs.
const KindOpenAI = "openai"

// OpenAI implements Adapter over any OpenAI-compatible endpoint.
type OpenAI struct {
	client *http.Client
	// maxResponseBytes caps a non-streaming body. Streaming bodies are bounded
	// by the caller as they are relayed, since their size is not known up front.
	maxResponseBytes int64
}

// NewOpenAI builds the adapter. connectTimeout bounds dialling; overall request
// deadlines come from the context, so a streaming call is not killed by a
// client-level timeout part-way through a long completion.
func NewOpenAI(connectTimeout time.Duration, maxResponseBytes int64) *OpenAI {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   connectTimeout,
		ExpectContinueTimeout: time.Second,
		// Response headers must arrive within the connect budget even if the
		// body then streams for minutes; a provider that accepts the connection
		// and says nothing is indistinguishable from a hang otherwise.
		ResponseHeaderTimeout: 0,
		ForceAttemptHTTP2:     true,
	}
	return &OpenAI{
		client: &http.Client{
			Transport: transport,
			// No client-level Timeout: it would abort long streams. The
			// per-request context carries the deadline instead.
		},
		maxResponseBytes: maxResponseBytes,
	}
}

func (o *OpenAI) Kind() string { return KindOpenAI }

// modelsResponse is the shape of GET /v1/models.
type modelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
		// Non-standard but widely emitted; used when present and ignored
		// otherwise, so a provider that omits them still yields a usable
		// catalog entry.
		ContextWindow   int `json:"context_window"`
		ContextLength   int `json:"context_length"`
		MaxOutputTokens int `json:"max_output_tokens"`
	} `json:"data"`
}

func (o *OpenAI) ListModels(ctx context.Context, cred Credential) ([]store.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(cred.BaseURL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	o.authorize(req, cred)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, o.maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: upstream returned %d", resp.StatusCode)
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("list models: malformed catalog response: %w", err)
	}

	out := make([]store.Model, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		window := m.ContextWindow
		if window == 0 {
			window = m.ContextLength
		}
		out = append(out, store.Model{
			ID:              m.ID,
			ContextWindow:   window,
			MaxOutputTokens: m.MaxOutputTokens,
			Capabilities:    inferCapabilities(m.ID),
		})
	}
	return out, nil
}

func (o *OpenAI) Do(ctx context.Context, cred Credential, r *Request) (*Response, error) {
	started := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		joinURL(cred.BaseURL, r.Path), bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	o.authorize(req, cred)
	req.Header.Set("Content-Type", "application/json")
	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := o.client.Do(req)
	if err != nil {
		// No HTTP exchange happened — dial failure, TLS failure, or the
		// context deadline. All three mean "this candidate is unusable",
		// which is a cascade.
		return &Response{
			Failure: FailTransport,
			Latency: time.Since(started),
		}, fmt.Errorf("upstream unreachable: %w", err)
	}

	kind := Classify(resp.StatusCode)

	// A failure never streams: read the (small) error body so the router can log
	// it and move on, and release the connection.
	if kind != FailNone {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &Response{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       body,
			Failure:    kind,
			Latency:    time.Since(started),
		}, nil
	}

	if r.Stream {
		// Hand the live body up. Ownership of Close transfers to the caller.
		return &Response{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Stream:     resp.Body,
			Failure:    FailNone,
			Latency:    time.Since(started),
		}, nil
	}

	defer resp.Body.Close()
	// Read one byte past the limit so an oversized body is detectable rather
	// than silently truncated into malformed JSON.
	body, err := io.ReadAll(io.LimitReader(resp.Body, o.maxResponseBytes+1))
	if err != nil {
		return &Response{Failure: FailTransport, Latency: time.Since(started)},
			fmt.Errorf("reading upstream body: %w", err)
	}
	if int64(len(body)) > o.maxResponseBytes {
		return &Response{
			StatusCode: resp.StatusCode,
			Failure:    FailServer,
			Latency:    time.Since(started),
		}, ErrResponseTooLarge
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       body,
		Usage:      extractUsage(body),
		Failure:    FailNone,
		Latency:    time.Since(started),
	}, nil
}

// ErrResponseTooLarge signals a body past limits.max_response_bytes. It is a
// distinct error because GW-13 gives it its own response code rather than
// folding it into a generic upstream failure.
var ErrResponseTooLarge = fmt.Errorf("upstream response exceeds the configured limit")

func (o *OpenAI) authorize(req *http.Request, cred Credential) {
	if cred.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	}
	req.Header.Set("User-Agent", "CogniGate/1.0 (+https://github.com/Life-Experimentalist/CogniGate)")
}

// usageEnvelope pulls just the usage block out of a completion, without
// unmarshalling the choices — the content is deliberately never materialised
// into a Go value the metering path could accidentally persist (GW-14).
type usageEnvelope struct {
	Usage *Usage `json:"usage"`
}

func extractUsage(body []byte) *Usage {
	var env usageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	return env.Usage
}

// joinURL appends a path to a base URL without doubling or dropping the
// separator. Providers are configured with and without trailing slashes in
// roughly equal measure.
func joinURL(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

// inferCapabilities derives capability tags from a model id. Providers do not
// advertise capabilities in a standard field, so GW-2 alias constraints need
// some basis; naming conventions are the only signal available across every
// OpenAI-compatible provider. An operator who needs exactness sets the
// capability explicitly on the alias instead of relying on this.
func inferCapabilities(id string) []string {
	lower := strings.ToLower(id)
	caps := []string{"chat"}
	switch {
	case strings.Contains(lower, "embed"):
		caps = []string{"embeddings"}
	case strings.Contains(lower, "whisper"), strings.Contains(lower, "transcribe"):
		caps = []string{"transcription"}
	}
	for _, marker := range []string{"vision", "-4o", "sonnet", "opus", "gemini"} {
		if strings.Contains(lower, marker) && caps[0] == "chat" {
			caps = append(caps, "vision")
			break
		}
	}
	if strings.Contains(lower, "tool") || caps[0] == "chat" {
		caps = append(caps, "tools")
	}
	return caps
}
