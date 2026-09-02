package server

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/store"
)

// --- fixtures ---------------------------------------------------------------

// chatRequest is the smallest body the gateway will accept. The gateway parses
// only "model" and "stream"; everything else is here because a real client sends
// it, not because anything under test reads it.
func chatRequest(model string, stream bool) map[string]any {
	return map[string]any{
		"model":    model,
		"stream":   stream,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
}

// upstreamOK is a successful buffered exchange carrying a usage block.
func upstreamOK(promptTokens, completionTokens int) *provider.Response {
	return &provider.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       []byte(`{"object":"chat.completion","choices":[]}`),
		Failure:    provider.FailNone,
		Usage: &provider.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
		Latency: 12 * time.Millisecond,
	}
}

// upstreamFailure is a non-2xx exchange of the given class. Body matters only
// for FailClient, where the gateway passes the provider's own message through.
func upstreamFailure(status int, kind provider.FailureKind, body string) *provider.Response {
	return &provider.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       []byte(body),
		Failure:    kind,
	}
}

// upstreamStream is a well-formed SSE completion: one content chunk, one usage
// chunk, then the sentinel. relaySSE only copies, so the sentinel has to come
// from the upstream — nothing downstream of it adds one on the happy path.
func upstreamStream() *provider.Response {
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"total_tokens\":1500}}\n\n" +
		"data: [DONE]\n\n"
	return &provider.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Stream:     io.NopCloser(strings.NewReader(body)),
		Failure:    provider.FailNone,
		Latency:    12 * time.Millisecond,
	}
}

// silentStream emits its chunks and then goes quiet, which is what an upstream
// that has died mid-completion looks like from here.
//
// Read blocks until Close, because closing the body is the only lever relaySSE's
// watchdog has: there is no way to interrupt a Read already in flight. A reader
// that ignored Close would hang the test rather than exercise the stall path.
type silentStream struct {
	chunks [][]byte
	sent   int
	closed chan struct{}
	once   sync.Once
}

func newSilentStream(chunks ...string) *silentStream {
	s := &silentStream{closed: make(chan struct{})}
	for _, c := range chunks {
		s.chunks = append(s.chunks, []byte(c))
	}
	return s
}

func (s *silentStream) Read(p []byte) (int, error) {
	if s.sent < len(s.chunks) {
		n := copy(p, s.chunks[s.sent])
		s.sent++
		return n, nil
	}
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *silentStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// abortingStream emits its chunks and then fails outright, which is what an
// upstream that resets the connection mid-completion looks like from here. It
// differs from silentStream in the one way that matters: the read returns
// immediately rather than hanging, so this is a relay error rather than a stall,
// and it takes a different branch.
type abortingStream struct {
	chunks [][]byte
	sent   int
}

func newAbortingStream(chunks ...string) *abortingStream {
	s := &abortingStream{}
	for _, c := range chunks {
		s.chunks = append(s.chunks, []byte(c))
	}
	return s
}

func (s *abortingStream) Read(p []byte) (int, error) {
	if s.sent < len(s.chunks) {
		n := copy(p, s.chunks[s.sent])
		s.sent++
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (s *abortingStream) Close() error { return nil }

// routeTenant is the standard fixture for the cascade tests: one provider whose
// catalog holds both default models, and a route that chains them. Resolution
// dedupes by provider/model, so one provider and a two-element chain give
// exactly two candidates — the minimum for a cascade to be observable.
func (h *harness) routeTenant(name string) tenantFixture {
	h.t.Helper()

	tenant := h.newTenant(name)
	h.addProvider(tenant.id, "primary")
	h.putRoute(tenant.id, "test-small", "test-small", "test-large")
	return tenant
}

func (h *harness) putRoute(tenantID, match string, chain ...string) {
	h.t.Helper()

	res := h.do(http.MethodPut, "/admin/v1/tenants/"+tenantID+"/routing-rules", testBootstrapKey,
		map[string]any{"match": match, "chain": chain})
	if res.status != http.StatusOK && res.status != http.StatusCreated {
		h.t.Fatalf("creating route %q: status %d, body %s", match, res.status, res.body)
	}
}

// callLog counts upstream invocations per resolved model, which is how the
// tests that assert a candidate was *not* tried prove it. Inferring that from a
// response header would only show which candidate won, not how many were asked.
type callLog struct {
	mu     sync.Mutex
	models []string
	keys   []string
}

func (l *callLog) record(req *provider.Request, cred provider.Credential) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.models = append(l.models, req.Model)
	l.keys = append(l.keys, cred.APIKey)
}

func (l *callLog) countFor(model string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.models {
		if m == model {
			n++
		}
	}
	return n
}

func (l *callLog) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.models...)
}

