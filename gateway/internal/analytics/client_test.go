package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/config"
	"github.com/cognigate/gateway/internal/store"
)

var (
	since = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
)

// capture records what the client sent, so a test can assert on the request
// rather than only on what came back.
type capture struct {
	method string
	path   string
	query  map[string][]string
	header http.Header
	body   []byte
}

// serve stands up an analytics service that answers every request the same way,
// and returns a client pointed at it plus the last request it saw.
func serve(t *testing.T, status int, body string) (*Client, *capture) {
	t.Helper()
	return serveAt(t, "", status, body)
}

// serveAt is serve, with the endpoint the client is configured with written as
// a suffix on the test server's own URL.
func serveAt(t *testing.T, suffix string, status int, body string) (*Client, *capture) {
	t.Helper()
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.Query()
		got.header = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(config.Analytics{BaseURL: srv.URL + suffix, Token: "sekrit"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, got
}

func TestNewClientRejectsUnusableEndpoints(t *testing.T) {
	// A typo must stop the process at startup, not become a warning on the
	// telemetry path that nobody reads until the invoice is wrong.
	cases := map[string]string{
		"no scheme":      "analytics:8081",
		"wrong scheme":   "ftp://analytics:8081",
		"no host":        "http:///api",
		"not a url":      "http://[::1",
		"empty":          "",
		"scheme only":    "https://",
		"bare host name": "analytics.internal",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(config.Analytics{BaseURL: raw}); err == nil {
				t.Fatalf("NewClient(%q) accepted an unusable endpoint", raw)
			}
		})
	}
}

func TestNewClientDefaultsTheTimeout(t *testing.T) {
	c, err := NewClient(config.Analytics{BaseURL: "http://analytics:8081"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Without a timeout a stalled analytics service would hold a telemetry
	// goroutine open forever, and the retry loop would never get to run.
	if c.hc.Timeout <= 0 {
		t.Fatalf("client has no timeout")
	}
}

func TestNewClientTrimsATrailingSlash(t *testing.T) {
	// A trailing slash is what a hand-written ANALYTICS_URL usually carries, and
	// it must not turn every path into a double-slashed one the service 404s.
	client, got := serveAt(t, "/", http.StatusCreated, "")

	if err := client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "r1"}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if got.path != "/api/v1/usage" {
		t.Fatalf("path = %q, want /api/v1/usage", got.path)
	}
}

func TestRecordUsageSendsTheWholeRecord(t *testing.T) {
	client, got := serve(t, http.StatusCreated, "")

	rec := store.UsageRecord{
		RequestID:       "req_01",
		ClientRequestID: "caller-abc",
		TenantID:        "tnt_dev",
		KeyPrefix:       "cg-dev-abcd",
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		RequestedModel:  "fast",
		FallbackDepth:   1,
		PromptTokens:    15,
		CompletionToken: 20,
		TotalTokens:     35,
		CostUSD:         0.00042,
		Streamed:        true,
		StatusCode:      200,
		DurationMS:      812,
		RecordedAt:      since,
	}
	if err := client.RecordUsage(context.Background(), &rec); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	if got.method != http.MethodPost || got.path != "/api/v1/usage" {
		t.Fatalf("sent %s %s, want POST /api/v1/usage", got.method, got.path)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if auth := got.header.Get("Authorization"); auth != "Bearer sekrit" {
		t.Fatalf("Authorization = %q, want the configured token", auth)
	}

	var sent map[string]any
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("the service could not parse what was sent: %v", err)
	}
	// Every primitive must be on the wire even at its zero value: the receiver
	// refuses a record whose numeric fields are absent, and a refusal is a 4xx,
	// which the gateway drops rather than retries.
	for _, field := range []string{
		"request_id", "client_request_id", "tenant_id", "key_prefix", "provider",
		"model", "requested_model", "fallback_depth", "prompt_tokens",
		"completion_tokens", "total_tokens", "cost_usd", "cached", "streamed",
		"status_code", "duration_ms", "recorded_at",
	} {
		if _, ok := sent[field]; !ok {
			t.Errorf("%s was not sent", field)
		}
	}
	if sent["completion_tokens"] != float64(20) {
		t.Errorf("completion_tokens = %v, want 20", sent["completion_tokens"])
	}
	if sent["cached"] != false {
		t.Errorf("cached = %v, want false", sent["cached"])
	}
}

func TestRecordUsageTreatsAlreadyStoredAsDelivered(t *testing.T) {
	// The service answers 200 when it already holds the record. A retry that
	// discovers its own earlier write has delivered it; calling that a failure
	// would wedge the queue behind something already stored.
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent} {
		client, _ := serve(t, status, "")
		err := client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "req_01"})
		if err != nil {
			t.Errorf("status %d: RecordUsage: %v", status, err)
		}
	}
}

