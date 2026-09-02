package conformance

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cognigate/cognigate/conformance/mockprovider"
)

// GW-1: the model catalog is discovered from the providers a tenant has
// configured, not written down anywhere.

func TestGW1_AC1_ModelsAreListedWithProviderAttribution(t *testing.T) {
	client := begin(t)

	resp := client.do(t, http.MethodGet, "/v1/models", suite.dataKey, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /v1/models: status %d, want 200\n%s", resp.Status, truncate(resp.Body))
	}

	body := resp.JSON(t)
	if body["object"] != "list" {
		t.Errorf("object = %v, want %q", body["object"], "list")
	}

	rows, ok := body["data"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("data is not a non-empty array\n%s", truncate(resp.Body))
	}

	// Read the raw map rather than the decoded struct: the criterion is that
	// each field is *present*, and a decoded struct cannot tell an absent field
	// from one that decoded to the zero value.
	for i, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("data[%d] is not an object\n%s", i, truncate(resp.Body))
		}
		for _, field := range []string{"id", "owned_by"} {
			if s, _ := row[field].(string); s == "" {
				t.Errorf("data[%d]: %s is %v, want a non-empty string", i, field, row[field])
			}
		}

		cg, ok := row["cognigate"].(map[string]any)
		if !ok {
			t.Errorf("data[%d] (%v): there is no cognigate block", i, row["id"])
			continue
		}
		// An alias names no provider until it resolves to one, so the
		// attribution claim is about the concrete models in the list.
		if alias, _ := cg["alias"].(bool); alias {
			continue
		}
		if p, _ := cg["provider"].(string); p == "" {
			t.Errorf("data[%d] (%v): cognigate.provider is %v, want a non-empty string",
				i, row["id"], cg["provider"])
		}
	}
}

func TestGW1_AC2_ModelsRejectsAMissingOrUnknownKey(t *testing.T) {
	client := begin(t)

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"no credential at all", ""},
		{"a well-formed key nobody minted", "cg-" + uniqueName("nosuchkey")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := client.do(t, http.MethodGet, "/v1/models", tc.key, nil)
			if resp.Status != http.StatusUnauthorized {
				t.Fatalf("GET /v1/models: status %d, want 401\n%s", resp.Status, truncate(resp.Body))
			}
			if code := resp.ErrorCode(t); code != "invalid_api_key" {
				t.Errorf("error.code = %q, want %q\n%s", code, "invalid_api_key", truncate(resp.Body))
			}
		})
	}
}

func TestGW1_AC3_CatalogsAreScopedToTheTenantsOwnProviders(t *testing.T) {
	begin(t)

	// A tenant with no provider registered at all is the strongest form of "no
	// tenant sees a provider it has no key for": there is no configuration it
	// could have been given by accident.
	other := newTenant(t, "gw1-ac3")

	mine := concreteModels(listModels(t, suite.dataKey))
	if len(mine) == 0 {
		t.Fatalf("the suite tenant lists no concrete models, so there is nothing here to be isolated from")
	}

	theirs := listModels(t, other.Key)
	if concrete := concreteModels(theirs); len(concrete) != 0 {
		t.Errorf("a tenant with no providers lists %d concrete models: %v",
			len(concrete), modelIDs(concrete))
	}
	if _, found := findModel(theirs, "mock-chat-a"); found {
		t.Errorf("the provider-less tenant can see mock-chat-a, which is served by a provider it holds no key for")
	}
}

func TestGW1_AC4_ARefreshPublishesANewModelWithoutARestart(t *testing.T) {
	begin(t)

	// Warm first, so "absent" below means the model genuinely is not in the
	// catalog rather than that no catalog had been built yet.
	refreshCatalog(t)
	// Registered before the model exists so that it runs *after* the model's own
	// removal: t.Cleanup is last-in first-out, and refreshing while the model
	// still existed would leave the gateway holding an entry for a model the
	// mock no longer serves.
	t.Cleanup(func() { refreshCatalog(t) })

	id := addMockModelRaw(t, uniqueName("gw1-ac4"), 64000)
	if _, found := findModel(listModels(t, suite.dataKey), id); found {
		t.Fatalf("%q is listed before any refresh, so this test cannot show that the refresh is what published it", id)
	}

	refreshCatalog(t)
	entry, found := findModel(listModels(t, suite.dataKey), id)
	if !found {
		t.Fatalf("%q is still absent after POST /admin/v1/catalog/refresh", id)
	}
	// The id alone would also appear if the gateway had invented the entry; the
	// context window can only have come from the provider.
	if entry.CogniGate.ContextWindow != 64000 {
		t.Errorf("cognigate.context_window = %d, want 64000", entry.CogniGate.ContextWindow)
	}
}

