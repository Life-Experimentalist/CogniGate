package config

import (
	"testing"
	"time"
)

// TestShippedManifestMatchesTheSchema loads the repository's own
// cognigate.config.yml.
//
// It exists because the file is documentation that claims to be configuration.
// A key that this package does not parse — a renamed field, a section that was
// never wired up, a duration written in a form yaml cannot decode — is silently
// ignored at load time, so nothing else in the suite would notice the file
// drifting away from the schema it says it describes. The values asserted below
// are the ones the file writes out as the defaults; if a default changes, this
// fails until the manifest is updated to match.
func TestShippedManifestMatchesTheSchema(t *testing.T) {
	cfg, err := Load("../../../cognigate.config.yml")
	if err != nil {
		t.Fatalf("Load(cognigate.config.yml) = %v", err)
	}

	def := Default()
	cases := []struct {
		name      string
		got, want any
	}{
		{"gateway.port", cfg.Gateway.Port, def.Gateway.Port},
		{"analytics.timeout", cfg.Analytics.Timeout, def.Analytics.Timeout},
		{"catalog.ttl", cfg.Catalog.TTL, def.Catalog.TTL},
		{"catalog.stale_warn_after", cfg.Catalog.StaleWarnAfter, def.Catalog.StaleWarnAfter},
		{"routing.max_fallback_depth", cfg.Routing.MaxFallbackDepth, def.Routing.MaxFallbackDepth},
		{"routing.breaker.open_duration", cfg.Routing.Breaker.OpenDuration, def.Routing.Breaker.OpenDuration},
		{"quotas.enforcement", cfg.Quotas.Enforcement, def.Quotas.Enforcement},
		{"quotas.default_soft_threshold_pct", cfg.Quotas.DefaultSoftThresholdPct, def.Quotas.DefaultSoftThresholdPct},
		{"limits.max_request_bytes", cfg.Limits.MaxRequestBytes, def.Limits.MaxRequestBytes},
		{"limits.max_response_bytes", cfg.Limits.MaxResponseBytes, def.Limits.MaxResponseBytes},
		{"limits.request_timeout", cfg.Limits.RequestTimeout, def.Limits.RequestTimeout},
		{"limits.max_concurrent_per_key", cfg.Limits.MaxConcurrentPerKey, def.Limits.MaxConcurrentPerKey},
		{"rate_limit.requests_per_second", cfg.RateLimit.RequestsPerSecond, def.RateLimit.RequestsPerSecond},
		{"cache.enabled", cfg.Cache.Enabled, def.Cache.Enabled},
		{"cache.default_ttl", cfg.Cache.DefaultTTL, def.Cache.DefaultTTL},
		{"cache.max_entry_bytes", cfg.Cache.MaxEntryBytes, def.Cache.MaxEntryBytes},
		{"cache.max_bytes", cfg.Cache.MaxBytes, def.Cache.MaxBytes},
		{"webhooks.max_attempts", cfg.Webhooks.MaxAttempts, def.Webhooks.MaxAttempts},
		{"telemetry.buffer", cfg.Telemetry.Buffer, def.Telemetry.Buffer},
		{"health.cache_ttl", cfg.Health.CacheTTL, def.Health.CacheTTL},
		{"metrics.enabled", cfg.Metrics.Enabled, def.Metrics.Enabled},
		{"metrics.path", cfg.Metrics.Path, def.Metrics.Path},
		{"shutdown.drain_timeout", cfg.Shutdown.DrainTimeout, def.Shutdown.DrainTimeout},
		{"log.level", cfg.Log.Level, def.Log.Level},
		{"debug.max_capture_ttl", cfg.Debug.MaxTTL, def.Debug.MaxTTL},
		{"debug.default_sample_rate", cfg.Debug.DefaultSampleRate, def.Debug.DefaultSampleRate},
		{"debug.capture_sweep_interval", cfg.Debug.SweepInterval, def.Debug.SweepInterval},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// Durations are the failure mode worth naming: yaml decodes "10s" into a
	// time.Duration, but a value written as a bare number would decode as
	// nanoseconds and land nowhere near the default it was meant to restate.
	if cfg.Analytics.Timeout != 10*time.Second {
		t.Errorf("analytics.timeout decoded as %v; the manifest's durations are not being parsed as durations", cfg.Analytics.Timeout)
	}
}
