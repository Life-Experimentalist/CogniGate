package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/httpx"
	"github.com/cognigate/gateway/internal/store"
)

// GW-7 is the promise the gateway makes to a client that never reads its
// source: the request id is always there, the caller's own correlation id comes
// back unchanged, unknown paths fail loudly, and only a rate limit invites a
// retry. These are the unit-level halves; conformance/gw07_test.go asserts the
// same contract over the wire against a deployed gateway.

// TestGW7RequestIDIsOnEveryResponse covers GW-7.AC-2. The interesting rows are
// the ones that fail before a handler runs — an unauthenticated request and an
// unrouted path — because those are exactly the responses a caller cannot
// explain without an id to quote.
func TestGW7RequestIDIsOnEveryResponse(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, tc := range []struct {
		name   string
		method string
		path   string
		token  string
		status int
	}{
		{"success", http.MethodGet, "/v1/meta", tenant.dataKey, http.StatusOK},
		{"no credential", http.MethodGet, "/v1/meta", "", http.StatusUnauthorized},
		{"wrong plane", http.MethodGet, "/v1/meta", tenant.adminKey, http.StatusUnauthorized},
		{"unrouted path", http.MethodGet, "/v1/completions", tenant.dataKey, http.StatusNotFound},
		{"bad parameter", http.MethodGet, "/v1/usage?window=fortnight", tenant.dataKey, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(tc.method, tc.path, tc.token, nil)
			if res.status != tc.status {
				t.Fatalf("status = %d, want %d; body %s", res.status, tc.status, res.body)
			}

			id := res.header.Get(httpx.HeaderRequestID)
			if id == "" {
				t.Fatalf("no %s on the response", httpx.HeaderRequestID)
			}
			if tc.status == http.StatusOK {
				return
			}

			// The header and the body must name the same request. Two ids for one
			// failure is worse than none: a user quotes one and the operator
			// searches for the other.
			var body errorBody
			res.decode(t, &body)
			if body.Error.RequestID != id {
				t.Errorf("envelope request_id = %q, header = %q", body.Error.RequestID, id)
			}
		})
	}
}

// TestGW7UnknownEndpointsAreNotSupported covers GW-7.AC-4. The gateway's surface
// is what it documents, so an OpenAI path it does not implement has to say so
// rather than 404 like a typo or, worse, proxy through to a provider.
func TestGW7UnknownEndpointsAreNotSupported(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, path := range []string{
		"/v1/nonexistent-openai-endpoint",
		"/v1/fine_tuning/jobs",
		"/v1/images/generations",
		// Routing is case-sensitive on purpose, so this is a distinct path and
		// not a second accepted spelling of one that exists.
		"/v1/Meta",
	} {
		t.Run(path, func(t *testing.T) {
			res := h.do(http.MethodGet, path, tenant.dataKey, nil)
			h.expectError(res, http.StatusNotFound, apierr.CodeNotSupported)
		})
	}
}

// TestGW7ClientRequestIDEcho covers the echo half of GW-7.AC-3, including the
// two sanitising rules: the value is bounded, and control characters are
// removed. Both exist because the id is written to logs — an unbounded or
// unescaped one turns every request into a log-injection opportunity.
func TestGW7ClientRequestIDEcho(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	long := strings.Repeat("x", httpx.MaxClientRequestID+64)

	for _, tc := range []struct {
		name string
		sent string
		want string
	}{
		{"echoed verbatim", "abc123", "abc123"},
		{"truncated to the bound", long, strings.Repeat("x", httpx.MaxClientRequestID)},
		// A tab is a control character that survives HTTP header serialisation,
		// which makes it the one this test can actually put on the wire.
		{"control characters stripped", "abc\tdef", "abcdef"},
		{"blank is not echoed", "   ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.doWithHeaders(http.MethodGet, "/v1/meta", tenant.dataKey, nil,
				map[string]string{httpx.HeaderClientRequestID: tc.sent})
			if res.status != http.StatusOK {
				t.Fatalf("status = %d, body %s", res.status, res.body)
			}
			if got := res.header.Get(httpx.HeaderClientRequestID); got != tc.want {
				t.Errorf("echoed %q, want %q", got, tc.want)
			}
		})
	}

	// A caller that sends nothing gets nothing back, rather than an empty header
	// a client has to distinguish from a real one.
	res := h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil)
	if got := res.header.Get(httpx.HeaderClientRequestID); got != "" {
		t.Errorf("unsolicited %s = %q", httpx.HeaderClientRequestID, got)
	}
}

