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

// The system instruction is the one value here that is prose rather than a
// number, and the one an operator is most likely to set from the environment
// rather than the file: it changes per deployment of the same image.
func TestTheSystemInstructionComesFromTheFileAndTheEnvironmentOverridesIt(t *testing.T) {
	path := t.TempDir() + "/cognigate.yml"
	body := "chat:\n  system_instruction: \"from the file\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.SystemInstruction != "from the file" {
		t.Errorf("system instruction = %q, want the file's", cfg.Chat.SystemInstruction)
	}

	t.Setenv("CG_CHAT_SYSTEM_INSTRUCTION", "from the environment")
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.SystemInstruction != "from the environment" {
		t.Errorf("system instruction = %q, want the environment's", cfg.Chat.SystemInstruction)
	}
}

// Nothing is added by default, and that is the property every existing
// deployment depends on: the body it sends is the body the provider receives.
func TestNoSystemInstructionByDefault(t *testing.T) {
	if got := Default().Chat.SystemInstruction; got != "" {
		t.Errorf("default system instruction = %q, want none", got)
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

// The default has to be the mode that changes nothing: an operator who never
// heard of this setting bills exactly what the provider charged.
func TestDefaultBillingChargesTheProviderRate(t *testing.T) {
	if got := Default().Billing.Charge(1.25); got != 1.25 {
		t.Errorf("charge = %v, want the cost itself under the default mode", got)
	}
}

func TestMarkupAddsItsPercentageAndAbsorbChargesNothing(t *testing.T) {
	markup := Billing{Mode: BillingMarkup, MarkupPct: 20}
	if got := markup.Charge(10); got != 12 {
		t.Errorf("charge = %v, want 12 from a 20%% markup on 10", got)
	}
	absorb := Billing{Mode: BillingAbsorb}
	if got := absorb.Charge(10); got != 0 {
		t.Errorf("charge = %v, want 0 when the operator absorbs the bill", got)
	}
}

// A model with no configured rate costs zero, and no mode may turn that into a
// charge: a margin on an unknown rate is an invented number, and it would flow
// into billing.
func TestNoRateMeansNoChargeInEveryMode(t *testing.T) {
	for _, b := range []Billing{
		{Mode: BillingPassthrough},
		{Mode: BillingMarkup, MarkupPct: 40},
		{Mode: BillingAbsorb},
	} {
		if got := b.Charge(0); got != 0 {
			t.Errorf("%s charged %v for a request that cost nothing", b.Mode, got)
		}
	}
}

// The default has to publish the figure the gateway actually computed. A
// deployment that never heard of cost_visibility reports cost exactly.
func TestDefaultCostVisibilityIsExact(t *testing.T) {
	if got := Default().Billing.TenantCost(1.8734); got != 1.8734 {
		t.Errorf("tenant cost = %v, want the exact figure under the default", got)
	}
}

func TestHintRoundsToOneSignificantFigure(t *testing.T) {
	b := Billing{Mode: BillingMarkup, MarkupPct: 15, CostVisibility: CostHint}
	for _, tc := range []struct {
		cost float64
		want float64
	}{
		{1.8734, 2},
		{0.0018734, 0.002},
		{14.2, 10},
		{0, 0},
	} {
		if got := b.TenantCost(tc.cost); got != tc.want {
			t.Errorf("hint of %v = %v, want %v", tc.cost, got, tc.want)
		}
	}
}

// The hint blurs what the traffic cost the operator. It must never touch what
// the tenant owes: a figure someone is billed on cannot be approximate.
func TestHintDoesNotChangeTheCharge(t *testing.T) {
	b := Billing{Mode: BillingMarkup, MarkupPct: 15, CostVisibility: CostHint}
	if got := b.Charge(1.8734); got != 1.8734*1.15 {
		t.Errorf("charge = %v, want the exact charge regardless of cost visibility", got)
	}
}

func TestValidateRejectsAnUnknownCostVisibility(t *testing.T) {
	cfg := Default()
	cfg.Billing.CostVisibility = "vague"

	if err := cfg.Validate(); err == nil {
		t.Fatal("an unknown billing.cost_visibility was accepted")
	}
}

func TestValidateRejectsAnUnknownBillingMode(t *testing.T) {
	cfg := Default()
	cfg.Billing.Mode = "commission"

	if err := cfg.Validate(); err == nil {
		t.Fatal("an unknown billing mode was accepted")
	}
}

// Markup mode without a percentage is a configuration that says it charges a
// margin and then charges none, which is worth refusing rather than silently
// behaving as passthrough.
func TestValidateRejectsMarkupModeWithoutAPercentage(t *testing.T) {
	cfg := Default()
	cfg.Billing.Mode = BillingMarkup

	if err := cfg.Validate(); err == nil {
		t.Fatal("markup mode with no markup_pct was accepted")
	}
}