func TestRecordUsageOmitsTheHeaderWhenNoTokenIsSet(t *testing.T) {
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client, err := NewClient(config.Analytics{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "r"}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if auth := got.header.Get("Authorization"); auth != "" {
		t.Fatalf("Authorization = %q, want no header at all", auth)
	}
}

func TestStatusErrorSeparatesPermanentFromTransient(t *testing.T) {
	cases := []struct {
		status    int
		permanent bool
	}{
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusNotFound, true},
		{http.StatusUnsupportedMediaType, true},
		// A moment, not a request: replaying these can succeed.
		{http.StatusRequestTimeout, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusGatewayTimeout, false},
	}
	for _, c := range cases {
		e := &StatusError{Op: "POST /api/v1/usage", Status: c.status}
		if got := e.Permanent(); got != c.permanent {
			t.Errorf("status %d: Permanent() = %v, want %v", c.status, got, c.permanent)
		}
	}
}

func TestRecordUsageReportsAnUnusableStatusAsAStatusError(t *testing.T) {
	client, _ := serve(t, http.StatusBadRequest, `{"error":"recorded_at is required."}`)

	err := client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "req_01"})
	if err == nil {
		t.Fatal("RecordUsage accepted a 400")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T, want a *StatusError the retry loop can classify", err)
	}
	if se.Status != http.StatusBadRequest || !se.Permanent() {
		t.Fatalf("status %d, permanent %v", se.Status, se.Permanent())
	}
	// The service's own message is what tells an operator which field was wrong.
	if !strings.Contains(err.Error(), "recorded_at is required.") {
		t.Fatalf("error text lost the service's message: %v", err)
	}
}

func TestErrorBodiesAreBounded(t *testing.T) {
	// A proxy in front of a stopped analytics service answers with an HTML page.
	// It must not end up in the log in full.
	client, _ := serve(t, http.StatusBadGateway, strings.Repeat("x", 8000))

	err := client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "r"})
	if err == nil {
		t.Fatal("RecordUsage accepted a 502")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T", err)
	}
	if len(se.Body) > maxErrorBody {
		t.Fatalf("quoted %d bytes of the response, want at most %d", len(se.Body), maxErrorBody)
	}
}

func TestUsageRequestsTheWindowAsRFC3339(t *testing.T) {
	client, got := serve(t, http.StatusOK,
		`{"requests":3,"prompt_tokens":30,"completion_tokens":45,"total_tokens":75,"cost_usd":0.015}`)

	totals, err := client.Usage(context.Background(), "tnt_dev", since, until)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	if got.method != http.MethodGet || got.path != "/api/v1/usage/totals" {
		t.Fatalf("sent %s %s", got.method, got.path)
	}
	if q := got.query["tenant_id"]; len(q) != 1 || q[0] != "tnt_dev" {
		t.Fatalf("tenant_id = %v", q)
	}
	if q := got.query["since"]; len(q) != 1 || q[0] != "2026-03-01T00:00:00Z" {
		t.Fatalf("since = %v, want an RFC3339 instant in UTC", q)
	}
	if q := got.query["until"]; len(q) != 1 || q[0] != "2026-03-02T00:00:00Z" {
		t.Fatalf("until = %v", q)
	}
	if _, ok := got.query["key_prefix"]; ok {
		t.Fatalf("a tenant-wide read narrowed itself to a key")
	}

	want := store.UsageTotals{Requests: 3, PromptTokens: 30, CompletionTokens: 45,
		TotalTokens: 75, CostUSD: 0.015}
	if totals != want {
		t.Fatalf("totals = %+v, want %+v", totals, want)
	}
}

