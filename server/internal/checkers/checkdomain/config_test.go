package checkdomain

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestDomainConfig_LegacyAlias(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DomainConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"domain":        "example.com",
		"thresholdDays": float64(14),
	}))
	r.NoError(cfg.Validate())

	// Legacy threshold maps onto the critical tier.
	r.Equal(14, cfg.CriticalDays)

	warning, critical := cfg.effectiveThresholds()
	r.Equal(14, critical)
	r.Equal(30, warning) // default warning, clamped up to >= critical
}

func TestDomainConfig_LegacySnakeCaseThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Stored rows use the DB/JSON "threshold_days" key (snake_case) —
	// exactly what GetConfig emits and what older rows persisted.
	cfg := &DomainConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"domain":         "example.com",
		"threshold_days": float64(20),
	}))
	r.NoError(cfg.Validate())

	r.Equal(20, cfg.CriticalDays)

	warning, critical := cfg.effectiveThresholds()
	r.Equal(20, critical)
	r.Equal(30, warning)
}

func TestDomainConfig_Validate_WarningBelowCritical(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DomainConfig{
		Domain:       "example.com",
		WarningDays:  7,
		CriticalDays: 30,
	}
	err := cfg.Validate()
	r.Error(err)
	r.Contains(err.Error(), "warningDays")
}

func TestDomainConfig_Validate_TwoTierValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DomainConfig{
		Domain:       "example.com",
		WarningDays:  30,
		CriticalDays: 7,
	}
	r.NoError(cfg.Validate())
}

func TestDomainConfig_Validate_NegativeRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Error((&DomainConfig{Domain: "example.com", CriticalDays: -1}).Validate())
	r.Error((&DomainConfig{Domain: "example.com", WarningDays: -1}).Validate())
}

func TestDomainConfig_GetConfig_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &DomainConfig{Domain: "example.com", WarningDays: 30, CriticalDays: 7}
	exported := cfg.GetConfig()

	r.Equal(7, exported["threshold_days"])
	r.Equal(7, exported["criticalDays"])
	r.Equal(30, exported["warningDays"])

	roundTripped := &DomainConfig{}
	r.NoError(roundTripped.FromMap(exported))
	r.Equal(7, roundTripped.CriticalDays)
	r.Equal(30, roundTripped.WarningDays)
}

// TestDomainChecker_Execute_OldConfigRoundTripsToSameBehavior proves the
// backward-compat requirement end to end: a check row persisted before this
// spec (only "threshold_days" in its stored config, no warningDays/
// criticalDays) still resolves to the SAME Down-at-threshold behavior as
// before via Execute, not just via config parsing in isolation.
func TestDomainChecker_Execute_OldConfigRoundTripsToSameBehavior(t *testing.T) {
	t.Parallel()

	srv := newFakeRDAPServer(t, []string{"com"}, rdapSuccessHandler(time.Now().AddDate(0, 0, 20), "Legacy Registrar"))
	whoisFunc, whoisCalls := countingWhoisFunc("")

	// Simulate decoding a legacy stored row: only the old snake_case key present.
	cfg := &DomainConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{
		"domain":         "example.com",
		"threshold_days": float64(30),
	}))

	checker := &DomainChecker{rdapClient: srv.client(), whoisFunc: whoisFunc}

	result, err := checker.Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, int32(0), whoisCalls.Load())

	// 20 days remaining <= legacy threshold (30) → Down, exactly as the old
	// single-threshold comparison would have produced.
	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, checkerdef.SeverityCritical, result.Output["severity"])
}
