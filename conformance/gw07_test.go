package conformance

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-7: the promise a client can build on without reading the gateway's source
// — an OpenAI-shaped body, an id on every response, the caller's own id echoed
// back, unknown paths that fail loudly, and exactly one kind of error that
// invites a retry.
//
// GW-7 is unconditional: every deployment claims this contract, because it is
// the contract. Where a criterion needs a capability the deployment may not
// have (aliases, quotas), that half gates on the capability and the rest still
// runs.

// TestGW7_AC1_AStockOpenAIClientCompletes asserts the wire contract a stock
// OpenAI SDK depends on, in both modes.
//
// The suite cannot import an SDK — it is standard library only, on purpose, so
// that it builds anywhere a Go toolchain does and so nothing it asserts can
// drift with a vendor's release. What it asserts instead is every field those
// SDKs decode into their response types: a gateway that satisfies this is one
// they parse, and a gateway that omits `object` or types `usage.total_tokens`
// as a string fails here rather than in a downstream traceback.
func TestGW7_AC1_AStockOpenAIClientCompletes(t *testing.T) {
	begin(t)

	// The criterion says "against an alias model name", which is the shape a
	// client is meant to use (GW-2). A deployment that does not claim aliases
	// still has to satisfy the compatibility contract, so it is exercised
	// against a concrete model rather than skipped.
	model := "mock-chat-a"
	if suite.features["aliases"] {
		model = uniqueName("gw7-ac1")
		putAlias(t, suite.tenantID, model, map[string]any{"pin": "mock-chat-a"})
	}

	t.Run("non-streaming", func(t *testing.T) {
		resp := chat(t, suite.dataKey, model)
		if resp.Status != http.StatusOK {
			t.Fatalf("a completion against %q: status %d, want 200\n%s",
				model, resp.Status, truncate(resp.Body))
		}
		body := resp.JSON(t)

		if got, _ := body["object"].(string); got != "chat.completion" {
			t.Errorf("object = %v, want %q", body["object"], "chat.completion")
		}
		for _, field := range []string{"id", "model"} {
			if s, _ := body[field].(string); s == "" {
				t.Errorf("%s = %v, want a non-empty string", field, body[field])
			}
		}

		choices, _ := body["choices"].([]any)
		if len(choices) == 0 {
			t.Fatalf("choices is empty\n%s", truncate(resp.Body))
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			t.Fatalf("choices[0] is not an object\n%s", truncate(resp.Body))
		}
		if s, _ := choice["finish_reason"].(string); s == "" {
			t.Errorf("choices[0].finish_reason = %v, want a non-empty string", choice["finish_reason"])
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			t.Fatalf("choices[0].message is not an object\n%s", truncate(resp.Body))
		}
		if role, _ := message["role"].(string); role != "assistant" {
			t.Errorf("choices[0].message.role = %v, want %q", message["role"], "assistant")
		}
		// Content may legitimately be empty, but it has to be a string: an SDK
		// decoding into Optional[str] breaks on a number or an object.
		if _, ok := message["content"].(string); !ok && message["content"] != nil {
			t.Errorf("choices[0].message.content = %v, want a string or null", message["content"])
		}

		usage, ok := body["usage"].(map[string]any)
		if !ok {
			t.Fatalf("usage is not an object\n%s", truncate(resp.Body))
		}
		for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
			if _, ok := usage[field].(float64); !ok {
				t.Errorf("usage.%s = %v, want a number", field, usage[field])
			}
		}
	})

	t.Run("streaming", func(t *testing.T) {
		stream := chatStream(t, suite.dataKey, model)
		if stream.Status != http.StatusOK {
			t.Fatalf("a streaming completion against %q: status %d, want 200", model, stream.Status)
		}
		if ct := stream.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
		if len(stream.Frames) == 0 {
			t.Fatalf("the stream carried no data frames (read error: %v)", stream.Err)
		}
		// Without the sentinel an SDK's iterator does not know the stream ended
		// cleanly, and reports a truncated response as a completed one.
		if stream.Frames[len(stream.Frames)-1] != "[DONE]" {
			t.Errorf("the last frame is %q, want the [DONE] sentinel",
				stream.Frames[len(stream.Frames)-1])
		}

		var chunks int
		for i, frame := range stream.Frames {
			if frame == "[DONE]" {
				continue
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(frame), &chunk); err != nil {
				t.Fatalf("frame %d is not JSON: %v\n%s", i, err, frame)
			}
			if got, _ := chunk["object"].(string); got != "chat.completion.chunk" {
				t.Errorf("frame %d object = %v, want %q", i, chunk["object"], "chat.completion.chunk")
			}
			choices, _ := chunk["choices"].([]any)
			if len(choices) == 0 {
				// One chunk legitimately carries no choices: the usage summary an
				// SDK reads off the end of the stream. Any other empty one is a
				// frame the SDK's iterator yields with nothing in it.
				if _, ok := chunk["usage"].(map[string]any); !ok {
					t.Errorf("frame %d has neither choices nor usage\n%s", i, frame)
				}
				continue
			}
			if choice, ok := choices[0].(map[string]any); ok {
				if _, ok := choice["delta"]; !ok {
					t.Errorf("frame %d choices[0] has no delta\n%s", i, frame)
				}
			}
			chunks++
		}
		if chunks == 0 {
			t.Error("the stream carried only the sentinel, so nothing was actually streamed")
		}
	})
}

