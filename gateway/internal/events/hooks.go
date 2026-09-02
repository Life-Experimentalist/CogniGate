package events

import (
	"context"

	"github.com/cognigate/gateway/internal/routing"
)

// Emitter is the sink a hook publishes to. *Dispatcher satisfies it, and so does
// any test double — which is the point: the wiring below is the part most likely
// to be wrong, so it must be assertable without standing up an HTTP endpoint.
type Emitter interface {
	Emit(ctx context.Context, tenantID, eventType string, data map[string]any)
}

// BreakerHook builds the routing.Breaker OnChange callback that turns a circuit
// breaker transition into a webhook (GW-5, GW-8.AC-5).
//
// Only transitions into open and into closed are published. Half-open is the
// breaker probing itself, and it re-enters that state on every cool-off that
// elapses under a continuing outage — publishing it would turn one dead provider
// into a webhook every openFor seconds, which is how an operator learns to
// ignore the channel.
func BreakerHook(e Emitter) func(key string, from, to routing.State) {
	if e == nil {
		return nil
	}
	return func(key string, from, to routing.State) {
		var typ string
		switch to {
		case routing.StateOpen:
			typ = BreakerOpened
		case routing.StateClosed:
			typ = BreakerClosed
		default:
			return
		}

		tenantID, provider, model := routing.SplitKey(key)
		if tenantID == "" {
			// An unattributable transition is dropped rather than delivered to
			// whoever happens to match an empty tenant.
			return
		}

		data := map[string]any{
			"provider":       provider,
			"model":          model,
			"state":          to.String(),
			"previous_state": from.String(),
		}

		// The callback runs while the breaker's lock is held (see
		// routing.Breaker.onChange), and Emit reads the tenant's webhooks from
		// the store — which is an HTTP call once the store is the analytics
		// service. Holding the breaker's lock across that would stall every
		// request routing through any provider, so the emit is handed off.
		go e.Emit(context.Background(), tenantID, typ, data)
	}
}

// CatalogHook builds the catalog.Options.OnChange callback that publishes model
// appearances and disappearances (GW-5).
//
// One event per model, matching the singular event names. The first snapshot for
// a tenant is not a change and never reaches here, so these are genuinely
// incremental: what arrives is a model a provider started or stopped serving,
// not the initial catalog load.
func CatalogHook(e Emitter) func(tenantID string, added, removed []string) {
	if e == nil {
		return nil
	}
	return func(tenantID string, added, removed []string) {
		if tenantID == "" {
			return
		}
		// Copied before the goroutine: the caller owns these slices and is free
		// to reuse them once it returns.
		add := append([]string(nil), added...)
		rem := append([]string(nil), removed...)

		// Hopped for the same reason as the breaker's: catalog.Get holds the
		// tenant's lock across OnChange, and it is called from the request path.
		go func() {
			ctx := context.Background()
			for _, id := range add {
				e.Emit(ctx, tenantID, CatalogModelAdded, map[string]any{"model": id})
			}
			for _, id := range rem {
				e.Emit(ctx, tenantID, CatalogModelRemoved, map[string]any{"model": id})
			}
		}()
	}
}
