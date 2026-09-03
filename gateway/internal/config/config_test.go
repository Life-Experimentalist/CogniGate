package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the shipped defaults do not validate: %v", err)
	}
}

// A bootstrap key shorter than the floor is refused at startup rather than at
// every request. Accepting it would leave an operator debugging 401s from a
// credential they can see in their own configuration.
func TestValidateRejectsAShortBootstrapKey(t *testing.T) {
	cfg := Default()
	cfg.Admin.BootstrapKey = "replace_me"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a 10-character bootstrap key was accepted")
	}
	if !strings.Contains(err.Error(), "admin.bootstrap_key") {
		t.Errorf("error %q does not name the setting at fault", err)
	}
}

// Empty is a legitimate choice: a deployment may provision its first key some
// other way, and there is nothing insecure about having no bootstrap credential.
func TestValidateAcceptsNoBootstrapKey(t *testing.T) {
	cfg := Default()
	cfg.Admin.BootstrapKey = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("an absent bootstrap key was rejected: %v", err)
	}
}

func TestValidateAcceptsABootstrapKeyAtTheFloor(t *testing.T) {
	cfg := Default()
	cfg.Admin.BootstrapKey = strings.Repeat("k", MinBootstrapKeyLen)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a %d-character bootstrap key was rejected: %v", MinBootstrapKeyLen, err)
	}
}

// The reference deployment passes CG_ADMIN_BOOTSTRAP_KEY, so this is the wiring
// docker-compose.yml and the CI smoke test both depend on.
func TestPrefixedEnvironmentWiresTheBootstrapKey(t *testing.T) {
	t.Setenv("CG_ADMIN_BOOTSTRAP_KEY", "a-bootstrap-key-from-the-environment")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.BootstrapKey != "a-bootstrap-key-from-the-environment" {
		t.Errorf("bootstrap key = %q, want the value from CG_ADMIN_BOOTSTRAP_KEY", cfg.Admin.BootstrapKey)
	}
}

// The unprefixed spelling is honoured too, but the prefixed one wins: an
// operator who set both meant the one that names this program.
func TestThePrefixedSpellingWins(t *testing.T) {
	t.Setenv("CG_ADMIN_BOOTSTRAP_KEY", "the-prefixed-bootstrap-key")
	t.Setenv("ADMIN_BOOTSTRAP_KEY", "the-bare-bootstrap-key")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admin.BootstrapKey != "the-prefixed-bootstrap-key" {
		t.Errorf("bootstrap key = %q, want the CG_-prefixed value", cfg.Admin.BootstrapKey)
	}
}

// A missing file is not an error: running on defaults plus the environment is
// the documented way to start, and --config is optional.
func TestLoadTreatsAMissingFileAsDefaults(t *testing.T) {
	cfg, err := Load("does-not-exist.yml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gateway.Port != Default().Gateway.Port {
		t.Errorf("port = %d, want the default %d", cfg.Gateway.Port, Default().Gateway.Port)
	}
}

func TestFileValuesOverrideDefaultsAndEnvironmentOverridesTheFile(t *testing.T) {
	path := t.TempDir() + "/cognigate.yml"
	if err := os.WriteFile(path, []byte("gateway:\n  port: 9090\nlog:\n  level: warn\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	t.Setenv("CG_LOG_LEVEL", "error")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gateway.Port != 9090 {
		t.Errorf("port = %d, want 9090 from the file", cfg.Gateway.Port)
	}
	if cfg.Log.Level != "error" {
		t.Errorf("log level = %q, want %q from the environment", cfg.Log.Level, "error")
	}
}

func TestValidateRejectsAnUnknownEnforcementMode(t *testing.T) {
	cfg := Default()
	cfg.Quotas.Enforcement = "sometimes"

	if err := cfg.Validate(); err == nil {
		t.Fatal("an unknown quota enforcement mode was accepted")
	}
}

// GW-14 puts the 72 h retention ceiling in documentation a downstream project
// quotes verbatim, so it has to hold for every deployment, not just the one
// that left the default alone. Raising it is refused at startup; lowering it is
// an operator's to do.
func TestValidateRefusesACaptureTTLCeilingAboveTheSpecifiedMaximum(t *testing.T) {
	cfg := Default()
	cfg.Debug.MaxTTL = MaxCaptureTTLCeiling + time.Hour

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a capture TTL ceiling above 72h was accepted")
	}
	if !strings.Contains(err.Error(), "max_capture_ttl") {
		t.Errorf("error = %q, want it to name the offending key", err)
	}
}

func TestValidateAcceptsALoweredCaptureTTLCeiling(t *testing.T) {
	cfg := Default()
	cfg.Debug.MaxTTL = time.Hour
	cfg.Debug.DefaultTTL = time.Minute

	if err := cfg.Validate(); err != nil {
		t.Fatalf("a ceiling below the maximum was refused: %v", err)
	}
}
