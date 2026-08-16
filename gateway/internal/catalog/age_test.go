package catalog

import (
	"sort"
	"testing"
	"time"

	"github.com/cognigate/gateway/internal/store"
)

// Ages feeds cognigate_catalog_age_seconds, which is what an operator alerts on
// when a provider's models stop appearing. Three of its properties are worth
// pinning: it is per provider rather than per tenant, a provider is counted once
// however many models it serves, and a tenant that has never loaded contributes
// no series at all — an absent one says "never loaded" where a zero would say
// "just refreshed".

func snapshotAt(fetched time.Time, providers ...string) *Snapshot {
	snap := &Snapshot{FetchedAt: fetched}
	for i, p := range providers {
		snap.Models = append(snap.Models, Entry{Model: store.Model{
			ID:       string(rune('a'+i)) + "-model",
			Provider: p,
		}})
	}
	return snap
}

func newTestCatalog(now time.Time) *Catalog {
	c := New(nil, nil, Options{})
	c.now = func() time.Time { return now }
	return c
}

func TestAgesReportsOneRowPerProvider(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := newTestCatalog(now)

	// Two providers, three models: the duplicate must collapse.
	c.perTenant["ten_a"] = &tenantState{
		snapshot: snapshotAt(now.Add(-90*time.Second), "openai", "anthropic", "openai"),
	}

	got := c.Ages()
	if len(got) != 2 {
		t.Fatalf("Ages returned %d rows, want one per provider: %+v", len(got), got)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Provider < got[j].Provider })
	if got[0].Provider != "anthropic" || got[1].Provider != "openai" {
		t.Errorf("providers are %s and %s, want anthropic and openai", got[0].Provider, got[1].Provider)
	}
	for _, row := range got {
		if row.TenantID != "ten_a" {
			t.Errorf("row for %s carries tenant %q, want ten_a", row.Provider, row.TenantID)
		}
		// A tenant's providers share one refresh cycle, so they share an age.
		if row.Age != 90*time.Second {
			t.Errorf("row for %s has age %v, want 90s", row.Provider, row.Age)
		}
	}
}

func TestAgesSkipsTenantsThatHaveNeverLoaded(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := newTestCatalog(now)

	// A tenant whose refresh is registered but has not produced a snapshot yet.
	// Reporting it as zero would read as a catalog that had just been refreshed.
	c.perTenant["ten_cold"] = &tenantState{}
	c.perTenant["ten_warm"] = &tenantState{snapshot: snapshotAt(now.Add(-time.Minute), "openai")}

	got := c.Ages()
	if len(got) != 1 {
		t.Fatalf("Ages returned %d rows, want only the loaded tenant's: %+v", len(got), got)
	}
	if got[0].TenantID != "ten_warm" {
		t.Errorf("row is for %s, want ten_warm", got[0].TenantID)
	}
}

func TestAgesSkipsEntriesWithNoProvider(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := newTestCatalog(now)

	// An unattributed model would otherwise become a series labelled with an
	// empty provider, which is worse than no series: it alerts on nothing an
	// operator can act on.
	c.perTenant["ten_a"] = &tenantState{snapshot: snapshotAt(now, "", "openai")}

	got := c.Ages()
	if len(got) != 1 || got[0].Provider != "openai" {
		t.Fatalf("Ages returned %+v, want only the openai row", got)
	}
}

func TestAgesOnAnEmptyCatalog(t *testing.T) {
	if got := newTestCatalog(time.Now()).Ages(); len(got) != 0 {
		t.Errorf("a catalog that has served nothing reports %+v, want no rows", got)
	}
}

func TestSnapshotAge(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if got := (&Snapshot{FetchedAt: now.Add(-30 * time.Second)}).Age(now); got != 30*time.Second {
		t.Errorf("Age is %v, want 30s", got)
	}
	// A snapshot that was never fetched, and a nil one, are both zero rather
	// than an enormous duration measured from the zero time.
	if got := (&Snapshot{}).Age(now); got != 0 {
		t.Errorf("an unfetched snapshot reports age %v, want 0", got)
	}
	var nilSnap *Snapshot
	if got := nilSnap.Age(now); got != 0 {
		t.Errorf("a nil snapshot reports age %v, want 0", got)
	}
}