func TestGW1_AC5_ARetiredModelFallsThroughToItsChain(t *testing.T) {
	begin(t)

	gone := addMockModel(t, uniqueName("gw1-ac5"))
	putRoute(t, suite.tenantID, gone, gone, "mock-chat-a")

	// Removed at the provider rather than at the gateway: the criterion is about
	// a model an upstream stopped offering.
	mockControl(t, http.MethodDelete, "/_control/models/"+gone, nil)
	refreshCatalog(t)
	awaitModel(t, suite.dataKey, gone, false)

	resp := chat(t, suite.dataKey, gone)
	if resp.Status != http.StatusOK {
		t.Fatalf("a completion naming the retired model: status %d, want 200\n%s",
			resp.Status, truncate(resp.Body))
	}
	// No claim about the fallback depth here: a chain position that no longer
	// resolves is dropped before dispatch sees it, so the surviving entry is
	// served at depth 0 rather than as a fallback from a failure.
	if served := servedModel(t, resp); served != "mock-chat-a" {
		t.Errorf("%s names %q, want the chain's surviving entry %q", headerServedBy, served, "mock-chat-a")
	}
}

func TestGW1_AC6_ThePreviousCatalogSurvivesAListingOutage(t *testing.T) {
	begin(t)

	// A catalog the gateway has never built has no previous version to keep
	// serving, so warm it before breaking the listing endpoint.
	refreshCatalog(t)
	// Ahead of the fault, so the cleanup order is: clear the fault, then rebuild
	// the catalog from a mock that is answering again.
	t.Cleanup(func() { refreshCatalog(t) })

	injectFault(t, mockprovider.ListingTarget, mockprovider.FaultServerError, mockprovider.ForeverCount)

	// The refresh is expected to fail. What the gateway serves afterwards is the
	// point, not the status of the call that failed.
	tryRefreshCatalogFor(t, suite.tenantID)

	if _, found := findModel(listModels(t, suite.dataKey), "mock-chat-a"); !found {
		t.Fatalf("the catalog emptied out when the provider's listing endpoint began failing")
	}

	awaitHealth(t, suite.dataKey, func(report map[string]any) bool {
		row, ok := providerHealthRow(report, "mock")
		if !ok {
			return false
		}
		cat, _ := row["catalog"].(map[string]any)
		age, _ := cat["age_seconds"].(float64)
		return age > 0
	}, "a non-zero providers[mock].catalog.age_seconds")
}

// TestGW1_AC7 is the static-list ban. It is a claim about the repository rather
// than about the running gateway, so it reads the checkout it is running from
// and skips when there is not one — the containerised form of the suite
// (GW-10.AC-6) ships without a working tree.
func TestGW1_AC7_NoServedModelIdIsWrittenIntoConfiguration(t *testing.T) {
	begin(t)

	root, ok := repoRoot()
	if !ok {
		t.Skip("not running inside a checkout: the static-list ban is a claim about committed files")
	}

	served := map[string]bool{}
	for _, e := range concreteModels(listModels(t, suite.dataKey)) {
		served[e.ID] = true
	}
	if len(served) == 0 {
		t.Fatal("the gateway serves no models, so this test would pass without checking anything")
	}

	var findings []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A directory this process cannot read is not evidence of a static
			// list, and failing on one would make the test depend on which user
			// ran it.
			return nil
		}
		if d.IsDir() {
			if skippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !configShaped(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 4<<20 {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		for id := range served {
			if strings.Contains(text, id) {
				findings = append(findings, filepath.ToSlash(rel)+" names "+id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if len(findings) > 0 {
		sort.Strings(findings)
		t.Errorf("a model the gateway serves is written into configuration, so the catalog is not "+
			"wholly discovered:\n\t%s", strings.Join(findings, "\n\t"))
	}
}

// --- helpers ---------------------------------------------------------------

func modelIDs(entries []modelEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// providerHealthRow picks one provider out of a health report.
func providerHealthRow(report map[string]any, name string) (map[string]any, bool) {
	rows, _ := report["providers"].([]any)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := row["provider"].(string); n == name {
			return row, true
		}
	}
	return nil, false
}

// repoRoot walks up from the working directory looking for a checkout.
func repoRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// skippedDir names the directories a static list cannot be committed in:
// dependency trees, build output, and the suite's own source, which is allowed
// to name the mock's models because that is what it tests against.
func skippedDir(name string) bool {
	switch name {
	case ".git", ".next", ".idea", ".vscode",
		"node_modules", "vendor", "target", "dist", "build", "out":
		return true
	}
	return name == "conformance"
}

// configShaped reports whether a file is the kind of thing an operator edits to
// configure a deployment. Lock files are excluded: they are generated, and the
// largest of them would be read on every run for nothing.
func configShaped(name string) bool {
	switch name {
	case "package-lock.json", "go.sum", "conformance-report.json":
		return false
	}
	if strings.HasPrefix(name, ".env") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yml", ".yaml", ".json", ".toml", ".ini", ".conf", ".cfg", ".properties", ".env":
		return true
	}
	return false
}
