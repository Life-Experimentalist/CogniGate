package catalog_test

import (
	"context"
	"testing"

	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/provider"
	"github.com/cognigate/gateway/internal/routing"
	"github.com/cognigate/gateway/internal/store"
)

// Rates normally arrive with the provider's own model listing, through the
// non-standard input_cost_per_mtok extension. Google's Gemini endpoint and
// Anthropic's compatibility layer publish neither, so every request they serve
// was priced at zero and a cost quota over them could never fire. The configured
// table is the way an operator supplies what the vendor does not.

// unpriced serves one model and no rates, the way those two vendors do.
type unpriced struct{ kind string }

func (a unpriced) Kind() string { return a.kind }

func (a unpriced) ListModels(context.Context, provider.Credential) ([]store.Model, error) {
	return []store.Model{{ID: "gemini-2.5-flash"}}, nil
}

func (a unpriced) Do(context.Context, provider.Credential, *provider.Request) (*provider.Response, error) {
	return nil, nil // the catalog only ever lists
}

func entryFor(t *testing.T, prices map[string]map[string]catalog.Price, id string) catalog.Entry {
	t.Helper()
	ctx := context.Background()

	mem := store.NewMemory(false)
	tenant, err := mem.CreateTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err = mem.CreateProvider(ctx, &store.Provider{
		TenantID: tenant.ID,
		Name:     "google",
		Kind:     "gemini",
		Enabled:  true,
		Keys:     []string{"test-key"},
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	cat := catalog.New(mem, provider.NewRegistry(unpriced{kind: "gemini"}), catalog.Options{Prices: prices})
	snap, err := cat.Get(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	entry, ok := snap.Lookup(id)
	if !ok {
		t.Fatalf("model %q is not in the catalog", id)
	}
	return entry
}

func costOf(entry catalog.Entry) float64 {
	result := routing.Result{Candidate: routing.Candidate{Entry: entry}}
	return result.CostUSD(&provider.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000})
}

func TestUnpricedListingCostsNothing(t *testing.T) {
	entry := entryFor(t, nil, "gemini-2.5-flash")
	if entry.InputCostPerMTok != 0 || entry.OutputCostPerMTok != 0 {
		t.Fatalf("rates are %v/%v, want zero from a listing that carries none",
			entry.InputCostPerMTok, entry.OutputCostPerMTok)
	}
	if got := costOf(entry); got != 0 {
		t.Errorf("CostUSD is %v, want 0", got)
	}
}

func TestConfiguredPriceReachesTheCatalogAndTheCost(t *testing.T) {
	// Spelled with the case an operator might plausibly use, since the vendor's
	// pricing page and the listing do not always agree on it.
	prices := map[string]map[string]catalog.Price{
		"Gemini": {"Gemini-2.5-Flash": {Input: 0.30, Output: 2.50}},
	}

	entry := entryFor(t, prices, "gemini-2.5-flash")
	if entry.InputCostPerMTok != 0.30 || entry.OutputCostPerMTok != 2.50 {
		t.Fatalf("rates are %v/%v, want 0.30/2.50", entry.InputCostPerMTok, entry.OutputCostPerMTok)
	}
	// A million of each, so the figure is the two rates added.
	if got := costOf(entry); got != 2.80 {
		t.Errorf("CostUSD is %v, want 2.80", got)
	}

	// The qualified form addresses the same provider's copy, so it has to carry
	// the rate too or a caller who pinned a provider would be billed nothing.
	qualified := entryFor(t, prices, "google/gemini-2.5-flash")
	if qualified.InputCostPerMTok != 0.30 || qualified.OutputCostPerMTok != 2.50 {
		t.Errorf("qualified rates are %v/%v, want 0.30/2.50",
			qualified.InputCostPerMTok, qualified.OutputCostPerMTok)
	}
}