func (l *callLog) credentials() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.keys...)
}

// --- GW-3 routing and cascade -----------------------------------------------

func TestChatCompletionServesThePrimaryAtDepthZero(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}

	// Served-By names the provider and the model that actually answered, which
	// for a bare model id is provider/id — the qualified form, even though the
	// caller asked with the bare one.
	if got := res.header.Get(httpx.HeaderServedBy); got != "primary/test-small" {
		t.Errorf("%s = %q, want %q", httpx.HeaderServedBy, got, "primary/test-small")
	}
	if got := res.header.Get(httpx.HeaderFallbackDepth); got != "0" {
		t.Errorf("%s = %q, want %q", httpx.HeaderFallbackDepth, got, "0")
	}
	if got := res.header.Get(httpx.HeaderQuotaState); got != httpx.QuotaOK {
		t.Errorf("%s = %q, want %q", httpx.HeaderQuotaState, got, httpx.QuotaOK)
	}
	if got := res.header.Get(httpx.HeaderRequestID); got == "" {
		t.Error("a served completion carries no request id")
	}
	if got := res.header.Get(fiberContentType); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestChatCompletionCascadesOnTransportFailure(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		if req.Model == "test-small" {
			return nil, errTestUpstreamDown
		}
		return upstreamOK(10, 5), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderServedBy); got != "primary/test-large" {
		t.Errorf("%s = %q, want the second candidate", httpx.HeaderServedBy, got)
	}
	if got := res.header.Get(httpx.HeaderFallbackDepth); got != "1" {
		t.Errorf("%s = %q, want %q", httpx.HeaderFallbackDepth, got, "1")
	}
	if calls := log.calls(); len(calls) != 2 {
		t.Errorf("upstream calls = %v, want both candidates tried once", calls)
	}
}

func TestChatCompletionCascadesOnUpstreamServerError(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if req.Model == "test-small" {
			return upstreamFailure(http.StatusBadGateway, provider.FailServer, `{"error":{"message":"upstream is sad"}}`), nil
		}
		return upstreamOK(10, 5), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderServedBy); got != "primary/test-large" {
		t.Errorf("%s = %q, want the second candidate", httpx.HeaderServedBy, got)
	}
}

// A 4xx caused by the request itself is not a provider outage. Cascading on it
// would multiply one malformed request into a call to every provider the tenant
// has, and would report the last provider's complaint instead of the first —
// which is the one that actually describes the caller's mistake.
func TestChatCompletionDoesNotCascadeOnClientError(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		return upstreamFailure(http.StatusBadRequest, provider.FailClient,
			`{"error":{"message":"temperature must be <= 2","param":"temperature","code":"invalid_request"}}`), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)

	if body.Error.Message != "temperature must be <= 2" {
		t.Errorf("message = %q, want the provider's own text", body.Error.Message)
	}
	if body.Error.Param == nil || *body.Error.Param != "temperature" {
		t.Errorf("param = %v, want %q", body.Error.Param, "temperature")
	}
	if n := log.countFor("test-large"); n != 0 {
		t.Errorf("the fallback candidate was called %d times; a client error must not cascade", n)
	}
}

