// Package mockprovider serves an OpenAI-compatible API that can be told to fail.
//
// GW-10 requires the conformance suite to drive a gateway through upstream
// conditions no real provider will produce on request: a 429, a 500, a stall
// past the read timeout, a model that disappears between two catalog refreshes.
// This is that upstream. It speaks the subset of the OpenAI API the gateway's
// adapter actually calls — GET /models and POST /chat/completions, streaming
// and not — and exposes a control plane under /_control that the suite uses to
// arrange each condition.
//
// The control plane shares the data plane's listener on purpose. In a container
// deployment the gateway reaches this process by one hostname and the suite by
// another, and putting the two planes on separate ports would double that
// problem rather than halve it.
//
// Concurrency (GW-10.AC-3): fault state and request counts are keyed by model
// id, so two suite runs that each register their own models against one mock
// cannot see each other's faults. Reset is the exception and says so.
package mockprovider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fault modes. A mode outside this set is rejected at the control plane rather
// than silently ignored, so a typo in a test fails that test instead of quietly
// producing a passing run against an upstream that never misbehaved.
const (
	FaultNone        = "none"
	FaultRateLimit   = "rate_limit"
	FaultServerError = "server_error"
	FaultTimeout     = "timeout"
	// FaultClientError is a request-caused 400. It is a separate mode from the
	// 404 an unknown model already produces because a gateway will not route to
	// a model absent from its catalog at all, so that 404 is unreachable from a
	// test — and GW-3 needs a client failure the gateway does reach in order to
	// prove it does not cascade on one.
	FaultClientError = "client_error"
	// FaultStreamAbort dies after the stream has already begun. See
	// abortMidStream for why a timeout is not a substitute.
	FaultStreamAbort = "stream_abort"
)

// ListingTarget is the reserved value of the fault control's model field that
// arranges a fault on GET /models rather than on a completion.
//
// It does not participate in the catch-all: a fault registered for every model
// is about completions, and silently breaking catalog refresh as a side effect
// of arranging one would make a whole class of test failures unreadable.
const ListingTarget = "_listing"

// ForeverCount asks for a fault that applies until it is cleared.
const ForeverCount = -1

// DefaultTimeoutDelay outlasts the gateway's 10s upstream connect timeout and
// its 60s stream idle timeout without making a test that forgets to clear the
// fault hang for minutes.
const DefaultTimeoutDelay = 75 * time.Second

// Model is one entry served from GET /models. The gateway derives capabilities
// from the id rather than reading them here, so the id is the part that decides
// how a model can be routed to.
type Model struct {
	ID              string `json:"id"`
	OwnedBy         string `json:"owned_by"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	// Prices in USD per million tokens, published because a provider may. They
	// are what makes GW-2's cost tiers testable: with every model at zero,
	// "cheapest" falls through to the alphabetical tie-break, and a test that
	// added a model whose id happened to sort first would pass without any of
	// the cost machinery having run.
	InputCostPerMTok  float64 `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok float64 `json:"output_cost_per_mtok,omitempty"`
}

// SeedModels are present on every fresh mock. The ids are chosen for what the
// gateway's inferCapabilities makes of them: "embed" yields embeddings only,
// "transcribe" yields transcription only, "vision" adds vision to chat, and
// anything else is chat plus tools. That spread is what the GW-2 alias tests
// need in order to distinguish capability filtering from ordering.
//
// The prices are deliberately not in id order — mock-chat-b is both later in
// the alphabet and cheaper than mock-chat-a — so a resolver that ignored cost
// and sorted by id would pick the other one and be caught.
func SeedModels() []Model {
	return []Model{
		{ID: "mock-chat-a", OwnedBy: "mock", ContextWindow: 128000, MaxOutputTokens: 4096,
			InputCostPerMTok: 3.00, OutputCostPerMTok: 15.00},
		{ID: "mock-chat-b", OwnedBy: "mock", ContextWindow: 32000, MaxOutputTokens: 4096,
			InputCostPerMTok: 0.50, OutputCostPerMTok: 1.50},
		{ID: "mock-vision", OwnedBy: "mock", ContextWindow: 128000, MaxOutputTokens: 4096,
			InputCostPerMTok: 5.00, OutputCostPerMTok: 20.00},
		{ID: "mock-embed", OwnedBy: "mock", ContextWindow: 8192,
			InputCostPerMTok: 0.02},
		{ID: "mock-transcribe", OwnedBy: "mock"},
	}
}