func TestUsageSendsTheWindowInUTCWhateverTheCallerHeld(t *testing.T) {
	client, got := serve(t, http.StatusOK, `{}`)

	// The two sides must not be able to disagree about which day a record fell
	// in, so an offset the caller happened to carry is normalised away.
	kolkata := time.FixedZone("IST", 5*3600+1800)
	if _, err := client.Usage(context.Background(), "t", since.In(kolkata), until); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if q := got.query["since"]; len(q) != 1 || q[0] != "2026-03-01T00:00:00Z" {
		t.Fatalf("since = %v, want the same instant rendered in UTC", q)
	}
}

func TestKeyUsageNarrowsToOneKey(t *testing.T) {
	client, got := serve(t, http.StatusOK, `{"requests":1,"total_tokens":20}`)

	totals, err := client.KeyUsage(context.Background(), "tnt_dev", "cg-dev-abcd", since, until)
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	if q := got.query["key_prefix"]; len(q) != 1 || q[0] != "cg-dev-abcd" {
		t.Fatalf("key_prefix = %v", q)
	}
	if totals.Requests != 1 || totals.TotalTokens != 20 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestUsageBreakdownKeepsTheServicesOrder(t *testing.T) {
	client, got := serve(t, http.StatusOK, `[
		{"key":"gpt-4o","requests":2,"prompt_tokens":20,"completion_tokens":30,"total_tokens":50,"cost_usd":0.4},
		{"key":"gpt-4o-mini","requests":9,"prompt_tokens":90,"completion_tokens":90,"total_tokens":180,"cost_usd":0.1}
	]`)

	rows, err := client.UsageBreakdown(context.Background(), "tnt_dev", since, until, "model")
	if err != nil {
		t.Fatalf("UsageBreakdown: %v", err)
	}
	if q := got.query["group_by"]; len(q) != 1 || q[0] != "model" {
		t.Fatalf("group_by = %v", q)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Most expensive first, which is not most-used first — re-sorting here would
	// undo the ordering the endpoint exists to provide.
	if rows[0].Key != "gpt-4o" || rows[1].Key != "gpt-4o-mini" {
		t.Fatalf("rows came back as %q, %q", rows[0].Key, rows[1].Key)
	}
	if rows[0].CostUSD != 0.4 || rows[0].TotalTokens != 50 {
		t.Fatalf("row = %+v, want the embedded totals filled in", rows[0])
	}
}

func TestUsageBreakdownOnAnEmptyWindowIsEmpty(t *testing.T) {
	client, _ := serve(t, http.StatusOK, `[]`)

	rows, err := client.UsageBreakdown(context.Background(), "tnt_dev", since, until, "provider")
	if err != nil {
		t.Fatalf("UsageBreakdown: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want none", len(rows))
	}
}

func TestAResponseThatIsNotJSONIsAnError(t *testing.T) {
	client, _ := serve(t, http.StatusOK, "<html>hello from the load balancer</html>")

	if _, err := client.Usage(context.Background(), "tnt_dev", since, until); err == nil {
		t.Fatal("Usage accepted a body it could not decode")
	}
}

func TestAnUnreachableServiceIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client, err := NewClient(config.Analytics{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close()

	err = client.RecordUsage(context.Background(), &store.UsageRecord{RequestID: "r"})
	if err == nil {
		t.Fatal("RecordUsage succeeded against a stopped service")
	}
	// A transport error is not a *StatusError, so nothing classifies it as
	// permanent — which is right: this is what a restart looks like.
	var se *StatusError
	if errors.As(err, &se) {
		t.Fatalf("a transport failure was reported as a status: %v", err)
	}
}

func TestARequestHonoursItsContext(t *testing.T) {
	client, _ := serve(t, http.StatusCreated, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.RecordUsage(ctx, &store.UsageRecord{RequestID: "r"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation to surface", err)
	}
}
