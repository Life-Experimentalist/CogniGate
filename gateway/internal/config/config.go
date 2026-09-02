// Package config holds every knob the gateway reads, with the defaults the
// GW-1..GW-14 specifications name.
//
// Three layers, lowest precedence first: the defaults in Default(), the YAML
// file, then environment variables. Environment overrides exist because the
// container images ship one baked config and are steered per-deployment; they
// are also how secrets stay out of the YAML.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole surface. Durations are time.Duration so YAML can carry
// human strings ("1h", "30s") and be wrong loudly at load rather than quietly
// at request time.
type Config struct {
	Gateway   Gateway   `yaml:"gateway"`
	Admin     Admin     `yaml:"admin"`
	Analytics Analytics `yaml:"analytics"`
	Catalog   Catalog   `yaml:"catalog"`
	Routing   Routing   `yaml:"routing"`
	Quotas    Quotas    `yaml:"quotas"`
	Limits    Limits    `yaml:"limits"`
	RateLimit RateLimit `yaml:"rate_limit"`
	Cache     Cache     `yaml:"cache"`
	Webhooks  Webhooks  `yaml:"webhooks"`
	Telemetry Telemetry `yaml:"telemetry"`
	Health    Health    `yaml:"health"`
	Metrics   Metrics   `yaml:"metrics"`
	Shutdown  Shutdown  `yaml:"shutdown"`
	Log       Log       `yaml:"log"`
	Debug     Debug     `yaml:"debug"`
}

type Gateway struct {
	Port int `yaml:"port"`
}

// MinBootstrapKeyLen is the shortest admin bootstrap credential the gateway
// will accept. It is a floor on entropy, not a policy: anything shorter is
// almost certainly a placeholder copied out of an example file.
const MinBootstrapKeyLen = 16

type Admin struct {
	// BootstrapKey is the one credential that exists before any tenant does,
	// so an operator (and the conformance suite) can create the first one. Set
	// it from the environment; a value in a committed YAML file is a
	// credential in version control.
	BootstrapKey string `yaml:"bootstrap_key"`
}