// A 429 is the one failure that is worth retrying inside the same provider:
// rate limits are per credential, so another key in the pool may well be under
// its own limit. Only once the pool is exhausted is the provider itself the
// problem.
func TestChatCompletionRotatesKeysBeforeCascadingOnRateLimit(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProviderWithKeys(tenant.id, "primary", "upstream-key-1", "upstream-key-2")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		if cred.APIKey == "upstream-key-1" {
			return upstreamFailure(http.StatusTooManyRequests, provider.FailRateLimit, `{}`), nil
		}
		return upstreamOK(10, 5), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	// Still depth 0: rotating a key is not a fallback, and reporting it as one
	// would make every rate-limited moment look like a provider outage.
	if got := res.header.Get(httpx.HeaderFallbackDepth); got != "0" {
		t.Errorf("%s = %q, want %q — key rotation is not a cascade", httpx.HeaderFallbackDepth, got, "0")
	}
	if creds := log.credentials(); len(creds) != 2 || creds[0] != "upstream-key-1" || creds[1] != "upstream-key-2" {
		t.Errorf("credentials tried = %v, want both keys in pool order", creds)
	}
}

func TestChatCompletionCascadesOnceTheKeyPoolIsExhausted(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProviderWithKeys(tenant.id, "primary", "upstream-key-1", "upstream-key-2")
	h.putRoute(tenant.id, "test-small", "test-small", "test-large")

	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if req.Model == "test-small" {
			return upstreamFailure(http.StatusTooManyRequests, provider.FailRateLimit, `{}`), nil
		}
		return upstreamOK(10, 5), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderFallbackDepth); got != "1" {
		t.Errorf("%s = %q, want %q", httpx.HeaderFallbackDepth, got, "1")
	}
}

// An open breaker has to cost zero upstream calls, or it is not a circuit
// breaker: the point is to stop dialling a provider that is known to be failing,
// not to keep dialling it and discard the answer.
func TestChatCompletionSkipsCandidatesWithAnOpenBreaker(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Routing.Breaker.ErrorThreshold = 1
	})
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		if req.Model == "test-small" {
			return nil, errTestUpstreamDown
		}
		return upstreamOK(10, 5), nil
	}

	// The first request trips the breaker on primary/test-small and is served by
	// the fallback.
	if res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false)); res.status != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200 (body %s)", res.status, res.body)
	}
	before := log.countFor("test-small")
	if before == 0 {
		t.Fatal("the first request never reached the primary")
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("second request: status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderServedBy); got != "primary/test-large" {
		t.Errorf("%s = %q, want the fallback", httpx.HeaderServedBy, got)
	}
	if after := log.countFor("test-small"); after != before {
		t.Errorf("the open candidate was dialled %d more times; an open breaker must skip", after-before)
	}
}

func TestChatCompletionExhaustionIsReportedAsUpstreamExhausted(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return nil, errTestUpstreamDown
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	h.expectError(res, http.StatusBadGateway, apierr.CodeUpstreamExhausted)
}

// "All 2 routing candidates failed" is not an answer anyone can act on. The
// caller needs to know which entries were tried and how each one died, because
// a chain that died of rate limits is a billing problem, one that died of 5xx is
// the provider's, and one that never dialled at all is the gateway's own breaker
// deciding for it. GW-3.AC-5 puts that enumeration in the body.
func TestChatCompletionExhaustionEnumeratesEachAttempt(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if req.Model == "test-small" {
			return upstreamFailure(http.StatusInternalServerError, provider.FailServer, `{}`), nil
		}
		return nil, errTestUpstreamDown
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	body := h.expectError(res, http.StatusBadGateway, apierr.CodeUpstreamExhausted)

	want := []apierr.Attempt{
		{Provider: "primary", Model: "test-small", Failure: "server", Status: http.StatusInternalServerError},
		{Provider: "primary", Model: "test-large", Failure: "transport"},
	}
	if !reflect.DeepEqual(body.Error.Attempts, want) {
		t.Errorf("attempts = %+v, want %+v", body.Error.Attempts, want)
	}
}

