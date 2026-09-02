package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// --- metrics ----------------------------------------------------------------

// scrapeLines returns the exposition lines for one metric name.
//
// The scrape goes through the real endpoint rather than reading the collector,
// because "the series exists" and "the series is exported" are different claims
// and GW-8 makes the second one.
func (h *harness) scrapeLines(name string) []string {
	h.t.Helper()

	res := h.do(http.MethodGet, "/metrics", "", nil)
	if res.status != http.StatusOK {
		h.t.Fatalf("GET /metrics: status %d, body %s", res.status, res.body)
	}

	var out []string
	for _, line := range strings.Split(string(res.body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			out = append(out, line)
		}
	}
	return out
}

// hasLabels reports whether an exposition line carries every label in want. The
// client library writes labels in its own order, so this matches on assignments
// rather than on the rendered set.
func hasLabels(line string, want map[string]string) bool {
	for k, v := range want {
		if !strings.Contains(line, k+"="+strconv.Quote(v)) {
			return false
		}
	}
	return true
}

func lineValue(t *testing.T, line string) float64 {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("exposition line has no value: %q", line)
	}
	v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	if err != nil {
		t.Fatalf("exposition line value is not a number: %q", line)
	}
	return v
}

// metricValue sums the samples of a metric whose labels match want.
func (h *harness) metricValue(name string, want map[string]string) float64 {
	h.t.Helper()

	var total float64
	for _, line := range h.scrapeLines(name) {
		if hasLabels(line, want) {
			total += lineValue(h.t, line)
		}
	}
	return total
}

func candidate(tenantID, model string) routing.Candidate {
	return routing.Candidate{TenantID: tenantID, Provider: "test", Model: model}
}

// TestFallbackCascadesAreCountedPerHop pins the shape of the series, not just
// its existence.
//
// One row per request would say "the chain that started at A ended at C", which
// names neither link that broke. Per hop, A→B and B→C say that A failed and that
// B was skipped, which is what an operator is actually looking for.
func TestFallbackCascadesAreCountedPerHop(t *testing.T) {
	h := newHarness(t)
	const tenantID = "ten_cascade"

	result := &routing.Result{
		Depth:     2,
		Candidate: candidate(tenantID, "model-c"),
		Attempts: []routing.Attempt{
			{Candidate: candidate(tenantID, "model-a"), Failure: provider.FailServer},
			{Candidate: candidate(tenantID, "model-b"), Skipped: true},
			{Candidate: candidate(tenantID, "model-c"), Failure: provider.FailNone},
		},
	}
	h.srv.meterCascade(tenantID, result)

	// The reason belongs to the attempt that handed off, not to the one that
	// took over: A→B is labelled with A's failure.
	hops := []struct {
		from, to, reason string
	}{
		{"model-a", "model-b", "server"},
		{"model-b", "model-c", "breaker_open"},
	}
	for _, hop := range hops {
		want := map[string]string{
			"tenant":     tenantID,
			"from_model": hop.from,
			"to_model":   hop.to,
			"reason":     hop.reason,
		}
		if got := h.metricValue("cognigate_fallback_cascades_total", want); got != 1 {
			t.Errorf("%s→%s (%s) counted %v times, want 1", hop.from, hop.to, hop.reason, got)
		}
	}

	if lines := h.scrapeLines("cognigate_fallback_cascades_total"); len(lines) != len(hops) {
		t.Errorf("the cascade counter has %d series, want one per hop (%d):\n%s",
			len(lines), len(hops), strings.Join(lines, "\n"))
	}
}

// TestNoCascadeIsCountedWhenThePrimaryServed: the counter has to stay at zero on
// the ordinary path, or the ratio an operator alerts on is meaningless.
func TestNoCascadeIsCountedWhenThePrimaryServed(t *testing.T) {
	h := newHarness(t)
	const tenantID = "ten_direct"

	h.srv.meterCascade(tenantID, &routing.Result{
		Depth:     0,
		Candidate: candidate(tenantID, "model-a"),
		Attempts: []routing.Attempt{
			{Candidate: candidate(tenantID, "model-a"), Failure: provider.FailNone},
		},
	})

	if lines := h.scrapeLines("cognigate_fallback_cascades_total"); len(lines) != 0 {
		t.Errorf("a request the primary served produced cascade series:\n%s", strings.Join(lines, "\n"))
	}
}