// Analytics is the gateway's view of the JVM service. The gateway owns the
// public /admin/v1 surface and delegates durability here; when BaseURL is
// empty the gateway runs on its in-memory store instead (GW-11 --dev).
type Analytics struct {
	BaseURL string        `yaml:"base_url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type Catalog struct {
	TTL             time.Duration `yaml:"ttl"`
	StaleWarnAfter  time.Duration `yaml:"stale_warn_after"`
	ProviderTimeout time.Duration `yaml:"provider_timeout"`
}

type Routing struct {
	MaxFallbackDepth int     `yaml:"max_fallback_depth"`
	Breaker          Breaker `yaml:"breaker"`
}

// Breaker trips a provider+model pair out of rotation after ErrorThreshold
// failures inside Window, and keeps it out for OpenDuration.
type Breaker struct {
	ErrorThreshold int           `yaml:"error_threshold"`
	Window         time.Duration `yaml:"window"`
	OpenDuration   time.Duration `yaml:"open_duration"`
}

type Quotas struct {
	DefaultSoftThresholdPct int `yaml:"default_soft_threshold_pct"`
	// Enforcement is "on" (hard caps reject) or "observe" (hard caps only
	// emit events and headers). "observe" is how an operator sizes quotas
	// before turning them into rejections.
	Enforcement string `yaml:"enforcement"`
}

type Limits struct {
	MaxRequestBytes        int64         `yaml:"max_request_bytes"`
	MaxResponseBytes       int64         `yaml:"max_response_bytes"`
	RequestTimeout         time.Duration `yaml:"request_timeout"`
	UpstreamConnectTimeout time.Duration `yaml:"upstream_connect_timeout"`
	StreamIdleTimeout      time.Duration `yaml:"stream_idle_timeout"`
	MaxConcurrentPerKey    int           `yaml:"max_concurrent_per_key"`
}

type RateLimit struct {
	RequestsPerSecond int `yaml:"requests_per_second"`
	BurstCapacity     int `yaml:"burst_capacity"`
}

type Cache struct {
	Enabled       bool          `yaml:"enabled"`
	DefaultTTL    time.Duration `yaml:"default_ttl"`
	MaxTTL        time.Duration `yaml:"max_ttl"`
	MaxEntryBytes int64         `yaml:"max_entry_bytes"`
}

type Webhooks struct {
	MaxAttempts int           `yaml:"max_attempts"`
	Timeout     time.Duration `yaml:"timeout"`
}

// Telemetry.Buffer bounds the queue of usage records awaiting the analytics
// service. Full means drop with a warning: metering is not worth stalling the
// data plane for (GW-11).
type Telemetry struct {
	Buffer int `yaml:"buffer"`
}

type Health struct {
	CacheTTL time.Duration `yaml:"cache_ttl"`
}

type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
	// Token, when set, requires a bearer token on the metrics path. Empty
	// leaves /metrics unauthenticated, which is the default because that is
	// what a Prometheus scrape in a private network expects.
	Token string `yaml:"token"`
}

type Shutdown struct {
	DrainTimeout time.Duration `yaml:"drain_timeout"`
}

type Log struct {
	Level string `yaml:"level"`
}

// Debug governs GW-14 capture. MaxTTL is a ceiling the admin API refuses to
// exceed: captured prompts are the most sensitive thing the gateway can hold,
// so they expire in days, not indefinitely.
type Debug struct {
	MaxTTL            time.Duration `yaml:"max_capture_ttl"`
	DefaultSampleRate float64       `yaml:"default_sample_rate"`
}

// Default returns the specification defaults. Every field is set here; loading
// a YAML file that omits a key leaves the default in place rather than a zero.
func Default() Config {
	return Config{
		Gateway: Gateway{Port: 8080},
		Admin:   Admin{},
		Analytics: Analytics{
			Timeout: 10 * time.Second,
		},
		Catalog: Catalog{
			TTL:             time.Hour,
			StaleWarnAfter:  6 * time.Hour,
			ProviderTimeout: 10 * time.Second,
		},
		Routing: Routing{
			MaxFallbackDepth: 5,
			Breaker: Breaker{
				ErrorThreshold: 5,
				Window:         30 * time.Second,
				OpenDuration:   60 * time.Second,
			},
		},
		Quotas: Quotas{
			DefaultSoftThresholdPct: 80,
			Enforcement:             "on",
		},
		Limits: Limits{
			MaxRequestBytes:        2 * 1024 * 1024,
			MaxResponseBytes:       8 * 1024 * 1024,
			RequestTimeout:         120 * time.Second,
			UpstreamConnectTimeout: 10 * time.Second,
			StreamIdleTimeout:      60 * time.Second,
			MaxConcurrentPerKey:    32,
		},
		RateLimit: RateLimit{RequestsPerSecond: 50, BurstCapacity: 100},
		Cache: Cache{
			Enabled:       false,
			DefaultTTL:    5 * time.Minute,
			MaxTTL:        24 * time.Hour,
			MaxEntryBytes: 256 * 1024,
		},
		Webhooks:  Webhooks{MaxAttempts: 5, Timeout: 10 * time.Second},
		Telemetry: Telemetry{Buffer: 1000},
		Health:    Health{CacheTTL: 2 * time.Second},
		Metrics:   Metrics{Enabled: true, Path: "/metrics"},
		Shutdown:  Shutdown{DrainTimeout: 30 * time.Second},
		Log:       Log{Level: "info"},
		Debug:     Debug{MaxTTL: 72 * time.Hour, DefaultSampleRate: 0.01},
	}
}

// Load reads path over the defaults, then applies environment overrides. A
// missing file is not an error — the defaults plus environment are a complete
// configuration, which is what the container images rely on.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		case !os.IsNotExist(err):
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv layers CG_-prefixed environment variables over the file. Only the
// values a deployment actually varies are wired: secrets, endpoints, and the
// log level. Everything else belongs in the file, where it is reviewable.
func applyEnv(cfg *Config) {
	envStr("PORT", func(v string) {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gateway.Port = n
		}
	})
	envStr("ADMIN_BOOTSTRAP_KEY", func(v string) { cfg.Admin.BootstrapKey = v })
	envStr("ANALYTICS_URL", func(v string) { cfg.Analytics.BaseURL = v })
	envStr("ANALYTICS_TOKEN", func(v string) { cfg.Analytics.Token = v })
	envStr("METRICS_TOKEN", func(v string) { cfg.Metrics.Token = v })
	envStr("LOG_LEVEL", func(v string) { cfg.Log.Level = v })
	envStr("QUOTA_ENFORCEMENT", func(v string) { cfg.Quotas.Enforcement = v })
	envStr("CACHE_ENABLED", func(v string) {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Cache.Enabled = b
		}
	})
}

// envStr calls set with the first non-empty of CG_<name> or <name>. The
// unprefixed spelling exists because PORT is a near-universal container
// convention and operators reach for it first.
func envStr(name string, set func(string)) {
	for _, key := range []string{"CG_" + name, name} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			set(v)
			return
		}
	}
}

// Validate rejects a configuration that would misbehave subtly at request time
// — a zero timeout that hangs forever, a negative limit that rejects
// everything. Failing at startup makes the misconfiguration obvious.
func (c Config) Validate() error {
	if c.Gateway.Port < 1 || c.Gateway.Port > 65535 {
		return fmt.Errorf("gateway.port %d out of range", c.Gateway.Port)
	}
	// Empty is allowed — a deployment may choose to have no bootstrap credential
	// at all and provision keys some other way. A short one is not: it is a
	// placeholder that the admin plane would silently refuse on every request,
	// leaving an operator to debug 401s from a key they believe they configured.
	if n := len(c.Admin.BootstrapKey); n > 0 && n < MinBootstrapKeyLen {
		return fmt.Errorf("admin.bootstrap_key is %d characters; at least %d are required",
			n, MinBootstrapKeyLen)
	}
	if c.Routing.MaxFallbackDepth < 1 {
		return fmt.Errorf("routing.max_fallback_depth must be at least 1")
	}
	if c.Limits.MaxRequestBytes < 1 {
		return fmt.Errorf("limits.max_request_bytes must be positive")
	}
	if c.Limits.MaxResponseBytes < 1 {
		return fmt.Errorf("limits.max_response_bytes must be positive")
	}
	if c.Limits.RequestTimeout <= 0 {
		return fmt.Errorf("limits.request_timeout must be positive")
	}
	if c.Limits.MaxConcurrentPerKey < 1 {
		return fmt.Errorf("limits.max_concurrent_per_key must be at least 1")
	}
	switch c.Quotas.Enforcement {
	case "on", "observe":
	default:
		return fmt.Errorf("quotas.enforcement must be \"on\" or \"observe\", got %q", c.Quotas.Enforcement)
	}
	if p := c.Quotas.DefaultSoftThresholdPct; p < 1 || p > 100 {
		return fmt.Errorf("quotas.default_soft_threshold_pct %d out of range 1..100", p)
	}
	if c.Cache.DefaultTTL > c.Cache.MaxTTL {
		return fmt.Errorf("cache.default_ttl exceeds cache.max_ttl")
	}
	if c.Telemetry.Buffer < 1 {
		return fmt.Errorf("telemetry.buffer must be at least 1")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	if c.Debug.DefaultSampleRate < 0 || c.Debug.DefaultSampleRate > 1 {
		return fmt.Errorf("debug.default_sample_rate must be within 0..1")
	}
	return nil
}