// TestGW7_AC2_EveryResponseCarriesTheRequestID covers the one string a user can
// quote about a request. The rows that matter are the failures: a request
// rejected before any handler ran, and a path that matched no route, are
// precisely the responses nobody can explain without an id.
func TestGW7_AC2_EveryResponseCarriesTheRequestID(t *testing.T) {
	c := begin(t)

	// A 5xx has to be provoked rather than found. The fault is on a model of
	// this test's own, so no other test's traffic goes through it.
	failing := addMockModel(t, uniqueName("gw7-ac2"))
	injectFault(t, failing, mockprovider.FaultServerError, mockprovider.ForeverCount)

	for _, tc := range []struct {
		name   string
		send   func() *response
		status int
	}{
		{"200", func() *response {
			return c.data(t, http.MethodGet, "/v1/meta", nil)
		}, http.StatusOK},
		{"401 no credential", func() *response {
			return c.do(t, http.MethodGet, "/v1/meta", "", nil)
		}, http.StatusUnauthorized},
		{"404 unrouted", func() *response {
			return c.data(t, http.MethodGet, "/v1/nonexistent-openai-endpoint", nil)
		}, http.StatusNotFound},
		{"400 bad parameter", func() *response {
			return c.data(t, http.MethodGet, "/v1/usage?window=fortnight", nil)
		}, http.StatusBadRequest},
		{"502 upstream", func() *response {
			return chat(t, suite.dataKey, failing)
		}, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.send()
			if resp.Status != tc.status {
				t.Fatalf("status %d, want %d\n%s", resp.Status, tc.status, truncate(resp.Body))
			}

			id := resp.Header.Get(headerRequestID)
			if id == "" {
				t.Fatalf("no %s on the response\n%s", headerRequestID, truncate(resp.Body))
			}
			if resp.Status == http.StatusOK {
				return
			}

			// Two ids for one failure is worse than none: the user quotes one and
			// the operator searches the logs for the other.
			envelope, _ := resp.JSON(t)["error"].(map[string]any)
			if envelope == nil {
				t.Fatalf("an error response with no envelope\n%s", truncate(resp.Body))
			}
			if got, _ := envelope["request_id"].(string); got != id {
				t.Errorf("error.request_id = %v, header %s = %q", envelope["request_id"], headerRequestID, id)
			}
		})
	}
}

