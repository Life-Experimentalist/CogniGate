package conformance

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-5 — health and degradation.
//
// The requirement is unconditional: every deployment answers /v1/health and
// /healthz, so unlike GW-2, GW-3, GW-4 and GW-12 there is no feature flag that
// can excuse a gateway from this file.
//
// Two of the seven criteria are not writable against the mock as GW-1..GW-4 left
// it, and both gaps are in the harness rather than in the gateway:
//
//   - AC-4 asks what happens when every provider a tenant can use is blocked. A
//     provider is blocked only when every model of it is, and this mock's
//     catalogue is global and grows as tests add to it, so "trip them all" is
//     neither cheap nor stable. The mock therefore serves a restricted view at
//     /_only/<ids>, and this file points one tenant's provider at a view holding
//     a single model. The alternative — the catch-all fault — would have failed
//     every other test in the run and every concurrent run with it.
//
//   - AC-7 asks that health trigger no provider call. The mock counted only
//     completions, which a health check can never issue, so the assertion was
//     true by construction; it now counts catalogue reads as well, which is the
//     call a health check could plausibly make.

// TestGW5_AC1_HealthReportsProvidersAndAliases covers GW-5.AC-1: an authenticated
// health call reports a status, the tenant's providers with their breaker and
// catalogue age, and its aliases; an unauthenticated one is rejected.
func TestGW5_AC1_HealthReportsProvidersAndAliases(t *testing.T) {
	c := begin(t)

	resp := c.data(t, http.MethodGet, "/v1/health", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/health: status %d\n%s", resp.Status, truncate(resp.Body))
	}
	report := resp.JSON(t)

	switch status, _ := report["status"].(string); status {
	case "ok", "degraded", "unavailable":
	default:
		t.Errorf("health reports status %q, which is not one of ok, degraded, unavailable", status)
	}

	rows, _ := report["providers"].([]any)
	if len(rows) == 0 {
		t.Fatalf("health reports no providers for a tenant that has one\n%s", truncate(resp.Body))
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("a providers[] entry is not an object\n%s", truncate(resp.Body))
		}
		name, _ := row["provider"].(string)
		if _, ok := row["breaker"]; !ok {
			t.Errorf("providers[%s] has no breaker state", name)
		}
		// Presence, not a value: a freshly polled catalogue is legitimately zero
		// seconds old, so reading the number and comparing it would pass on a
		// gateway that omitted the field entirely.
		cat, _ := row["catalog"].(map[string]any)
		if _, ok := cat["age_seconds"]; !ok {
			t.Errorf("providers[%s] has no catalog.age_seconds", name)
		}
	}

	if _, ok := report["aliases"].([]any); !ok {
		t.Errorf("health has no aliases[] array\n%s", truncate(resp.Body))
	}

	// The same call without a credential. An empty key sends no Authorization
	// header at all, which is the shape a caller who has not read the docs sends.
	unauthenticated := c.do(t, http.MethodGet, "/v1/health", "", nil)
	if unauthenticated.Status != http.StatusUnauthorized {
		t.Errorf("GET /v1/health with no credential: status %d, want 401\n%s",
			unauthenticated.Status, truncate(unauthenticated.Body))
	}
}

// TestGW5_AC2_LivenessIsUnauthenticatedAndOpaque covers GW-5.AC-2: /healthz needs
// no credential and discloses nothing about the deployment behind it.
func TestGW5_AC2_LivenessIsUnauthenticatedAndOpaque(t *testing.T) {
	c := begin(t)

	resp := c.do(t, http.MethodGet, "/healthz", "", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /healthz: status %d\n%s", resp.Status, truncate(resp.Body))
	}

	// Decoded rather than byte-compared: whitespace is not part of the contract,
	// but the set of keys is. "Exactly one key, and it is the status" is at once
	// the shape the specification pins and the strongest available form of
	// "contains no provider or tenant information".
	body := resp.JSON(t)
	if len(body) != 1 {
		t.Errorf("GET /healthz returns %d fields, want only status\n%s", len(body), truncate(resp.Body))
	}
	if status, _ := body["status"].(string); status != "ok" {
		t.Errorf("GET /healthz reports status %q, want ok", status)
	}

	// The criterion in its own words, kept because the key count above would not
	// catch a deployment that named a provider inside the status string.
	for what, secret := range map[string]string{
		"the provider name": "mock",
		"the tenant id":     suite.tenantID,
		"a model id":        "mock-chat-a",
	} {
		if secret != "" && strings.Contains(string(resp.Body), secret) {
			t.Errorf("GET /healthz discloses %s (%q)\n%s", what, secret, truncate(resp.Body))
		}
	}
}

