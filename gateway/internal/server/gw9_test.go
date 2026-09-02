package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"github.com/cognigate/gateway/internal/config"
)

// GW-9 turns /v1/meta into the deployment's own account of what it can do. Two
// properties carry the weight: the capability list must be true — an id present
// here is a promise that the requirement's conformance section passes — and the
// admin plane must answer with the same document, so an operator's key and a
// client's key feature-detect identically.

// semverPattern is the official expression from semver.org, anchored. The
// conformance suite carries its own copy because its module is stdlib-only; this
// one exists so a version that would fail the suite fails here first, in a test
// that runs on every build rather than only against a live deployment.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

func TestMetaIdentifiesTheProductAndItsAPIVersion(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)

	if meta.Name != "cognigate" {
		t.Errorf("name = %q, want cognigate", meta.Name)
	}
	if meta.APIVersion != "v1" {
		t.Errorf("api_version = %q, want v1; it tracks the major, and the routes are /v1/*", meta.APIVersion)
	}
	// A version a client cannot parse is worse than an obviously untagged one:
	// the whole point of publishing it is that it can be compared.
	if !semverPattern.MatchString(meta.Version) {
		t.Errorf("version %q is not semver; GW-9 makes it a comparable value, so even an untagged build reports 0.0.0-dev", meta.Version)
	}
}

func TestMetaLimitsReportTheConfiguredEnforcement(t *testing.T) {
	// GW-9.AC-3 requires the published limits to be the ones actually enforced.
	// A non-default configuration is the case that catches a hard-coded figure.
	h := newHarness(t, func(c *config.Config) {
		c.Limits.MaxRequestBytes = 4096
		c.Routing.MaxFallbackDepth = 3
	})
	tenant := h.newTenant("acme")

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)

	if meta.Limits.MaxRequestBytes != 4096 {
		t.Errorf("max_request_bytes = %d, want the configured 4096", meta.Limits.MaxRequestBytes)
	}
	if meta.Limits.MaxFallbackDepth != 3 {
		t.Errorf("max_fallback_depth = %d, want the configured 3", meta.Limits.MaxFallbackDepth)
	}
}

func TestCapabilitiesAreWellFormedRequirementIDs(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)

	if len(meta.Capabilities) == 0 {
		t.Fatal("capabilities is empty; a deployment that implements nothing cannot serve this endpoint either")
	}
	valid := regexp.MustCompile(`^gw-(?:[1-9]|1[0-4])$`)
	seen := map[string]bool{}
	for _, id := range meta.Capabilities {
		if !valid.MatchString(id) {
			t.Errorf("capability %q is not a gw-N id in the range GW-1..GW-14", id)
		}
		if seen[id] {
			t.Errorf("capability %q is listed twice", id)
		}
		seen[id] = true
	}
	// GW-10 is the conformance suite and GW-11 is a deployment's resource
	// footprint. Neither is a behaviour of the running process, so neither is
	// ever a capability it can claim — and the suite exempts them from gating
	// on the strength of that.
	for _, id := range []string{"gw-10", "gw-11"} {
		if seen[id] {
			t.Errorf("%s is listed as a capability; it is not a runtime behaviour of the gateway", id)
		}
	}
}

func TestCapabilitiesOmitObservabilityWhenMetricsAreOff(t *testing.T) {
	// GW-9.AC-4: a capability turned off by configuration disappears from the
	// list rather than being reported and then failing its section.
	h := newHarness(t, func(c *config.Config) { c.Metrics.Enabled = false })
	tenant := h.newTenant("acme")

	var meta metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &meta)

	for _, id := range meta.Capabilities {
		if id == "gw-8" {
			t.Fatalf("gw-8 is claimed with metrics disabled; the metric names GW-8 fixes are not exported at all, so its section would fail")
		}
	}

	// And the default deployment does claim it, so the test above is measuring
	// the switch rather than a capability that is never listed.
	on := newHarness(t)
	onTenant := on.newTenant("acme")
	var withMetrics metaResponse
	on.do(http.MethodGet, "/v1/meta", onTenant.dataKey, nil).decode(t, &withMetrics)
	if !contains(withMetrics.Capabilities, "gw-8") {
		t.Errorf("gw-8 is absent with metrics enabled: %v", withMetrics.Capabilities)
	}
}