// A candidate the breaker skipped is not a candidate that failed, and the body
// has to say which it was: an operator reading "breaker_open" knows to look at
// this gateway's recent history, and one reading "server" knows to look at the
// provider.
func TestChatCompletionExhaustionNamesSkippedCandidates(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Routing.Breaker.ErrorThreshold = 1
	})
	tenant := h.routeTenant("acme")

	// The first request trips the breaker on primary/test-small while the
	// fallback still answers, so only one of the two entries is broken.
	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if req.Model == "test-small" {
			return nil, errTestUpstreamDown
		}
		return upstreamOK(10, 5), nil
	}
	if res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false)); res.status != http.StatusOK {
		t.Fatalf("priming the breaker: status = %d, want 200 (body %s)", res.status, res.body)
	}

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return nil, errTestUpstreamDown
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	body := h.expectError(res, http.StatusBadGateway, apierr.CodeUpstreamExhausted)

	want := []apierr.Attempt{
		{Provider: "primary", Model: "test-small", Failure: "breaker_open"},
		{Provider: "primary", Model: "test-large", Failure: "transport"},
	}
	if !reflect.DeepEqual(body.Error.Attempts, want) {
		t.Errorf("attempts = %+v, want %+v", body.Error.Attempts, want)
	}
}

// The attempt list is the one error body that quotes the routing layer, which
// makes it the one most likely to grow a field it should not have. GW-3.AC-5
// forbids provider secret material and GW-14 forbids prompt content; both are
// cheap to assert and expensive to discover in production.
func TestChatCompletionExhaustionBodyLeaksNothing(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProviderWithKeys(tenant.id, "primary", "upstream-key-1", "upstream-key-2")
	h.putRoute(tenant.id, "test-small", "test-small", "test-large")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return nil, errTestUpstreamDown
	}

	// A prompt distinctive enough that finding it is unambiguous. The default
	// fixture says "ping", which is short enough to turn up inside a random
	// request id and turn this assertion flaky.
	req := chatRequest("test-small", false)
	req["messages"] = []map[string]string{{"role": "user", "content": "sentinel-prompt-body"}}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, req)
	h.expectError(res, http.StatusBadGateway, apierr.CodeUpstreamExhausted)

	for _, forbidden := range []string{
		"upstream-key-1",            // a pooled provider credential
		"upstream.invalid",          // the provider's base url
		"sentinel-prompt-body",      // the prompt, per GW-14
		errTestUpstreamDown.Error(), // the upstream's own error text
	} {
		if strings.Contains(string(res.body), forbidden) {
			t.Errorf("the error body contains %q: %s", forbidden, res.body)
		}
	}
}

// An oversize response is fatal rather than a cascade trigger: the next provider
// would be asked the same question and would answer at the same size, so the
// only thing retrying buys is more bandwidth spent on a body already refused.
func TestChatCompletionOversizeResponseDoesNotCascade(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		return nil, provider.ErrResponseTooLarge
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	h.expectError(res, http.StatusBadGateway, apierr.CodeResponseTooLarge)

	if n := log.countFor("test-large"); n != 0 {
		t.Errorf("the fallback candidate was called %d times; an oversize response is terminal", n)
	}
}

func TestChatCompletionUnknownModelIsModelNotFound(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("no-such-model", false))
	h.expectError(res, http.StatusNotFound, apierr.CodeModelNotFound)
}

// An alias that resolves to nothing is a different failure from a model that
// does not exist: the name is configured and valid, and the fix is to widen the
// alias or add a model, not to correct a typo.
func TestChatCompletionUnresolvableAliasIsDistinguished(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	// The seeded "transcribe" alias requires a transcription capability, which
	// neither default model has.
	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("transcribe", false))
	h.expectError(res, http.StatusNotFound, apierr.CodeAliasUnresolvable)
}

func TestChatCompletionRejectsMalformedRequests(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	t.Run("empty body", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, nil)
		h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	})

	t.Run("not json", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, "{not json")
		h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
	})

	t.Run("no model", func(t *testing.T) {
		res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey,
			map[string]any{"messages": []map[string]string{{"role": "user", "content": "ping"}}})
		body := h.expectError(res, http.StatusBadRequest, apierr.CodeInvalidRequest)
		if body.Error.Param == nil || *body.Error.Param != "model" {
			t.Errorf("param = %v, want %q so a client knows which field to fix", body.Error.Param, "model")
		}
	})
}