type fault struct {
	mode      string
	remaining int // ForeverCount means "until cleared"
	delay     time.Duration
}

// Server is the mock upstream. The zero value is not usable; call New.
type Server struct {
	mu       sync.Mutex
	models   map[string]Model
	order    []string          // insertion order, so GET /models is stable
	faults   map[string]*fault // by model id; "" is the catch-all
	requests map[string]int    // completions served, by model id
	keys     map[string]int    // completions served, by credential
}

func New() *Server {
	s := &Server{
		faults:   map[string]*fault{},
		requests: map[string]int{},
		keys:     map[string]int{},
	}
	s.seed()
	return s
}

func (s *Server) seed() {
	s.models = map[string]Model{}
	s.order = nil
	for _, m := range SeedModels() {
		s.models[m.ID] = m
		s.order = append(s.order, m.ID)
	}
}

// Handler routes both planes.
//
// Each data-plane path is registered twice, bare and under /v1, because the
// base URL a deployment registers for this provider may or may not already end
// in /v1 and both spellings are things an operator legitimately writes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	for _, prefix := range []string{"", "/v1"} {
		mux.HandleFunc("GET "+prefix+"/models", s.listModels)
		mux.HandleFunc("POST "+prefix+"/chat/completions", s.chatCompletions)
	}

	mux.HandleFunc("GET /_control/state", s.controlState)
	mux.HandleFunc("POST /_control/models", s.controlAddModel)
	mux.HandleFunc("DELETE /_control/models/{id}", s.controlRemoveModel)
	mux.HandleFunc("POST /_control/faults", s.controlSetFault)
	mux.HandleFunc("POST /_control/reset", s.controlReset)

	return mux
}

// --- data plane -------------------------------------------------------------

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	// GW-1 requires a gateway to keep serving the last good catalog when a
	// provider's listing endpoint goes away, which cannot be arranged unless the
	// listing endpoint can be made to fail on its own — separately from
	// completions, since the point of the test is that completions still work.
	if mode, delay := s.takeFaultExact(ListingTarget); mode != FaultNone {
		switch mode {
		case FaultRateLimit:
			w.Header().Set("Retry-After", "1")
			writeProviderError(w, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded")
			return
		case FaultServerError:
			writeProviderError(w, http.StatusInternalServerError, "server_error", "the server had an error")
			return
		case FaultTimeout:
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
			}
			return
		}
	}

	s.mu.Lock()
	data := make([]Model, 0, len(s.order))
	for _, id := range s.order {
		if m, ok := s.models[id]; ok {
			data = append(data, m)
		}
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

type completionRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	var req completionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProviderError(w, http.StatusBadRequest, "invalid_request_error", "malformed request body")
		return
	}

	// Counted before anything can fail the request, and before the model is even
	// known. "How many times did the gateway dial us, and with which pooled
	// credential" is what GW-3 asserts on, and a faulted call is still a call:
	// counting only successes would make "the breaker skipped this entry with
	// zero upstream calls" indistinguishable from "the entry was tried and
	// returned 500", which is exactly the distinction those tests exist to draw.
	s.mu.Lock()
	s.requests[req.Model]++
	if cred := credential(r); cred != "" {
		s.keys[cred]++
	}
	_, known := s.models[req.Model]
	s.mu.Unlock()

	if !known {
		// The shape a real OpenAI-compatible upstream returns for an unknown
		// model. It is a 404, which classifies as a client failure, so the
		// gateway must NOT cascade on it (GW-3).
		writeProviderError(w, http.StatusNotFound,
			"invalid_request_error", fmt.Sprintf("the model %q does not exist", req.Model))
		return
	}

	if mode, delay := s.takeFault(req.Model); mode != FaultNone {
		switch mode {
		case FaultRateLimit:
			w.Header().Set("Retry-After", "1")
			writeProviderError(w, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded")
			return
		case FaultServerError:
			writeProviderError(w, http.StatusInternalServerError, "server_error", "the server had an error")
			return
		case FaultClientError:
			writeProviderError(w, http.StatusBadRequest,
				"invalid_request_error", "the request was not valid")
			return
		case FaultTimeout:
			// Honour cancellation so a cleared-late fault does not pin a
			// goroutine for the whole delay after the client has gone.
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
			}
			return
		case FaultStreamAbort:
			s.abortMidStream(w, req.Model)
			return
		}
	}

	if req.Stream {
		s.streamCompletion(w, req.Model)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": completionText},
			"finish_reason": "stop",
		}},
		"usage": usageBlock(),
	})
}

