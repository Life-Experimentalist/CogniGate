package routing

import (
	"net/http"
	"testing"
	"time"
)

// The clock the cooldown tests run on. Fixed, so a parked key's window is
// decided by the test rather than by how long the test took to run.
var testNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func newCooldownDispatcher() *Dispatcher {
	d := newTestDispatcher()
	d.now = func() time.Time { return testNow }
	return d
}

func TestRetryAfterReadsBothFormsAndIgnoresTheRest(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"delay in seconds", "30", 30 * time.Second},
		{"http date", testNow.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second},
		{"date in the past", testNow.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"nonsense", "soon", 0},
		{"absent", "", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.value != "" {
				h.Set("Retry-After", tc.value)
			}
			if got := retryAfter(h, testNow); got != tc.want {
				t.Fatalf("Retry-After %q: got %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// The point of parking a key: once a provider has said how long its limit
// lasts, the next request spends no round trip rediscovering it.
func TestAParkedKeyIsSkippedUntilItsWindowPasses(t *testing.T) {
	d := newCooldownDispatcher()
	order := []string{"a", "b", "c"}

	d.coolKey("prov_test", "a", testNow.Add(30*time.Second))

	got := d.readyKeys("prov_test", order)
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("parked key still offered: %v", got)
	}

	// A key parked on one provider says nothing about the same string used as a
	// key on another.
	if got := d.readyKeys("prov_other", order); len(got) != 3 {
		t.Fatalf("cooldown leaked across providers: %v", got)
	}

	d.now = func() time.Time { return testNow.Add(time.Minute) }
	if got := d.readyKeys("prov_test", order); len(got) != 3 {
		t.Fatalf("key not released after its window: %v", got)
	}
	if len(d.cooldowns) != 0 {
		t.Fatalf("expired entry not cleared: %v", d.cooldowns)
	}
}

// A window is the provider's estimate, not a fact. If every key is inside one,
// the pool is tried anyway rather than failing a request the provider might
// now accept.
func TestAWhollyParkedPoolIsStillTried(t *testing.T) {
	d := newCooldownDispatcher()
	order := []string{"a", "b"}

	for _, k := range order {
		d.coolKey("prov_test", k, testNow.Add(time.Hour))
	}

	if got := d.readyKeys("prov_test", order); len(got) != 2 {
		t.Fatalf("got %v, want the whole pool", got)
	}
}

// Two 429s for the same key must not let the shorter window shorten the longer
// one, or a key the provider parked for an hour comes back in a second.
func TestTheLongestWindowWins(t *testing.T) {
	d := newCooldownDispatcher()

	d.coolKey("prov_test", "a", testNow.Add(time.Hour))
	d.coolKey("prov_test", "a", testNow.Add(time.Second))

	if got := d.cooldowns[poolKey{"prov_test", "a"}]; !got.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("got %v, want %v", got, testNow.Add(time.Hour))
	}
}
