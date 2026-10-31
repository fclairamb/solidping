package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFormGuardDefaults pins the documented guard: on, 3 s, 24 h tickets, 12
// submissions per IP and 3 emails per address, each per hour.
func TestFormGuardDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := DefaultFormGuardConfig()
	r.True(guard.Enabled)
	r.Equal(3*time.Second, guard.MinFillTime)
	r.Equal(24*time.Hour, guard.TicketTTL)
	r.Equal(12, guard.PerIPPerHour)
	r.Equal(3, guard.PerEmailPerHour)
}

// TestFormGuardEnvBinds proves the snake_case env names land on their fields,
// 0 included, and that garbage leaves the configured value.
func TestFormGuardEnvBinds(t *testing.T) {
	t.Setenv(EnvFormGuardEnabled, "false")
	t.Setenv(EnvFormGuardMinFillTime, "5s")
	t.Setenv(EnvFormGuardTicketTTL, "not-a-duration")
	t.Setenv(EnvFormGuardPerIPPerHour, "0")
	t.Setenv(EnvFormGuardPerEmailPerHour, "-4")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.False(cfg.Auth.FormGuard.Enabled)
	r.Equal(5*time.Second, cfg.Auth.FormGuard.MinFillTime)
	r.Equal(DefaultFormGuardTicketTTL, cfg.Auth.FormGuard.TicketTTL)
	r.Equal(0, cfg.Auth.FormGuard.PerIPPerHour)
	r.Equal(DefaultFormGuardPerEmailPerHour, cfg.Auth.FormGuard.PerEmailPerHour)
}

// TestFormGuardTestRunMode: test run mode drops the minimum fill time (the
// Playwright suites fill forms in well under 3 s) but keeps the ticket
// required, and an explicit value still wins.
func TestFormGuardTestRunMode(t *testing.T) {
	r := require.New(t)

	guard := DefaultFormGuardConfig()
	applyFormGuardEnv(&guard, runModeTest)
	r.Zero(guard.MinFillTime)
	r.True(guard.Enabled)

	guard = DefaultFormGuardConfig()
	applyFormGuardEnv(&guard, "")
	r.Equal(DefaultFormGuardMinFillTime, guard.MinFillTime, "production keeps the default")

	t.Setenv(EnvFormGuardMinFillTime, "2s")

	guard = DefaultFormGuardConfig()
	applyFormGuardEnv(&guard, runModeTest)
	r.Equal(2*time.Second, guard.MinFillTime)
}

// TestFormGuardResolvedTicketTTL: an unset or negative lifetime falls back to
// the default, an explicit one is kept.
func TestFormGuardResolvedTicketTTL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(DefaultFormGuardTicketTTL, FormGuardConfig{}.ResolvedTicketTTL())
	r.Equal(DefaultFormGuardTicketTTL, FormGuardConfig{TicketTTL: -time.Minute}.ResolvedTicketTTL())
	r.Equal(time.Hour, FormGuardConfig{TicketTTL: time.Hour}.ResolvedTicketTTL())
}