// TestGW5_AC3_OpenBreakerSurfacesWithinFiveSeconds covers GW-5.AC-3: forcing a
// breaker open flips the provider's reported breaker state and degrades the
// overall status, and does so within five seconds.
func TestGW5_AC3_OpenBreakerSurfacesWithinFiveSeconds(t *testing.T) {
	begin(t)

	// The model has to exist before the tenant's first catalogue poll, which
	// happens when its provider is registered — a model added afterwards is
	// invisible until the catalogue TTL elapses, and the completions below would
	// be rejected as unknown without ever dialling the mock.
	model := addMockModel(t, uniqueName("gw5-ac3"))

	tn := newTenant(t, "gw5-ac3")
	addMockProvider(t, tn)
	awaitModel(t, tn.Key, model, true)

	// A tenant with a healthy mock starts out healthy. Asserting it here is what
	// keeps the degraded assertion below from passing on a report that was
	// already degraded before the breaker had anything to do with it.
	before := suite.client.do(t, http.MethodGet, "/v1/health", tn.Key, nil).JSON(t)
	if status, _ := before["status"].(string); status != "ok" {
		t.Fatalf("a freshly provisioned tenant reports status %q before any fault", status)
	}

	injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)
	// The default threshold is five failures inside the window; a sixth costs
	// nothing and covers a deployment that counts differently.
	for i := 0; i < 6; i++ {
		chat(t, tn.Key, model)
	}

	// The clock starts at the last failure, which is the moment the specification
	// measures from.
	within := allowSeconds(t, 5*time.Second)

	report := awaitHealth(t, tn.Key, func(report map[string]any) bool {
		row, ok := providerHealthRow(report, "mock")
		if !ok {
			return false
		}
		return row["breaker"] == "open" && report["status"] == "degraded"
	}, `providers[mock].breaker "open" and an overall status of "degraded"`)
	within("the open breaker reached the health report")

	// Named explicitly, so a failure says which model rather than only that
	// something somewhere is open.
	row, _ := providerHealthRow(report, "mock")
	var named bool
	breakers, _ := row["breakers"].([]any)
	for _, raw := range breakers {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m, _ := b["model"].(string); m == model {
			named = true
			if b["breaker"] != "open" {
				t.Errorf("providers[mock].breakers[%s] is %v, want open", model, b["breaker"])
			}
		}
	}
	if !named {
		t.Errorf("providers[mock].breakers[] does not mention %q", model)
	}
}

// TestGW5_AC4_EveryProviderBlockedIsUnavailable covers GW-5.AC-4: when nothing a
// tenant can reach is still reachable, health is 503 and says unavailable.
func TestGW5_AC4_EveryProviderBlockedIsUnavailable(t *testing.T) {
	begin(t)

	// A model this run owns, never a seed model: faults are keyed by model id and
	// shared by everyone dialling this mock, so faulting a seed model forever
	// would break every other test and every concurrent run.
	model := addMockModel(t, uniqueName("gw5-ac4"))

	tn := newTenant(t, "gw5-ac4")

	// The restricted view. This tenant's only provider serves exactly one model,
	// which is what makes "every provider is blocked" reachable by tripping one
	// breaker. addMockProvider is not usable here: it waits for mock-chat-a, and
	// the view does not serve it.
	created := suite.client.admin(t, http.MethodPost, "/admin/v1/tenants/"+tn.ID+"/providers",
		map[string]any{
			"name":     "mock",
			"kind":     "openai",
			"base_url": suite.providerURL + "/_only/" + model,
			"keys":     []string{"mock-key-primary"},
		})
	if created.Status != http.StatusCreated {
		t.Fatalf("registering the restricted mock for %s: status %d\n%s",
			tn.ID, created.Status, truncate(created.Body))
	}
	awaitModel(t, tn.Key, model, true)

	// The view is the tenant's whole catalogue, so a model outside it must not be
	// visible — otherwise the breaker below would leave a reachable path and the
	// criterion would be testing nothing.
	if _, found := findModel(listModels(t, tn.Key), "mock-chat-a"); found {
		t.Fatalf("the restricted view leaked mock-chat-a into the tenant's catalog")
	}

	injectFault(t, model, mockprovider.FaultServerError, mockprovider.ForeverCount)
	for i := 0; i < 6; i++ {
		chat(t, tn.Key, model)
	}

	awaitHealth(t, tn.Key, func(report map[string]any) bool {
		return report["status"] == "unavailable"
	}, `an overall status of "unavailable"`)

	// awaitHealth reads the body and discards the status line, and the status
	// line is half of what this criterion asks for. One direct call settles it;
	// there is no race, because a breaker stays open for a minute.
	resp := suite.client.do(t, http.MethodGet, "/v1/health", tn.Key, nil)
	if resp.Status != http.StatusServiceUnavailable {
		t.Errorf("health with every provider blocked: status %d, want 503\n%s",
			resp.Status, truncate(resp.Body))
	}
	if status, _ := resp.JSON(t)["status"].(string); status != "unavailable" {
		t.Errorf("health with every provider blocked reports %q, want unavailable", status)
	}
}