// completionText is deliberately dull. GW-14 forbids the gateway from storing
// or logging content, and a test that asserts on a memorable string invites
// someone to satisfy it by capturing one.
const completionText = "ok"

func usageBlock() map[string]any {
	return map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
}

func streamChunk(model string, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0, "delta": delta, "finish_reason": finish,
		}},
	}
}

// beginStream writes the SSE headers and returns a frame writer, or nil if the
// response writer cannot stream.
func beginStream(w http.ResponseWriter) func(map[string]any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProviderError(w, http.StatusInternalServerError, "server_error", "streaming unsupported")
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	return func(payload map[string]any) {
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
	}
}

func (s *Server) streamCompletion(w http.ResponseWriter, model string) {
	chunk := beginStream(w)
	if chunk == nil {
		return
	}

	chunk(streamChunk(model, map[string]any{"role": "assistant"}, nil))
	chunk(streamChunk(model, map[string]any{"content": completionText}, nil))
	chunk(streamChunk(model, map[string]any{}, "stop"))

	// A usage-bearing final chunk, as an upstream sends when the caller asked
	// for stream_options.include_usage. It is sent unconditionally: the
	// gateway has to account for a streamed request either way, and an
	// upstream that volunteers usage is the easier case to be correct about.
	final := streamChunk(model, map[string]any{}, nil)
	final["choices"] = []any{}
	final["usage"] = usageBlock()
	chunk(final)

	fmt.Fprint(w, "data: [DONE]\n\n")
	// The [DONE] frame was written through the same flusher beginStream
	// captured, so it is already on the wire by the time this returns.
}

// abortMidStream opens a normal stream, emits real content, and then kills the
// connection with no terminating frame.
//
// FaultTimeout cannot stand in for this. A timeout stalls before the first byte,
// which is precisely the window in which GW-3 still permits the gateway to fall
// back to another model; once a byte of content has reached the client,
// switching models would splice two models' output into one response, which
// GW-3.AC-7 forbids. The two failures look nearly identical to a gateway's error
// handling and mean opposite things about what it is allowed to do next, so the
// suite has to be able to produce each of them on demand.
func (s *Server) abortMidStream(w http.ResponseWriter, model string) {
	chunk := beginStream(w)
	if chunk == nil {
		return
	}

	chunk(streamChunk(model, map[string]any{"role": "assistant"}, nil))
	chunk(streamChunk(model, map[string]any{"content": completionText}, nil))

	// ErrAbortHandler is net/http's "drop this connection, and do not log a
	// stack trace for it". The client sees the stream stop without a stop
	// chunk and without [DONE], which is what a provider dying mid-response
	// actually looks like.
	panic(http.ErrAbortHandler)
}

// --- control plane ----------------------------------------------------------

func (s *Server) controlState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	faults := map[string]any{}
	for id, f := range s.faults {
		faults[id] = map[string]any{"mode": f.mode, "remaining": f.remaining}
	}
	models := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if _, ok := s.models[id]; ok {
			models = append(models, id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":   models,
		"faults":   faults,
		"requests": s.requests,
		"keys":     s.keys,
	})
}