// TestGW7_AC3_TheClientsOwnIDIsEchoedAndStored covers both halves of the
// correlation contract. The echo alone is not the criterion: an id a caller can
// send and never look up again correlates nothing, which is why the stored
// value is read back through the usage API rather than trusted.
func TestGW7_AC3_TheClientsOwnIDIsEchoedAndStored(t *testing.T) {
	begin(t)

	label := uniqueName("gw7-ac3")
	resp := chatWithHeaders(t, suite.dataKey, "mock-chat-a",
		map[string]string{headerClientRequestID: label})
	if resp.Status != http.StatusOK {
		t.Fatalf("a labelled completion: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}
	if got := resp.Header.Get(headerClientRequestID); got != label {
		t.Fatalf("%s echoed as %q, want %q — verbatim is the whole contract",
			headerClientRequestID, got, label)
	}

	report := awaitBreakdown(t, suite.dataKey, "day", "client_request_id",
		func(r breakdownReport) bool {
			for _, row := range r.Data {
				if row.Key == label {
					return true
				}
			}
			return r.Truncated // a bounded response cannot prove absence
		},
		"the label "+label+" this run attached to a completion")

	if report.Truncated {
		t.Skipf("the deployment's breakdown is truncated, so this run cannot tell "+
			"a missing %s from one past the bound", headerClientRequestID)
	}
	for _, row := range report.Data {
		if row.Key != label {
			continue
		}
		if row.Requests < 1 {
			t.Errorf("the stored record for %q reports %d requests, want at least 1", label, row.Requests)
		}
		if row.TotalTokens < 1 {
			t.Errorf("the stored record for %q reports %d tokens, want the completion's",
				label, row.TotalTokens)
		}
	}

	// The bound is part of the contract, not an implementation detail: a caller
	// that sends more must be told what was kept rather than have the request
	// rejected or the log poisoned.
	t.Run("bounded", func(t *testing.T) {
		long := strings.Repeat("z", maxClientRequestID+64)
		resp := chatWithHeaders(t, suite.dataKey, "mock-chat-a",
			map[string]string{headerClientRequestID: long})
		if resp.Status != http.StatusOK {
			t.Fatalf("an over-long label must not fail the request: status %d\n%s",
				resp.Status, truncate(resp.Body))
		}
		got := resp.Header.Get(headerClientRequestID)
		if len(got) > maxClientRequestID {
			t.Errorf("echoed %d characters, want at most %d", len(got), maxClientRequestID)
		}
		if got != "" && !strings.HasPrefix(long, got) {
			t.Errorf("the echoed value %q is not a prefix of what was sent", got)
		}
	})
}

// TestGW7_AC4_AnUnknownEndpointIsNotSupported is the criterion that keeps the
// gateway's surface equal to what it documents. The alternative — proxying an
// unrecognised path through to whatever the upstream implements — produces a
// gateway that cannot be versioned or conformance-tested at all.
func TestGW7_AC4_AnUnknownEndpointIsNotSupported(t *testing.T) {
	c := begin(t)

	for _, path := range []string{
		"/v1/nonexistent-openai-endpoint",
		"/v1/fine_tuning/jobs",
		"/v1/images/generations",
	} {
		t.Run(path, func(t *testing.T) {
			resp := c.data(t, http.MethodGet, path, nil)
			if resp.Status != http.StatusNotFound {
				t.Fatalf("GET %s: status %d, want 404\n%s", path, resp.Status, truncate(resp.Body))
			}
			if code := resp.ErrorCode(t); code != "not_supported" {
				t.Errorf("error.code = %q, want %q\n%s", code, "not_supported", truncate(resp.Body))
			}
			assertEnvelope(t, resp)
		})
	}
}

// TestGW7_AC5_EveryErrorParsesAsOpenAIShaped walks the registry's codes and
// checks the shape rather than the message. A stock SDK decodes the envelope
// into a fixed set of fields; one error that types `param` as an object, or
// omits `type`, raises inside the SDK's own error handling — which is the worst
// place for a gateway failure to surface.
func TestGW7_AC5_EveryErrorParsesAsOpenAIShaped(t *testing.T) {
	c := begin(t)

	failing := addMockModel(t, uniqueName("gw7-ac5"))
	injectFault(t, failing, mockprovider.FaultServerError, mockprovider.ForeverCount)

	for _, tc := range []struct {
		name   string
		send   func() *response
		status int
		code   string
	}{
		{"invalid_api_key", func() *response {
			return c.do(t, http.MethodGet, "/v1/meta", "cg-nosuchkeyatall", nil)
		}, http.StatusUnauthorized, "invalid_api_key"},
		{"wrong_plane", func() *response {
			return c.do(t, http.MethodPost, "/v1/chat/completions", suite.cfg.AdminKey, map[string]any{
				"model":    "mock-chat-a",
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			})
		}, http.StatusUnauthorized, "wrong_plane"},
		{"not_supported", func() *response {
			return c.data(t, http.MethodGet, "/v1/nonexistent-openai-endpoint", nil)
		}, http.StatusNotFound, "not_supported"},
		{"invalid_request", func() *response {
			return c.data(t, http.MethodGet, "/v1/usage?window=fortnight", nil)
		}, http.StatusBadRequest, "invalid_request"},
		{"model_not_found", func() *response {
			return chat(t, suite.dataKey, uniqueName("gw7-ac5-absent"))
		}, http.StatusNotFound, "model_not_found"},
		{"upstream_exhausted", func() *response {
			return chat(t, suite.dataKey, failing)
		}, http.StatusBadGateway, "upstream_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.send()
			if resp.Status != tc.status {
				t.Fatalf("status %d, want %d\n%s", resp.Status, tc.status, truncate(resp.Body))
			}
			if code := resp.ErrorCode(t); code != tc.code {
				t.Errorf("error.code = %q, want %q\n%s", code, tc.code, truncate(resp.Body))
			}
			assertEnvelope(t, resp)
		})
	}
}

// TestGW7_AC6_OnlyRateLimitsInviteARetry covers the header a client's backoff
// logic keys on. Retry-After on a 400 would tell a client to send the same
// malformed request again, which is the one thing GW-7 promises it never has to
// do; a 429 without one leaves it guessing, and it will guess badly.
func TestGW7_AC6_OnlyRateLimitsInviteARetry(t *testing.T) {
	c := begin(t)

	t.Run("4xx does not", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			send func() *response
		}{
			{"400", func() *response {
				return c.data(t, http.MethodGet, "/v1/usage?window=fortnight", nil)
			}},
			{"401", func() *response {
				return c.do(t, http.MethodGet, "/v1/meta", "cg-nosuchkeyatall", nil)
			}},
			{"404", func() *response {
				return c.data(t, http.MethodGet, "/v1/nonexistent-openai-endpoint", nil)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp := tc.send()
				if resp.Status == http.StatusTooManyRequests {
					t.Fatalf("fixture drifted: this case is a 429\n%s", truncate(resp.Body))
				}
				if v := resp.Header.Get("Retry-After"); v != "" {
					t.Errorf("Retry-After = %q on a %d, which invites a retry that must not happen",
						v, resp.Status)
				}
			})
		}
	})

	// The 429 half needs a deployment that actually rejects on quota. The other
	// half above still runs everywhere, because "a 400 must not carry
	// Retry-After" is true of every gateway.
	t.Run("429 does", func(t *testing.T) {
		requireFeature(t, "quotas")
		requireEnforcement(t, true)

		tn := quotaTenant(t, "gw7-ac6")
		// Below one completion, so the first request is admitted at zero usage and
		// puts the tenant over — the overshoot GW-4 accepts by design.
		putQuota(t, tn.ID, map[string]any{"day": tokenCap(10, 80)})

		resp := awaitChat(t, tn.Key, "mock-chat-a",
			func(r *response) bool { return r.Status == http.StatusTooManyRequests },
			"was rejected for the day token cap")

		if code := resp.ErrorCode(t); code != "quota_exceeded" && code != "budget_exceeded" {
			t.Errorf("error.code = %q, want quota_exceeded or budget_exceeded\n%s",
				code, truncate(resp.Body))
		}
		retry := resp.Header.Get("Retry-After")
		if retry == "" {
			t.Fatal("a 429 with no Retry-After leaves the client to guess when the window resets")
		}
		if seconds, err := strconv.Atoi(retry); err != nil || seconds < 1 {
			t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
		}
		assertEnvelope(t, resp)
	})
}

// assertEnvelope checks the four fields a stock OpenAI SDK decodes, and their
// types. It deliberately does not require the envelope to carry *only* those:
// CogniGate adds request_id and attempts, and an SDK ignores keys it does not
// know — the failure mode is a missing or wrongly-typed field, not an extra one.
func assertEnvelope(t *testing.T, resp *response) {
	t.Helper()

	body := resp.JSON(t)
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("an error response with no error object\n%s", truncate(resp.Body))
	}
	for _, field := range []string{"message", "type", "code"} {
		if s, _ := envelope[field].(string); s == "" {
			t.Errorf("error.%s = %v, want a non-empty string\n%s",
				field, envelope[field], truncate(resp.Body))
		}
	}
	// param is optional and nullable, but when present it names a field, so a
	// non-string is a decode failure inside the SDK rather than in the caller.
	if raw, present := envelope["param"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			t.Errorf("error.param = %v, want a string or null\n%s", raw, truncate(resp.Body))
		}
	}
}