func TestChatCompletionMarksDeprecatedModels(t *testing.T) {
	h := newHarness(t)
	// A fresh slice: defaultModels is shared by every harness, and mutating its
	// backing array here would leak a deprecation into unrelated tests.
	h.adapter.models = []store.Model{{
		ID:                "test-small",
		ContextWindow:     8192,
		Capabilities:      []string{"chat"},
		InputCostPerMTok:  0.15,
		OutputCostPerMTok: 0.60,
		Deprecated:        true,
	}}

	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(httpx.HeaderDeprecation); got != "true" {
		t.Errorf("%s = %q, want %q", httpx.HeaderDeprecation, got, "true")
	}
}

// --- GW-8 metering ----------------------------------------------------------

func TestChatCompletionRecordsTokenAndCostSeries(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamOK(1000, 500), nil
	}
	if res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false)); res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}

	scrape := h.scrapeMetrics()
	// The counters TestMetricsEndpoint deliberately leaves alone, because only a
	// served completion creates a child for them.
	for _, series := range []string{
		"cognigate_tokens_total",
		"cognigate_cost_usd_total",
		"cognigate_upstream_duration_seconds",
	} {
		if !strings.Contains(scrape, series) {
			t.Errorf("/metrics does not expose %s after a served completion", series)
		}
	}
}

func TestChatCompletionRecordsTheFallbackCascadeSeries(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if req.Model == "test-small" {
			return nil, errTestUpstreamDown
		}
		return upstreamOK(10, 5), nil
	}
	if res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false)); res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}

	if scrape := h.scrapeMetrics(); !strings.Contains(scrape, "cognigate_fallback_cascades_total") {
		t.Error("/metrics does not expose cognigate_fallback_cascades_total after a cascade")
	}
}

func (h *harness) scrapeMetrics() string {
	h.t.Helper()
	res := h.do(http.MethodGet, "/metrics", "", nil)
	if res.status != http.StatusOK {
		h.t.Fatalf("/metrics status = %d, want 200", res.status)
	}
	return string(res.body)
}

// --- GW-3 streaming ---------------------------------------------------------

func TestStreamedCompletionRelaysEventsVerbatim(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(_ context.Context, _ provider.Credential, req *provider.Request) (*provider.Response, error) {
		if !req.Stream {
			t.Errorf("a streamed request reached the adapter with Stream = false")
		}
		return upstreamStream(), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", true))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}
	if got := res.header.Get(fiberContentType); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := res.header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	// Without this, any buffering proxy in front of the gateway holds the whole
	// completion and streaming silently stops being streaming.
	if got := res.header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
	if got := res.header.Get(httpx.HeaderServedBy); got != "primary/test-small" {
		t.Errorf("%s = %q, want %q", httpx.HeaderServedBy, got, "primary/test-small")
	}

	body := string(res.body)
	if !strings.Contains(body, `"delta"`) {
		t.Errorf("the content chunk did not reach the client: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the stream did not end with the sentinel: %s", body)
	}
}

// A stall cannot be an HTTP status: the 200 went out with the first byte. GW-7
// gives it an in-band terminal event for exactly that reason.
func TestStreamedCompletionReportsAStallInBand(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Limits.StreamIdleTimeout = 100 * time.Millisecond
	})
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		// One chunk first, so this is a mid-stream stall rather than a failure
		// before the first byte — which is a different path with a different
		// answer.
		return &provider.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Stream:     newSilentStream("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n"),
			Failure:    provider.FailNone,
		}, nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", true))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the status line is long gone by the time a stall is detected", res.status)
	}

	body := string(res.body)
	if !strings.Contains(body, "event: error") {
		t.Errorf("no terminal error event in the stream: %s", body)
	}
	if !strings.Contains(body, apierr.CodeUpstreamStreamStalled) {
		t.Errorf("the terminal event does not carry %s: %s", apierr.CodeUpstreamStreamStalled, body)
	}
	// The sentinel still goes out, so a client looping until [DONE] terminates
	// instead of hanging on an error it was never told about.
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("a stalled stream did not end with the sentinel: %s", body)
	}
}