func (s *Server) controlAddModel(w http.ResponseWriter, r *http.Request) {
	var m Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || strings.TrimSpace(m.ID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a model id is required"})
		return
	}
	if m.OwnedBy == "" {
		m.OwnedBy = "mock"
	}

	s.mu.Lock()
	if _, exists := s.models[m.ID]; !exists {
		s.order = append(s.order, m.ID)
	}
	s.models[m.ID] = m
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) controlRemoveModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	_, existed := s.models[id]
	delete(s.models, id)
	s.mu.Unlock()

	if !existed {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such model"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type faultRequest struct {
	Model   string `json:"model"`
	Mode    string `json:"mode"`
	Count   int    `json:"count"`
	DelayMS int    `json:"delay_ms"`
}

func (s *Server) controlSetFault(w http.ResponseWriter, r *http.Request) {
	var req faultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed body"})
		return
	}
	switch req.Mode {
	case FaultNone, FaultRateLimit, FaultServerError, FaultTimeout,
		FaultClientError, FaultStreamAbort:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("unknown fault mode %q", req.Mode)})
		return
	}
	// Two of the modes describe things only a completion can do. Accepting them
	// against the listing target and then ignoring them would leave a test
	// arranged against an endpoint that never misbehaved, passing for the wrong
	// reason.
	if req.Model == ListingTarget {
		switch req.Mode {
		case FaultNone, FaultRateLimit, FaultServerError, FaultTimeout:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("fault mode %q does not apply to %s", req.Mode, ListingTarget)})
			return
		}
	}

	delay := DefaultTimeoutDelay
	if req.DelayMS > 0 {
		delay = time.Duration(req.DelayMS) * time.Millisecond
	}

	s.mu.Lock()
	if req.Mode == FaultNone {
		delete(s.faults, req.Model)
	} else {
		count := req.Count
		if count == 0 {
			count = ForeverCount
		}
		s.faults[req.Model] = &fault{mode: req.Mode, remaining: count, delay: delay}
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// controlReset restores the seed models and clears all faults and counters.
//
// It is the one control operation that is not safe against a concurrent suite
// run, because it discards state that belongs to every caller and not just the
// one asking. The suite does not use it; it exists for a human driving the mock
// by hand.
func (s *Server) controlReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seed()
	s.faults = map[string]*fault{}
	s.requests = map[string]int{}
	s.keys = map[string]int{}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// --- internals --------------------------------------------------------------

// takeFault reports what this request should do instead of succeeding, and
// consumes one use of a counted fault. A fault registered against the empty
// model id applies to every model, and the model's own fault wins over it.
func (s *Server) takeFault(model string) (string, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range []string{model, ""} {
		if mode, delay, ok := s.takeLocked(key); ok {
			return mode, delay
		}
	}
	return FaultNone, 0
}

// takeFaultExact is takeFault without the catch-all fallback, for targets that
// are not models and so must not inherit an every-model arrangement.
func (s *Server) takeFaultExact(key string) (string, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if mode, delay, ok := s.takeLocked(key); ok {
		return mode, delay
	}
	return FaultNone, 0
}

// takeLocked consumes one use of the fault at key, if there is a live one.
// Called with the lock held.
func (s *Server) takeLocked(key string) (string, time.Duration, bool) {
	f, ok := s.faults[key]
	if !ok {
		return "", 0, false
	}
	if f.remaining == 0 {
		delete(s.faults, key)
		return "", 0, false
	}
	if f.remaining > 0 {
		f.remaining--
	}
	return f.mode, f.delay, true
}

// credential identifies which of a provider's pooled keys served a request, so
// a test can prove the gateway rotated within the pool before giving up on the
// provider. Only the tail is kept: the whole point of the pool is that these
// are secrets, and a mock that echoes them back in a state dump is a mock that
// ends up in a CI log.
func credential(r *http.Request) string {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if raw == "" {
		return ""
	}
	if len(raw) > 6 {
		return "…" + raw[len(raw)-6:]
	}
	return "…" + raw
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeProviderError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": message, "type": kind},
	})
}

// ListenAddr renders a port as the address form http.Server wants, so the
// standalone binary and the tests agree on how a port becomes an address.
func ListenAddr(port int) string { return ":" + strconv.Itoa(port) }
