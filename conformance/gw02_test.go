package conformance

import (
	"net/http"
	"testing"
)

// GW-2: a tenant addresses capabilities by portable names of its own, and the
// gateway decides which concrete model each one means.

func TestGW2_AC1_APortableNameServesAConcreteModel(t *testing.T) {
	begin(t)

	resp := chat(t, suite.dataKey, "fast")
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion naming the alias %q: status %d, want 200\n%s",
			"fast", resp.Status, truncate(resp.Body))
	}

	served := servedModel(t, resp)
	entries := listModels(t, suite.dataKey)
	if _, found := findModel(concreteModels(entries), served); !found {
		t.Errorf("%s names %q, which is not one of the concrete models this tenant lists: %v",
			headerServedBy, served, modelIDs(concreteModels(entries)))
	}

	// The upstream is asked for the resolved model, not the portable name, so the
	// body an OpenAI client parses says which model actually answered.
	if model, _ := resp.JSON(t)["model"].(string); model != served {
		t.Errorf("the response body names model %q, want the served model %q", model, served)
	}
}

func TestGW2_AC2_AnAliasCannotShadowAModelID(t *testing.T) {
	begin(t)

	// mock-chat-a is a model this tenant already lists, so the name means
	// something before the alias is written.
	if _, found := findModel(listModels(t, suite.dataKey), "mock-chat-a"); !found {
		t.Fatalf("mock-chat-a is not in the catalog, so writing an alias for it would collide with nothing")
	}

	resp := tryPutAlias(t, suite.tenantID, "mock-chat-a", map[string]any{
		"cost_tier":    "cheapest",
		"capabilities": []string{"chat"},
	})
	if resp.Status != http.StatusConflict {
		t.Fatalf("PUT an alias named after a model: status %d, want 409\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "alias_collides_with_model" {
		t.Errorf("error.code = %q, want %q\n%s", code, "alias_collides_with_model", truncate(resp.Body))
	}
}

func TestGW2_AC3_ACheaperModelIsAdoptedWithoutAConfigurationChange(t *testing.T) {
	begin(t)

	// "fast" is a cost-tier alias, so what it means is a function of the catalog
	// alone. Nothing below edits it.
	before := servedModel(t, chatOK(t, "fast"))

	cheaper := addPricedMockModel(t, uniqueName("mock-cheap"), 0.01)
	if before == cheaper {
		t.Fatalf("%q already resolved to the model this test was about to add", "fast")
	}

	after := servedModel(t, chatOK(t, "fast"))
	if after != cheaper {
		t.Errorf("after a model priced at 0.01/Mtok appeared, %q still resolves to %q (was %q); "+
			"want the cheaper model %q", "fast", after, before, cheaper)
	}
}

func TestGW2_AC4_APinOverridesSelectionAndReleasingItRestoresIt(t *testing.T) {
	begin(t)

	name := uniqueName("gw2-ac4")
	constraints := map[string]any{"cost_tier": "cheapest", "capabilities": []string{"chat"}}

	putAlias(t, suite.tenantID, name, constraints)
	unpinned := servedModel(t, chatOK(t, name))
	// The pin below has to name something the constraints would not have chosen,
	// or overriding them proves nothing. mock-vision is the most expensive chat
	// model the mock serves, so "cheapest" can never land on it.
	if unpinned == "mock-vision" {
		t.Fatalf("the cheapest chat model is already %q, so pinning to it would not be an override", unpinned)
	}

	putAlias(t, suite.tenantID, name, map[string]any{
		"cost_tier":    "cheapest",
		"capabilities": []string{"chat"},
		"pin":          "mock-vision",
	})
	if pinned := servedModel(t, chatOK(t, name)); pinned != "mock-vision" {
		t.Errorf("with a pin on %q, the alias serves %q", "mock-vision", pinned)
	}

	putAlias(t, suite.tenantID, name, constraints)
	if released := servedModel(t, chatOK(t, name)); released != unpinned {
		t.Errorf("after the pin was released the alias serves %q, want the constraint-selected %q",
			released, unpinned)
	}
}

func TestGW2_AC5_AnAliasThatMatchesNothingFailsAndIsReportedDegraded(t *testing.T) {
	begin(t)

	name := uniqueName("gw2-ac5")
	putAlias(t, suite.tenantID, name, map[string]any{"capabilities": []string{"telepathy"}})

	resp := chat(t, suite.dataKey, name)
	if resp.Status != http.StatusNotFound {
		t.Fatalf("a completion naming an unsatisfiable alias: status %d, want 404\n%s",
			resp.Status, truncate(resp.Body))
	}
	if code := resp.ErrorCode(t); code != "alias_unresolvable" {
		t.Errorf("error.code = %q, want %q\n%s", code, "alias_unresolvable", truncate(resp.Body))
	}

	// A failed request tells the caller. Health is what tells the operator,
	// without anyone having to send the failing request first.
	awaitHealth(t, suite.dataKey, func(report map[string]any) bool {
		row, ok := nameState(report, "aliases", name)
		if !ok {
			return false
		}
		return row["state"] == "degraded" && row["reason"] == "alias_unresolvable"
	}, "aliases[] naming "+name+" as degraded with reason alias_unresolvable")
}

func TestGW2_AC6_AliasesBelongToTheTenantThatWroteThem(t *testing.T) {
	begin(t)

	// A name this run invented, rather than one every tenant is given: an alias
	// both tenants happen to hold would answer for both and prove nothing.
	name := uniqueName("gw2-ac6")
	putAlias(t, suite.tenantID, name, map[string]any{
		"cost_tier": "cheapest", "capabilities": []string{"chat"},
	})
	if resp := chat(t, suite.dataKey, name); resp.Status != http.StatusOK {
		t.Fatalf("the tenant that wrote %q cannot use it: status %d\n%s",
			name, resp.Status, truncate(resp.Body))
	}

	// The second tenant points at the same mock, so it can serve chat traffic.
	// Whatever it says about this alias is therefore about the alias.
	other := newTenant(t, "gw2-ac6")
	addMockProvider(t, other)

	resp := chat(t, other.Key, name)
	if resp.Status != http.StatusNotFound {
		t.Fatalf("a tenant that never wrote %q can address it: status %d\n%s",
			name, resp.Status, truncate(resp.Body))
	}
	// Not alias_unresolvable: to a tenant with no such alias the name is simply
	// not a model it has.
	if code := resp.ErrorCode(t); code != "model_not_found" {
		t.Errorf("error.code = %q, want %q\n%s", code, "model_not_found", truncate(resp.Body))
	}
	if _, found := findModel(listModels(t, other.Key), name); found {
		t.Errorf("GET /v1/models lists %q for a tenant that never wrote it", name)
	}
}

func TestGW2_AC7_AliasesAreListedAsAliasesWithWhatTheyResolveTo(t *testing.T) {
	begin(t)

	name := uniqueName("gw2-ac7")
	putAlias(t, suite.tenantID, name, map[string]any{
		"cost_tier": "cheapest", "capabilities": []string{"chat"},
	})

	entries := listModels(t, suite.dataKey)
	row, found := findModel(entries, name)
	if !found {
		t.Fatalf("GET /v1/models does not list the alias %q: %v", name, modelIDs(entries))
	}
	if !row.CogniGate.Alias {
		t.Errorf("the row for %q does not carry cognigate.alias, so a client cannot tell it "+
			"apart from a model the provider published", name)
	}
	if row.OwnedBy == "" {
		t.Errorf("the row for %q has an empty owned_by", name)
	}

	resolved := row.CogniGate.ResolvesTo
	if resolved == "" {
		t.Fatalf("the row for %q has an empty cognigate.resolves_to", name)
	}
	if _, ok := findModel(concreteModels(entries), resolved); !ok {
		t.Errorf("%q resolves_to %q, which is not a concrete model in the same listing: %v",
			name, resolved, modelIDs(concreteModels(entries)))
	}

	// The listing and the routing decision have to agree, or resolves_to is a
	// second opinion rather than a report.
	if served := servedModel(t, chatOK(t, name)); served != resolved {
		t.Errorf("%q lists resolves_to %q but is served by %q", name, resolved, served)
	}
}

// --- helpers ---------------------------------------------------------------

// chatOK sends a completion the test expects to succeed.
func chatOK(t *testing.T, model string) *response {
	t.Helper()

	resp := chat(t, suite.dataKey, model)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion naming %q: status %d, want 200\n%s",
			model, resp.Status, truncate(resp.Body))
	}
	return resp
}

// nameState picks one row out of the aliases[] or rules[] section of a health
// report. Both sections carry the same shape, so one reader serves both.
func nameState(report map[string]any, section, name string) (map[string]any, bool) {
	rows, _ := report[section].([]any)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := row["name"].(string); n == name {
			return row, true
		}
	}
	return nil, false
}
