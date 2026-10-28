package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStatusSubscribersBudgetDefaults pins the documented confirm-mail budgets
// (spec 2026-10-28-02): 3 per recipient, 100 per page, 20 per IP, per hour.
func TestStatusSubscribersBudgetDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(3, cfg.StatusSubscribers.ConfirmPerRecipientPerHour)
	r.Equal(100, cfg.StatusSubscribers.ConfirmPerPagePerHour)
	r.Equal(20, cfg.StatusSubscribers.ConfirmPerIPPerHour)
}

// TestStatusSubscribersBudgetEnvBinds proves the snake_case env names land on
// their fields, 0 included (it disables a budget), and garbage is ignored.
func TestStatusSubscribersBudgetEnvBinds(t *testing.T) {
	t.Setenv(EnvStatusSubscriberConfirmPerRecipient, "5")
	t.Setenv(EnvStatusSubscriberConfirmPerPage, "0")
	t.Setenv(EnvStatusSubscriberConfirmPerIP, "not-a-number")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(5, cfg.StatusSubscribers.ConfirmPerRecipientPerHour)
	r.Equal(0, cfg.StatusSubscribers.ConfirmPerPagePerHour)
	r.Equal(DefaultStatusSubscriberConfirmPerIPPerHour, cfg.StatusSubscribers.ConfirmPerIPPerHour)
}
