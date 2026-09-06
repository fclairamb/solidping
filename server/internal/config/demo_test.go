package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestDemoConfigEnvBinding covers §7's env table. Every one of these keys is
// multi-word, which is exactly the trap koanf's SP_* loader falls into (it
// collapses underscores to dots), so this is the test that would catch
// SP_DEMO_ORG_SLUG silently binding nothing.
func TestDemoConfigEnvBinding(t *testing.T) {
	t.Setenv("SP_DEMO_ENABLED", "true")
	t.Setenv("SP_DEMO_ORG_SLUG", "showroom")
	t.Setenv("SP_DEMO_EMAIL", "visitor@acme.com")
	t.Setenv("SP_DEMO_PASSWORD", "look-around")
	t.Setenv("SP_DEMO_CHECK_TTL", "2h")
	t.Setenv("SP_DEMO_CLEANUP_INTERVAL", "10m")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.True(t, cfg.Demo.Enabled)
	require.Equal(t, "showroom", cfg.Demo.ResolvedOrgSlug())
	require.Equal(t, "visitor@acme.com", cfg.Demo.ResolvedEmail())
	require.Equal(t, "look-around", cfg.Demo.ResolvedPassword())
	require.Equal(t, 2*time.Hour, cfg.Demo.ResolvedCheckTTL())
	require.Equal(t, 10*time.Minute, cfg.Demo.ResolvedCleanupInterval())
}

// TestDemoIsOffByDefault is the self-hosted protection, asserted at the
// configuration layer: pulling a new image must never turn a private instance
// into one with a world-readable account on it.
func TestDemoIsOffByDefault(t *testing.T) {
	cfg, err := config.Load()
	require.NoError(t, err)

	require.False(t, cfg.Demo.Enabled)
}

// TestTestRunModeForcesTheDemoOn covers the one exception, which the E2E suite
// depends on.
func TestTestRunModeForcesTheDemoOn(t *testing.T) {
	t.Setenv("SP_RUN_MODE", "test")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.True(t, cfg.Demo.Enabled, "SP_RUN_MODE=test must enable the demo for the E2E suite")
}

// TestTestRunModeCannotTurnTheDemoOff pins the ordering: the run-mode rule is a
// floor, not an override, so an E2E run may still choose its own slug, email
// and password.
func TestTestRunModeCannotTurnTheDemoOff(t *testing.T) {
	t.Setenv("SP_RUN_MODE", "test")
	t.Setenv("SP_DEMO_ENABLED", "false")
	t.Setenv("SP_DEMO_ORG_SLUG", "e2e-demo")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.True(t, cfg.Demo.Enabled)
	require.Equal(t, "e2e-demo", cfg.Demo.ResolvedOrgSlug())
}

// TestDemoDurationTypoIsIgnored pins the deliberate leniency in applyDemoEnv: a
// mistyped TTL must not silently become "delete every visitor's check
// immediately".
func TestDemoDurationTypoIsIgnored(t *testing.T) {
	t.Setenv("SP_DEMO_CHECK_TTL", "one hour please")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, config.DefaultDemoCheckTTL, cfg.Demo.ResolvedCheckTTL())
}
