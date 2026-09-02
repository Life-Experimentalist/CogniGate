package routing

import (
	"context"
	"sort"

	"github.com/cognigate/gateway/internal/apierr"
	"github.com/cognigate/gateway/internal/catalog"
	"github.com/cognigate/gateway/internal/store"
)

// Reasons a configured name is reported degraded by GET /v1/health.
//
// The first two are spelled the same as the error a request would get, so an
// operator reading a health report and an operator reading a failed response see
// one vocabulary rather than two. The last two have no request-time equivalent:
// both describe configuration that still serves, just not as written.
const (
	ReasonAliasUnresolvable = apierr.CodeAliasUnresolvable
	ReasonFallbackDuplicate = apierr.CodeFallbackDuplicate
	ReasonPinUnresolvable   = "pin_unresolvable"
	ReasonRuleUnresolvable  = "rule_unresolvable"
)

// NameState is how one configured name — an alias, or a routing rule's match —
// currently resolves. An empty Reason means it is serving.
type NameState struct {
	Name       string `json:"name"`
	State      string `json:"state"` // ok | degraded
	ResolvesTo string `json:"resolves_to,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Diagnosis is the GW-5 view of a tenant's routing configuration.
type Diagnosis struct {
	Aliases []NameState
	Rules   []NameState
}

// Degraded reports whether any configured name is not serving as written, which
// is one of the GW-5 triggers for an overall status of "degraded".
func (d Diagnosis) Degraded() bool {
	for _, s := range d.Aliases {
		if s.Reason != "" {
			return true
		}
	}
	for _, s := range d.Rules {
		if s.Reason != "" {
			return true
		}
	}
	return false
}

// Diagnose evaluates every alias and routing rule a tenant has configured
// against the live catalog.
//
// It exists because configuration that was valid when it was written can be
// invalidated later by a provider retiring a model, with nothing on the write
// path in a position to notice: GW-2 validates an alias against the catalog of
// the day it is created, and GW-3 rejects a chain that names a model twice, but
// neither can see a change that happens months afterwards. Health is where that
// shows up, which is why this walks the configuration rather than waiting for a
// request to fail against it.
//
// A store or catalog failure is returned rather than reported per name: the
// difference between "this alias is broken" and "we could not check" matters
// enough that guessing at it would make the report worse than no report.
func (r *Resolver) Diagnose(ctx context.Context, tenantID string) (Diagnosis, error) {
	snap, err := r.catalog.Get(ctx, tenantID)
	if err != nil {
		return Diagnosis{}, err
	}
	aliases, err := r.store.ListAliases(ctx, tenantID)
	if err != nil {
		return Diagnosis{}, err
	}
	routes, err := r.store.ListRoutes(ctx, tenantID)
	if err != nil {
		return Diagnosis{}, err
	}

	aliasByName := make(map[string]*store.Alias, len(aliases))
	for _, a := range aliases {
		aliasByName[a.Name] = a
	}

	d := Diagnosis{
		Aliases: make([]NameState, 0, len(aliases)),
		Rules:   make([]NameState, 0, len(routes)),
	}
	for _, a := range aliases {
		d.Aliases = append(d.Aliases, diagnoseAlias(a, snap))
	}
	for _, rt := range routes {
		d.Rules = append(d.Rules, diagnoseRoute(rt, aliasByName, snap))
	}

	// Sorted so a dashboard polling every few seconds does not reorder its rows
	// under the reader, since neither store returns a guaranteed order.
	sort.Slice(d.Aliases, func(i, j int) bool { return d.Aliases[i].Name < d.Aliases[j].Name })
	sort.Slice(d.Rules, func(i, j int) bool { return d.Rules[i].Name < d.Rules[j].Name })
	return d, nil
}

func diagnoseAlias(a *store.Alias, snap *catalog.Snapshot) NameState {
	st := NameState{Name: a.Name, State: "ok"}

	cands := resolveAlias(a, snap)
	if len(cands) == 0 {
		st.State = "degraded"
		st.Reason = ReasonAliasUnresolvable
		return st
	}
	st.ResolvesTo = cands[0].ServedBy()

	// A pin the catalog no longer carries still resolves, because resolveAlias
	// falls through to the constraints rather than failing. That is the right
	// request-time behaviour and the wrong thing to stay quiet about: the alias
	// is serving something other than what the operator pinned.
	if a.Pin != "" {
		if _, ok := snap.Lookup(a.Pin); !ok {
			st.State = "degraded"
			st.Reason = ReasonPinUnresolvable
		}
	}
	return st
}

func diagnoseRoute(rt *store.Route, aliases map[string]*store.Alias, snap *catalog.Snapshot) NameState {
	st := NameState{Name: rt.Match, State: "ok"}

	// The chain positions that still resolve, in order — which is the order a
	// request would actually try them, since Resolve drops the rest.
	var live []string
	for _, ref := range rt.Chain {
		if cands := resolveRef(ref, aliases, snap); len(cands) > 0 {
			live = append(live, cands[0].ServedBy())
		}
	}

	if len(live) == 0 {
		st.State = "degraded"
		st.Reason = ReasonRuleUnresolvable
		return st
	}
	st.ResolvesTo = live[0]

	// Positions that would be tried one after the other and land on the same
	// provider and model make the second one dead weight: it can only ever hit
	// the breaker the first one just tripped. GW-3 rejects this when the chain
	// names a model twice outright, but an alias edit can produce it later with
	// the chain itself unchanged, and nothing on the write path sees that.
	for i := 1; i < len(live); i++ {
		if live[i] == live[i-1] {
			st.State = "degraded"
			st.Reason = ReasonFallbackDuplicate
			break
		}
	}
	return st
}