// TestGW5_AC5_HealthNeverNamesAnotherTenant covers GW-5.AC-5: one tenant's health
// report says nothing about another tenant's providers or aliases.
func TestGW5_AC5_HealthNeverNamesAnotherTenant(t *testing.T) {
	c := begin(t)

	other := newTenant(t, "gw5-ac5")
	providerName := uniqueName("gw5-ac5-provider")
	aliasName := uniqueName("gw5-ac5-alias")

	created := c.admin(t, http.MethodPost, "/admin/v1/tenants/"+other.ID+"/providers",
		map[string]any{
			"name":     providerName,
			"kind":     "openai",
			"base_url": suite.providerURL,
			"keys":     []string{"mock-key-primary"},
		})
	if created.Status != http.StatusCreated {
		t.Fatalf("registering %q for %s: status %d\n%s",
			providerName, other.ID, created.Status, truncate(created.Body))
	}
	awaitModel(t, other.Key, "mock-chat-a", true)
	putAlias(t, other.ID, aliasName, map[string]any{"capabilities": []string{"chat"}})

	// The other tenant can see its own names. Without this the absence test below
	// would pass on a gateway that had simply failed to record any of it.
	mine := c.do(t, http.MethodGet, "/v1/health", other.Key, nil)
	for what, name := range map[string]string{"provider": providerName, "alias": aliasName} {
		if !strings.Contains(string(mine.Body), name) {
			t.Fatalf("the owning tenant's own health does not mention its %s %q\n%s",
				what, name, truncate(mine.Body))
		}
	}

	theirs := c.data(t, http.MethodGet, "/v1/health", nil)
	for what, name := range map[string]string{"provider": providerName, "alias": aliasName} {
		if strings.Contains(string(theirs.Body), name) {
			t.Errorf("a tenant's health names another tenant's %s %q\n%s",
				what, name, truncate(theirs.Body))
		}
	}
}

// TestGW5_AC6_BlockedRefreshReportsStale covers GW-5.AC-6: a catalogue the gateway
// can no longer refresh is reported stale, and staleness degrades the status.
func TestGW5_AC6_BlockedRefreshReportsStale(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw5-ac6")
	addMockProvider(t, tn)

	if status, _ := suite.client.do(t, http.MethodGet, "/v1/health", tn.Key, nil).
		JSON(t)["status"].(string); status != "ok" {
		t.Fatalf("a freshly provisioned tenant reports status %q before the refresh is blocked", status)
	}

	// Registered before the fault, so it runs after the fault is cleared:
	// t.Cleanup is last-in first-out, and a refresh attempted while the listing
	// endpoint was still failing would leave the catalogue stale for whatever
	// runs next.
	t.Cleanup(func() { refreshCatalogFor(t, tn.ID) })

	// A server error rather than a timeout. Once a snapshot is stale every health
	// call retries the refresh, and a timeout fault would make each of them block
	// for the provider timeout — which would wreck the poll loop below and any
	// timing assertion sharing the deployment.
	injectFault(t, mockprovider.ListingTarget, mockprovider.FaultServerError, mockprovider.ForeverCount)
	tryRefreshCatalogFor(t, tn.ID)

	awaitHealth(t, tn.Key, func(report map[string]any) bool {
		row, ok := providerHealthRow(report, "mock")
		if !ok {
			return false
		}
		cat, _ := row["catalog"].(map[string]any)
		return cat["state"] == "stale" && report["status"] == "degraded"
	}, `providers[mock].catalog.state "stale" and an overall status of "degraded"`)
}

// TestGW5_AC7_HealthIsLocalAndFast covers GW-5.AC-7: a hundred health calls answer
// from gateway-local state, quickly, without dialling a provider once.
func TestGW5_AC7_HealthIsLocalAndFast(t *testing.T) {
	begin(t)

	tn := newTenant(t, "gw5-ac7")
	addMockProvider(t, tn)

	// Snapshotted after provisioning, because provisioning legitimately dials the
	// mock — once, to fill the tenant's catalogue. The counters are cumulative
	// and shared with every concurrent run, so this has to be a difference.
	before := mockState(t)

	const calls = 100
	latencies := make([]time.Duration, 0, calls)
	for i := 0; i < calls; i++ {
		started := time.Now()
		resp := suite.client.do(t, http.MethodGet, "/v1/health", tn.Key, nil)
		latencies = append(latencies, time.Since(started))
		if resp.Status != http.StatusOK {
			t.Fatalf("health call %d: status %d\n%s", i+1, resp.Status, truncate(resp.Body))
		}
	}

	after := mockState(t)
	if got := after.Listings - before.Listings; got != 0 {
		t.Errorf("%d health calls caused %d provider catalog reads, want none", calls, got)
	}
	if got := totalRequests(after) - totalRequests(before); got != 0 {
		t.Errorf("%d health calls caused %d provider completions, want none", calls, got)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	// The 99th percentile of a hundred ordered samples is the ninety-ninth of
	// them, which is the second slowest.
	p99 := latencies[98]
	if p99 > 100*time.Millisecond {
		t.Errorf("health p99 over %d calls is %s, past the 100ms the specification allows "+
			"(slowest %s, median %s)", calls, p99.Round(time.Millisecond),
			latencies[calls-1].Round(time.Millisecond), latencies[calls/2].Round(time.Millisecond))
	}
}

// totalRequests sums the mock's per-model completion counters.
func totalRequests(snap mockSnapshot) int {
	total := 0
	for _, n := range snap.Requests {
		total += n
	}
	return total
}