// TestGW7ClientRequestIDIsCorrelatableInUsage covers the storage half of
// GW-7.AC-3. Storing the id is only half a promise: a correlation id that can
// never be looked up correlates nothing, so it is readable back as a usage
// grouping.
func TestGW7ClientRequestIDIsCorrelatableInUsage(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	h.recordLabelledUsage(tenant.id, "job-42", 300, 0.02)
	h.recordLabelledUsage(tenant.id, "job-42", 200, 0.01)
	h.recordLabelledUsage(tenant.id, "job-43", 100, 0.50)
	h.recordUsage(tenant.id, 900, 0.03) // unlabelled

	var out breakdownResponse
	h.do(http.MethodGet, "/v1/usage/breakdown?group_by=client_request_id", tenant.dataKey, nil).
		decode(t, &out)

	if out.Truncated {
		t.Error("four records should not truncate")
	}
	if len(out.Data) != 2 {
		t.Fatalf("got %d buckets, want 2 (the unlabelled record must not become a bucket): %+v",
			len(out.Data), out.Data)
	}
	// Spend-descending, which is why job-43 leads on a tenth of the tokens.
	if out.Data[0].Key != "job-43" {
		t.Errorf("first bucket = %q, want the costliest (job-43)", out.Data[0].Key)
	}

	byKey := map[string]store.UsageBucket{}
	for _, b := range out.Data {
		byKey[b.Key] = b
	}
	if got := byKey["job-42"].TotalTokens; got != 500 {
		t.Errorf("job-42 total_tokens = %d, want 500 (both of its records)", got)
	}
	if got := byKey["job-42"].Requests; got != 2 {
		t.Errorf("job-42 requests = %d, want 2", got)
	}
}

// TestGW7BreakdownIsBounded guards the response size that grouping by a
// caller-supplied id makes unbounded. A tenant labelling every request with a
// distinct id would otherwise be able to ask for a month of them in one body.
func TestGW7BreakdownIsBounded(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	// Cost ascends with the index, so the rows that survive the cut are the last
	// ones written — which is the assertion that the cut falls on the cheapest.
	for i := 0; i < maxBreakdownBuckets+50; i++ {
		h.recordLabelledUsage(tenant.id, "job-"+strconv.Itoa(i), 10, float64(i))
	}

	var out breakdownResponse
	h.do(http.MethodGet, "/v1/usage/breakdown?group_by=client_request_id", tenant.dataKey, nil).
		decode(t, &out)

	if len(out.Data) != maxBreakdownBuckets {
		t.Fatalf("got %d buckets, want the cap of %d", len(out.Data), maxBreakdownBuckets)
	}
	if !out.Truncated {
		t.Error("a truncated response must say so, or it reads as a complete bill")
	}
	if want := "job-" + strconv.Itoa(maxBreakdownBuckets+49); out.Data[0].Key != want {
		t.Errorf("first bucket = %q, want the costliest %q", out.Data[0].Key, want)
	}
}

// TestGW7OnlyRateLimitsInviteRetry covers GW-7.AC-6's second half. Retry-After
// on a 400 would tell a client to send the same malformed request again, which
// is the one thing GW-7 promises it never has to do.
func TestGW7OnlyRateLimitsInviteRetry(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	for _, tc := range []struct {
		name  string
		path  string
		token string
	}{
		{"bad parameter", "/v1/usage?window=fortnight", tenant.dataKey},
		{"no credential", "/v1/meta", ""},
		{"unrouted path", "/v1/nonexistent-openai-endpoint", tenant.dataKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodGet, tc.path, tc.token, nil)
			if res.status == http.StatusTooManyRequests {
				t.Fatalf("fixture drifted: %s is a 429", tc.path)
			}
			if v := res.header.Get("Retry-After"); v != "" {
				t.Errorf("Retry-After = %q on a %d", v, res.status)
			}
		})
	}
}

// recordLabelledUsage is recordUsage with the caller's correlation id attached,
// for the GW-7 tests that need usage the id can be looked up by.
func (h *harness) recordLabelledUsage(tenantID, clientRequestID string, tokens int, costUSD float64) {
	h.t.Helper()

	err := h.mem.RecordUsage(context.Background(), &store.UsageRecord{
		RequestID:       store.NewID(store.IDRequest),
		ClientRequestID: clientRequestID,
		TenantID:        tenantID,
		KeyPrefix:       "cg-testkey",
		Provider:        "test",
		Model:           "test-small",
		TotalTokens:     tokens,
		CostUSD:         costUSD,
		StatusCode:      http.StatusOK,
		RecordedAt:      time.Now().UTC(),
	})
	if err != nil {
		h.t.Fatalf("recording labelled usage: %v", err)
	}
}