// TestCascadeReasonUsesABoundedVocabulary.
//
// The label value comes from the failure enumeration and never from upstream
// error text. Free text here would be unbounded cardinality, and under GW-14 it
// may quote the request that produced it.
func TestCascadeReasonUsesABoundedVocabulary(t *testing.T) {
	cases := []struct {
		name    string
		attempt routing.Attempt
		want    string
	}{
		{"skipped outranks the failure kind", routing.Attempt{Skipped: true, Failure: provider.FailServer}, "breaker_open"},
		{"transport", routing.Attempt{Failure: provider.FailTransport}, "transport"},
		{"server", routing.Attempt{Failure: provider.FailServer}, "server"},
		{"rate limit", routing.Attempt{Failure: provider.FailRateLimit}, "rate_limit"},
		{"client", routing.Attempt{Failure: provider.FailClient}, "client"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cascadeReason(c.attempt); got != c.want {
				t.Errorf("cascadeReason = %q, want %q", got, c.want)
			}
		})
	}
}

// TestMetricsNeedsNoCredential: GW-8 says the endpoint must not require a
// cg-*/cga-* key by default, because scrapers are not tenants. A gateway that
// guarded it would be silently unmonitorable behind a working deployment.
func TestMetricsNeedsNoCredential(t *testing.T) {
	h := newHarness(t)
	if res := h.do(http.MethodGet, "/metrics", "", nil); res.status != http.StatusOK {
		t.Fatalf("GET /metrics with no credential: status %d, body %s", res.status, res.body)
	}
}

// --- the event history ------------------------------------------------------

func (h *harness) seedEvents(tenantID string, n int) []string {
	h.t.Helper()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		e := &store.Event{
			ID:       fmt.Sprintf("ev_%s_%02d", tenantID, i),
			Type:     "breaker.opened",
			Created:  base.Add(time.Duration(i) * time.Second),
			TenantID: tenantID,
			Data:     map[string]any{"provider": "test", "model": "test-small"},
		}
		if err := h.mem.RecordEvent(context.Background(), e); err != nil {
			h.t.Fatalf("seeding event %d: %v", i, err)
		}
		ids = append(ids, e.ID)
	}
	return ids
}

func TestListEventsReturnsTheTenantsHistoryNewestFirst(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")
	ids := h.seedEvents(tenant.id, 3)

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/events", tenant.adminKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("listing events: status %d, body %s", res.status, res.body)
	}
	var page pageOf[store.Event]
	res.decode(t, &page)

	if len(page.Data) != len(ids) {
		t.Fatalf("listing returned %d events, want %d", len(page.Data), len(ids))
	}
	// Newest first: a poller that has fallen behind reads until it recognises
	// something rather than from the start of a thousand-entry list.
	for i, want := range []string{ids[2], ids[1], ids[0]} {
		if page.Data[i].ID != want {
			t.Errorf("event %d is %s, want %s", i, page.Data[i].ID, want)
		}
	}
	if page.Data[0].Data["provider"] != "test" {
		t.Errorf("the payload did not survive the round trip: %v", page.Data[0].Data)
	}
}