func TestCachingIsClaimedOnlyWhenEnabled(t *testing.T) {
	// GW-12 is optional, and a deployment that declined it serves no cache
	// header at all. Claiming the id there would tell a client to feature-detect
	// something that is not present, which GW-9 makes a failure in its own right.
	off := newHarness(t)
	offTenant := off.newTenant("acme")

	var without metaResponse
	off.do(http.MethodGet, "/v1/meta", offTenant.dataKey, nil).decode(t, &without)
	if contains(without.Capabilities, "gw-12") {
		t.Errorf("gw-12 is claimed with caching disabled: %v", without.Capabilities)
	}

	// And the switch is what decides, rather than an id that is never listed.
	on := newHarness(t, func(c *config.Config) { c.Cache.Enabled = true })
	onTenant := on.newTenant("acme")

	var with metaResponse
	on.do(http.MethodGet, "/v1/meta", onTenant.dataKey, nil).decode(t, &with)
	if !contains(with.Capabilities, "gw-12") {
		t.Errorf("gw-12 is absent with caching enabled: %v", with.Capabilities)
	}
}

func TestAdminMetaServesTheSameDocument(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var data metaResponse
	h.do(http.MethodGet, "/v1/meta", tenant.dataKey, nil).decode(t, &data)

	var admin adminMetaResponse
	h.do(http.MethodGet, "/admin/v1/meta", tenant.adminKey, nil).decode(t, &admin)

	// Every field GW-9 names, compared as a unit: an admin key must be able to
	// feature-detect exactly as a data key does.
	if admin.Name != data.Name || admin.Version != data.Version || admin.APIVersion != data.APIVersion {
		t.Errorf("identity differs across planes: admin %+v, data %+v", admin.metaResponse, data)
	}
	if !equalStrings(admin.Capabilities, data.Capabilities) {
		t.Errorf("capabilities differ across planes: admin %v, data %v", admin.Capabilities, data.Capabilities)
	}
	if !equalStrings(admin.Endpoints, data.Endpoints) {
		t.Errorf("endpoints differ across planes: admin %v, data %v", admin.Endpoints, data.Endpoints)
	}
	if admin.Limits != data.Limits {
		t.Errorf("limits differ across planes: admin %+v, data %+v", admin.Limits, data.Limits)
	}

	// The admin-only additions survive the merge.
	if admin.Scope != "" && admin.Scope != "tenant:"+tenant.id {
		t.Errorf("scope = %q, want the calling key's", admin.Scope)
	}
	if len(admin.Events) == 0 {
		t.Error("the admin document lists no event types; a webhook cannot be registered against a list nobody can read")
	}
}

func TestAdminMetaNamesItsOwnRoute(t *testing.T) {
	h := newHarness(t)
	tenant := h.newTenant("acme")

	var body map[string]json.RawMessage
	h.do(http.MethodGet, "/admin/v1/meta", tenant.adminKey, nil).decode(t, &body)

	var object string
	if err := json.Unmarshal(body["object"], &object); err != nil {
		t.Fatalf("no object field: %v", err)
	}
	// The one field the two planes disagree on, and the only one: it says which
	// route answered, now that the bodies are otherwise the same.
	if object != "admin_meta" {
		t.Errorf("object = %q, want admin_meta", object)
	}
	for _, required := range []string{"name", "version", "api_version", "capabilities", "endpoints", "limits"} {
		if _, ok := body[required]; !ok {
			t.Errorf("the admin document is missing %q, which GW-9 requires on both planes", required)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
