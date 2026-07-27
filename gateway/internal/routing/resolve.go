package routing

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/store"
)

// AliasNamePattern is the GW-2 grammar for an alias. Lowercase and
// hyphen/underscore only, so an alias can never be confused for a provider's
// model id (which carry dots, colons and capitals) and is safe in a URL path.
var AliasNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// SeededAliases are created with every tenant so that a caller has portable
// names to reach for on day one, before anyone has configured anything.
// Constraints, not pins: they should keep meaning the same thing as providers
// ship new models.
var SeededAliases = []store.Alias{
	{Name: "fast", CostTier: "cheapest", Capabilities: []string{"chat"}},
	{Name: "balanced", CostTier: "balanced", Capabilities: []string{"chat"}},
	{Name: "best", CostTier: "best", Capabilities: []string{"chat"}},
	{Name: "transcribe", Capabilities: []string{"transcription"}},
}

// Candidate is one resolved place a request can be sent.
type Candidate struct {
	Provider   string
	ProviderID string
	Model      string
	Entry      catalog.Entry
}

// Key is the candidate's breaker key.
func (c Candidate) Key() string { return Key(c.Provider, c.Model) }

// ServedBy is the value of the X-CogniGate-Served-By header.
func (c Candidate) ServedBy() string { return c.Provider + "/" + c.Model }

// Resolver turns the model a caller asked for into the ordered list of places
// to try (GW-2 aliases, then GW-3 fallback chains).
type Resolver struct {
	store    store.Store
	catalog  *catalog.Catalog
	maxDepth int
}

func NewResolver(s store.Store, c *catalog.Catalog, maxDepth int) *Resolver {
	if maxDepth < 1 {
		maxDepth = 5
	}
	return &Resolver{store: s, catalog: c, maxDepth: maxDepth}
}

// Resolve produces the candidate list for one request.
//
// The returned slice is never empty on a nil error, and is capped at the
// configured fallback depth. An entry in a configured chain that no provider
// currently serves is dropped rather than failing the request — that is exactly
// the case a fallback chain exists to survive.
func (r *Resolver) Resolve(ctx context.Context, tenantID, requested string) ([]Candidate, *catalog.Snapshot, error) {
	snap, err := r.catalog.Get(ctx, tenantID)
	if err != nil {
		return nil, nil, apierr.Unavailable("The model catalog is unavailable; no provider could be reached.").WithCause(err)
	}

	aliases, err := r.store.ListAliases(ctx, tenantID)
	if err != nil {
		return nil, snap, err
	}
	aliasByName := make(map[string]*store.Alias, len(aliases))
	for _, a := range aliases {
		aliasByName[a.Name] = a
	}

	chain, err := r.chainFor(ctx, tenantID, requested)
	if err != nil {
		return nil, snap, err
	}

	var (
		out  []Candidate
		seen = map[string]bool{}
		// requestedIsAlias decides which 404 to raise when nothing resolves:
		// an alias that matches no model is a configuration problem the caller
		// cannot see, and deserves its own code.
		requestedIsAlias = aliasByName[requested] != nil
	)

	for _, ref := range chain {
		if len(out) >= r.maxDepth {
			break
		}
		for _, cand := range resolveRef(ref, aliasByName, snap) {
			if seen[cand.ServedBy()] {
				continue
			}
			seen[cand.ServedBy()] = true
			out = append(out, cand)
			break // one candidate per chain position; the chain is the fallback
		}
	}

	if len(out) == 0 {
		if requestedIsAlias {
			return nil, snap, apierr.AliasUnresolvable(requested)
		}
		return nil, snap, apierr.ModelNotFound(requested)
	}
	return out, snap, nil
}

// chainFor returns the ordered references to try. A configured route wins; with
// no route, the chain is just what the caller asked for.
func (r *Resolver) chainFor(ctx context.Context, tenantID, requested string) ([]string, error) {
	routes, err := r.store.ListRoutes(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, rt := range routes {
		if rt.Match == requested && len(rt.Chain) > 0 {
			return rt.Chain, nil
		}
	}
	return []string{requested}, nil
}

// resolveRef expands one chain entry into the catalog entries it could mean,
// best first. An alias can expand to several so that a chain position still
// resolves when its first choice is missing from the catalog.
func resolveRef(ref string, aliases map[string]*store.Alias, snap *catalog.Snapshot) []Candidate {
	if a, ok := aliases[ref]; ok {
		return resolveAlias(a, snap)
	}
	if e, ok := snap.Lookup(ref); ok {
		return []Candidate{entryCandidate(e)}
	}
	return nil
}

// resolveAlias applies the GW-2 selection order: an explicit pin wins outright;
// otherwise filter by capability and context window, then order by the tenant's
// provider preference and the requested cost tier.
func resolveAlias(a *store.Alias, snap *catalog.Snapshot) []Candidate {
	if a.Pin != "" {
		if e, ok := snap.Lookup(a.Pin); ok {
			return []Candidate{entryCandidate(e)}
		}
		// A pin that no longer exists is a degraded alias. Fall through to the
		// constraints rather than failing: the alias still describes what the
		// caller wants, and answering is better than 404ing because one model
		// was retired.
	}

	var matches []catalog.Entry
	for _, e := range snap.Models {
		if !hasAll(e.Capabilities, a.Capabilities) {
			continue
		}
		if a.MinContextWindow > 0 && e.ContextWindow > 0 && e.ContextWindow < a.MinContextWindow {
			continue
		}
		matches = append(matches, e)
	}

	prefRank := make(map[string]int, len(a.ProviderPreference))
	for i, name := range a.ProviderPreference {
		prefRank[name] = i
	}
	rankOf := func(e catalog.Entry) int {
		if r, ok := prefRank[e.Provider]; ok {
			return r
		}
		// Unlisted providers sort after every listed one, without being
		// excluded: a preference is an ordering, not a whitelist.
		return len(a.ProviderPreference)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if ri, rj := rankOf(matches[i]), rankOf(matches[j]); ri != rj {
			return ri < rj
		}
		ci, cj := matches[i].InputCostPerMTok, matches[j].InputCostPerMTok
		switch a.CostTier {
		case "best":
			// Price is the only quality signal available across providers, so
			// "best" means the most expensive model that meets the
			// constraints. Crude, but it is honest about what it knows.
			if ci != cj {
				return ci > cj
			}
		case "cheapest", "balanced", "":
			if ci != cj {
				return ci < cj
			}
		}
		// Deterministic tie-break: the same request must resolve the same way
		// twice, or a cache key means nothing.
		return matches[i].ID < matches[j].ID
	})

	out := make([]Candidate, 0, len(matches))
	for _, e := range matches {
		out = append(out, entryCandidate(e))
	}
	return out
}

func entryCandidate(e catalog.Entry) Candidate {
	_, model := catalog.ProviderOf(e.ID)
	return Candidate{
		Provider:   e.Provider,
		ProviderID: e.ProviderID,
		Model:      model,
		Entry:      e,
	}
}

// hasAll reports whether every required capability is present.
func hasAll(have, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[strings.ToLower(h)] = true
	}
	for _, r := range required {
		if !set[strings.ToLower(r)] {
			return false
		}
	}
	return true
}

// ValidateChain enforces the GW-3 rule that a fallback chain names each model at
// most once. A repeated entry is always a mistake: the second attempt would hit
// the breaker the first one just tripped, so it can only ever waste a retry.
func ValidateChain(chain []string) error {
	seen := map[string]bool{}
	for _, m := range chain {
		if seen[m] {
			return apierr.FallbackDuplicate(m)
		}
		seen[m] = true
	}
	return nil
}
