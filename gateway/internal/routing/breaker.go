package routing

import (
	"sync"
	"time"
)

// State is a circuit breaker's position. The numeric values are the ones the
// cognigate_breaker_state gauge exports, so they are part of the metrics
// contract and must not be renumbered.
type State int

const (
	StateClosed   State = 0 // healthy; traffic flows
	StateOpen     State = 1 // failing; traffic is skipped with no upstream call
	StateHalfOpen State = 2 // probing; exactly one request is let through
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// Breaker trips a provider+model pair out of rotation once it has failed often
// enough, and keeps it out long enough for the upstream to recover.
//
// The point is not to save the failing request — that one has already failed.
// It is to stop the next several hundred requests from each paying a full
// timeout against a provider that is known to be down, which is what turns one
// provider's outage into a gateway-wide latency collapse.
type Breaker struct {
	mu      sync.Mutex
	entries map[string]*breakerEntry

	threshold int
	window    time.Duration
	openFor   time.Duration

	// OnChange fires on every state transition, for the breaker.opened and
	// breaker.closed events. It runs while the lock is held, so it must not
	// call back into the breaker.
	onChange func(key string, from, to State)

	now func() time.Time
}

type breakerEntry struct {
	failures []time.Time
	state    State
	openedAt time.Time
	// probing marks that a half-open probe is in flight, so a burst of
	// concurrent requests sends exactly one request to a recovering provider
	// rather than all of them.
	probing bool
}

// NewBreaker builds a breaker. Zero or negative settings fall back to the GW-3
// defaults rather than producing a breaker that trips instantly or never.
func NewBreaker(threshold int, window, openFor time.Duration, onChange func(key string, from, to State)) *Breaker {
	if threshold < 1 {
		threshold = 5
	}
	if window <= 0 {
		window = 30 * time.Second
	}
	if openFor <= 0 {
		openFor = 60 * time.Second
	}
	return &Breaker{
		entries:   map[string]*breakerEntry{},
		threshold: threshold,
		window:    window,
		openFor:   openFor,
		onChange:  onChange,
		now:       time.Now,
	}
}

// Key is the breaker's unit of isolation: one provider's failure on one model
// must not take that provider's other models out of rotation.
func Key(providerName, model string) string { return providerName + "/" + model }

// Allow reports whether a candidate may be tried. A false result means the
// candidate is skipped with zero upstream calls, which is what GW-3 requires an
// open breaker to cost.
func (b *Breaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok {
		return true
	}
	now := b.now()

	switch e.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(e.openedAt) < b.openFor {
			return false
		}
		// The cool-off has elapsed. Move to half-open and let this caller be
		// the probe.
		b.transition(key, e, StateHalfOpen)
		e.probing = true
		return true
	case StateHalfOpen:
		// A probe is already in flight; everyone else keeps waiting.
		if e.probing {
			return false
		}
		e.probing = true
		return true
	default:
		return true
	}
}

// State reports the current position, for the gauge and for /v1/health.
func (b *Breaker) State(key string) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[key]
	if !ok {
		return StateClosed
	}
	// An open breaker whose cool-off has elapsed is reported as half-open even
	// before a probe arrives, so the gauge does not claim a provider is still
	// being skipped when the next request will in fact try it.
	if e.state == StateOpen && b.now().Sub(e.openedAt) >= b.openFor {
		return StateHalfOpen
	}
	return e.state
}

// Snapshot returns every non-closed breaker, for GET /v1/health.
func (b *Breaker) Snapshot() map[string]State {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]State{}
	now := b.now()
	for key, e := range b.entries {
		state := e.state
		if state == StateOpen && now.Sub(e.openedAt) >= b.openFor {
			state = StateHalfOpen
		}
		if state != StateClosed {
			out[key] = state
		}
	}
	return out
}

// Success clears the failure history. A half-open probe that succeeds closes
// the breaker outright rather than decrementing a counter: the provider has
// demonstrably recovered.
func (b *Breaker) Success(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok {
		return
	}
	e.probing = false
	e.failures = nil
	if e.state != StateClosed {
		b.transition(key, e, StateClosed)
	}
}

// Failure records an upstream failure and opens the breaker once the threshold
// is reached inside the window. A failed half-open probe re-opens immediately —
// one confirmation is enough, because the alternative is to spend another
// threshold's worth of requests re-proving what the probe just showed.
func (b *Breaker) Failure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e, ok := b.entries[key]
	if !ok {
		e = &breakerEntry{state: StateClosed}
		b.entries[key] = e
	}
	now := b.now()

	if e.state == StateHalfOpen {
		e.probing = false
		e.openedAt = now
		b.transition(key, e, StateOpen)
		return
	}

	// Drop failures that have aged out, so a slow trickle of errors spread over
	// hours never accumulates into a trip.
	cutoff := now.Add(-b.window)
	kept := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.failures = append(kept, now)

	if len(e.failures) >= b.threshold && e.state == StateClosed {
		e.openedAt = now
		b.transition(key, e, StateOpen)
	}
}

// transition changes state and notifies. Called with the lock held.
func (b *Breaker) transition(key string, e *breakerEntry, to State) {
	from := e.state
	if from == to {
		return
	}
	e.state = to
	if to == StateClosed {
		e.probing = false
	}
	if b.onChange != nil {
		b.onChange(key, from, to)
	}
}
