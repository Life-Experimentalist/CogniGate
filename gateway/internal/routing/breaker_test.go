package routing

import "testing"

// State has two published spellings and one published number, and none of the
// three agrees with the constants' own values. That is deliberate — see the
// comments on Gauge and Health — which is exactly why it needs pinning: the
// cheapest available "cleanup" is to renumber the constants so the translation
// can be deleted, and nothing else would notice.

func TestStateGaugeUsesTheSpecifiedEncoding(t *testing.T) {
	// GW-8 fixes cognigate_breaker_state at 0 closed, 1 half-open, 2 open.
	cases := map[State]float64{
		StateClosed:   0,
		StateHalfOpen: 1,
		StateOpen:     2,
	}
	for state, want := range cases {
		if got := state.Gauge(); got != want {
			t.Errorf("%s exports as %v, want %v", state, got, want)
		}
	}

	// The point of the translation: the constant's own value is not the gauge's.
	if float64(StateOpen) == StateOpen.Gauge() {
		t.Error("StateOpen's constant and its gauge value have converged, so the translation is no longer doing anything and a renumbering has gone unnoticed")
	}
}

func TestStateSpellings(t *testing.T) {
	// String feeds the breaker.opened and breaker.closed event payloads; Health
	// feeds the admin health rows. They differ only in the hyphen, and a change
	// to either is a change to a published contract.
	cases := []struct {
		state  State
		str    string
		health string
	}{
		{StateClosed, "closed", "closed"},
		{StateOpen, "open", "open"},
		{StateHalfOpen, "half_open", "half-open"},
	}
	for _, c := range cases {
		if got := c.state.String(); got != c.str {
			t.Errorf("String() is %q, want %q", got, c.str)
		}
		if got := c.state.Health(); got != c.health {
			t.Errorf("Health() is %q, want %q", got, c.health)
		}
	}
}

func TestKeyRoundTrips(t *testing.T) {
	cases := []struct {
		name     string
		tenant   string
		provider string
		model    string
	}{
		{"plain", "ten_1", "openai", "gpt-4o-mini"},
		// A catalog id may be qualified, so the model has to take everything
		// after the second separator rather than being one segment.
		{"qualified model", "ten_1", "openrouter", "anthropic/claude-3-haiku"},
		{"twice qualified", "ten_1", "proxy", "a/b/c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tenant, provider, model := SplitKey(Key(c.tenant, c.provider, c.model))
			if tenant != c.tenant || provider != c.provider || model != c.model {
				t.Errorf("round trip gave (%q, %q, %q), want (%q, %q, %q)",
					tenant, provider, model, c.tenant, c.provider, c.model)
			}
		})
	}
}

func TestSplitKeyOnMalformedInput(t *testing.T) {
	// The metric observer splits whatever the breaker hands it. A key that is
	// not in the expected shape must not panic or silently produce a label that
	// looks like a tenant id.
	tenant, provider, model := SplitKey("not-a-key")
	if tenant != "" || provider != "" || model != "not-a-key" {
		t.Errorf("SplitKey gave (%q, %q, %q), want the whole string as the model with the rest empty", tenant, provider, model)
	}
}

func TestBlockRankOrdersByHowMuchTrafficIsStopped(t *testing.T) {
	if !(BlockRank(StateClosed) < BlockRank(StateHalfOpen) && BlockRank(StateHalfOpen) < BlockRank(StateOpen)) {
		t.Errorf("BlockRank is not ordered: closed=%d half-open=%d open=%d",
			BlockRank(StateClosed), BlockRank(StateHalfOpen), BlockRank(StateOpen))
	}
}