// A provider that dies mid-stream leaves the caller holding a truncated answer.
// Ending the response quietly makes that indistinguishable from a completion
// that simply finished, so the client would treat half a sentence as the whole
// one; GW-3.AC-7 requires the stream to say so on its way out. Falling back is
// not an option this late — the first byte has shipped, and continuing with
// another model's output would splice two completions into one.
func TestStreamedCompletionReportsAMidStreamAbortInBand(t *testing.T) {
	h := newHarness(t)
	tenant := h.routeTenant("acme")

	var log callLog
	h.adapter.do = func(_ context.Context, cred provider.Credential, req *provider.Request) (*provider.Response, error) {
		log.record(req, cred)
		return &provider.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Stream:     newAbortingStream("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n"),
			Failure:    provider.FailNone,
		}, nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", true))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the status line went out with the first chunk", res.status)
	}

	body := string(res.body)
	if !strings.Contains(body, `"delta"`) {
		t.Fatalf("the content chunk never reached the client, so this is not a mid-stream abort: %s", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Errorf("no terminal error event after the upstream died: %s", body)
	}
	if !strings.Contains(body, apierr.CodeUpstreamError) {
		t.Errorf("the terminal event does not carry %s: %s", apierr.CodeUpstreamError, body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("an aborted stream did not end with the sentinel: %s", body)
	}
	if n := log.countFor("test-large"); n != 0 {
		t.Errorf("the fallback candidate was called %d times; a stream cannot fall back after the first byte", n)
	}
}

// Before the first byte there is no stream yet, so a failure is an ordinary HTTP
// error. Answering it as SSE would force every client to parse an event stream
// to discover its request was rejected.
func TestStreamedCompletionFailsAsJSONBeforeTheFirstByte(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return nil, errTestUpstreamDown
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", true))
	h.expectError(res, http.StatusBadGateway, apierr.CodeUpstreamExhausted)

	if got := res.header.Get(fiberContentType); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// GW-4 accounting has to cover the streaming path or it covers almost nothing:
// streaming is how most chat traffic is served, and an unattributed usage row is
// invisible to /v1/usage, to quota enforcement, and to billing alike.
func TestStreamedCompletionIsAttributedToItsTenant(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamStream(), nil
	}

	res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", true))
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}

	h.flushTelemetry()

	totals, err := h.mem.Usage(context.Background(), tenant.id,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("reading usage: %v", err)
	}
	if totals.Requests != 1 {
		t.Fatalf("usage requests = %d, want 1 — the streamed row was not attributed to the tenant", totals.Requests)
	}
	if totals.PromptTokens != 1000 || totals.CompletionTokens != 500 {
		t.Errorf("usage tokens = %d/%d, want 1000/500 from the stream's usage chunk",
			totals.PromptTokens, totals.CompletionTokens)
	}
	// 1000 prompt tokens at $0.15/MTok plus 500 completion at $0.60/MTok.
	if want := 0.00045; totals.CostUSD < want*0.999 || totals.CostUSD > want*1.001 {
		t.Errorf("usage cost = %v, want %v", totals.CostUSD, want)
	}
}

func TestBufferedCompletionIsAttributedToItsTenant(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	h.addProvider(tenant.id, "primary")

	h.adapter.do = func(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
		return upstreamOK(1000, 500), nil
	}
	if res := h.do(http.MethodPost, "/v1/chat/completions", tenant.dataKey, chatRequest("test-small", false)); res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", res.status, res.body)
	}

	h.flushTelemetry()

	totals, err := h.mem.Usage(context.Background(), tenant.id,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("reading usage: %v", err)
	}
	if totals.Requests != 1 || totals.TotalTokens != 1500 {
		t.Errorf("usage = %d requests / %d tokens, want 1 / 1500", totals.Requests, totals.TotalTokens)
	}
}

// fiberContentType is spelled out rather than imported so the test file does not
// depend on the framework for a header name every HTTP client already knows.
const fiberContentType = "Content-Type"
