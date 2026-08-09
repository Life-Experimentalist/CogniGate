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
)

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
}

// SeedModels are present on every fresh mock. The ids are chosen for what the
// gateway's inferCapabilities makes of them: "embed" yields embeddings only,
// "transcribe" yields transcription only, "vision" adds vision to chat, and
// anything else is chat plus tools. That spread is what the GW-2 alias tests
// need in order to distinguish capability filtering from ordering.
func SeedModels() []Model {
	return []Model{
		{ID: "mock-chat-a", OwnedBy: "mock", ContextWindow: 128000, MaxOutputTokens: 4096},
		{ID: "mock-chat-b", OwnedBy: "mock", ContextWindow: 32000, MaxOutputTokens: 4096},
		{ID: "mock-vision", OwnedBy: "mock", ContextWindow: 128000, MaxOutputTokens: 4096},
		{ID: "mock-embed", OwnedBy: "mock", ContextWindow: 8192},
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

	s.mu.Lock()
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
		case FaultTimeout:
			// Honour cancellation so a cleared-late fault does not pin a
			// goroutine for the whole delay after the client has gone.
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
			}
			return
		}
	}

	s.mu.Lock()
	s.requests[req.Model]++
	if cred := credential(r); cred != "" {
		s.keys[cred]++
	}
	s.mu.Unlock()

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

func (s *Server) streamCompletion(w http.ResponseWriter, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProviderError(w, http.StatusInternalServerError, "server_error", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	chunk := func(payload map[string]any) {
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
	}

	base := func(delta map[string]any, finish any) map[string]any {
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

	chunk(base(map[string]any{"role": "assistant"}, nil))
	chunk(base(map[string]any{"content": completionText}, nil))
	chunk(base(map[string]any{}, "stop"))

	// A usage-bearing final chunk, as an upstream sends when the caller asked
	// for stream_options.include_usage. It is sent unconditionally: the
	// gateway has to account for a streamed request either way, and an
	// upstream that volunteers usage is the easier case to be correct about.
	final := base(map[string]any{}, nil)
	final["choices"] = []any{}
	final["usage"] = usageBlock()
	chunk(final)

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
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
	case FaultNone, FaultRateLimit, FaultServerError, FaultTimeout:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("unknown fault mode %q", req.Mode)})
		return
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
		f, ok := s.faults[key]
		if !ok {
			continue
		}
		if f.remaining == 0 {
			delete(s.faults, key)
			continue
		}
		if f.remaining > 0 {
			f.remaining--
		}
		return f.mode, f.delay
	}
	return FaultNone, 0
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