// TestListEventsCarriesTheWebhookEnvelopesFieldNames: a receiver reconciling what
// it polled against what it was delivered is the ordinary use of this endpoint,
// so the two shapes must not diverge.
func TestListEventsCarriesTheWebhookEnvelopesFieldNames(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	if err := h.mem.RecordEvent(context.Background(), &store.Event{
		ID:       "ev_bare",
		Type:     "catalog.stale",
		Created:  time.Now().UTC(),
		TenantID: tenant.id,
	}); err != nil {
		t.Fatalf("seeding event: %v", err)
	}

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/events", tenant.adminKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("listing events: status %d, body %s", res.status, res.body)
	}
	var page struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	res.decode(t, &page)
	if len(page.Data) != 1 {
		t.Fatalf("listing returned %d events, want 1", len(page.Data))
	}
	for _, field := range []string{"id", "type", "created", "tenant", "data"} {
		if _, ok := page.Data[0][field]; !ok {
			t.Errorf("the serialised event has no %q field; the webhook envelope carries one", field)
		}
	}
}

func TestListEventsPaginatesWithoutGapsOrRepeats(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	const total = 5
	want := map[string]bool{}
	for _, id := range h.seedEvents(tenant.id, total) {
		want[id] = true
	}

	got := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		path := "/admin/v1/tenants/" + tenant.id + "/events?limit=2"
		if cursor != "" {
			path += "&after=" + cursor
		}
		res := h.do(http.MethodGet, path, tenant.adminKey, nil)
		if res.status != http.StatusOK {
			t.Fatalf("listing events: status %d, body %s", res.status, res.body)
		}
		var page pageOf[store.Event]
		res.decode(t, &page)

		if len(page.Data) > 2 {
			t.Fatalf("page carries %d rows, want at most the requested 2", len(page.Data))
		}
		for _, e := range page.Data {
			if got[e.ID] {
				t.Errorf("event %s returned on more than one page", e.ID)
			}
			got[e.ID] = true
			cursor = e.ID
		}
		if !page.HasMore {
			break
		}
		if len(page.Data) == 0 {
			t.Fatal("has_more is set on an empty page; the cursor cannot advance")
		}
	}

	for id := range want {
		if !got[id] {
			t.Errorf("event %s was never returned", id)
		}
	}
	if len(got) != total {
		t.Errorf("walked %d events, want %d", len(got), total)
	}
}

// TestListEventsIsScopedToTheKeysTenant: the history names providers, models and
// quota windows, so reading another tenant's would be a disclosure. The answer is
// 404 rather than 403 because a 403 would confirm the tenant exists.
func TestListEventsIsScopedToTheKeysTenant(t *testing.T) {
	h := newHarness(t)
	mine := h.newTenant("mine")
	theirs := h.newTenant("theirs")
	h.seedEvents(theirs.id, 2)

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+theirs.id+"/events", mine.adminKey, nil)
	h.expectError(res, http.StatusNotFound, apierr.CodeResourceNotFound)

	// The root credential does reach it, so the 404 is a scope decision rather
	// than a broken route.
	if res := h.do(http.MethodGet, "/admin/v1/tenants/"+theirs.id+"/events", testBootstrapKey, nil); res.status != http.StatusOK {
		t.Fatalf("root reading the same history: status %d, body %s", res.status, res.body)
	}
}

// TestListEventsRefusesADataKey: the history is control-plane data, and a cg-*
// credential presented to the admin plane is the cross-plane case GW-6 fixes at
// 401 wrong_plane.
func TestListEventsRefusesADataKey(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/events", tenant.dataKey, nil)
	h.expectError(res, http.StatusUnauthorized, apierr.CodeWrongPlane)
}

func TestListEventsOnATenantThatHasRaisedNone(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	res := h.do(http.MethodGet, "/admin/v1/tenants/"+tenant.id+"/events", tenant.adminKey, nil)
	if res.status != http.StatusOK {
		t.Fatalf("listing events: status %d, body %s", res.status, res.body)
	}
	var page pageOf[store.Event]
	res.decode(t, &page)
	if len(page.Data) != 0 || page.HasMore {
		t.Errorf("an empty history returned %d rows with has_more=%v", len(page.Data), page.HasMore)
	}
	// An empty collection is an empty page, not a 404: the tenant exists and has
	// nothing to report, which a caller must be able to tell from a bad id.
	if page.Object == "" {
		t.Error("the empty page carries no object field")
	}
}
