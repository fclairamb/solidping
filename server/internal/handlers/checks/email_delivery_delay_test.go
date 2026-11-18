package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// The "max_delivery_delay lower than the period" rule (spec 2026-11-18-03)
// holds on create (explicit and defaulted period) and on update (config change
// and period-only change).
func TestEmailMaxDeliveryDelayVersusPeriod(t *testing.T) {
	t.Parallel()

	period := "5m"

	t.Run("create with explicit period", func(t *testing.T) {
		t.Parallel()
		svc, _, org := setupSMTPDeliveryChecksService(t)

		_, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
			Name: "mail", Slug: "mail", Type: "email", Period: &period,
			Config: map[string]any{"max_delivery_delay": "5m"},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "lower than the check period")
	})

	t.Run("create with defaulted period", func(t *testing.T) {
		t.Parallel()
		svc, _, org := setupSMTPDeliveryChecksService(t)

		_, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
			Name: "mail", Slug: "mail", Type: "email",
			Config: map[string]any{"max_delivery_delay": "100h"},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "lower than the check period")
	})

	t.Run("update config and period-only", func(t *testing.T) {
		t.Parallel()
		svc, _, org := setupSMTPDeliveryChecksService(t)
		long := "1h"

		created, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
			Name: "mail", Slug: "mail", Type: "email", Period: &long,
			Config: map[string]any{"max_delivery_delay": "10m"},
		})
		require.NoError(t, err)

		tooBig := map[string]any{"max_delivery_delay": "2h"}
		_, err = svc.UpdateCheck(t.Context(), org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &tooBig})
		require.Error(t, err)
		require.Contains(t, err.Error(), "lower than the check period")

		okPeriod := "20m"
		_, err = svc.UpdateCheck(t.Context(), org.Slug, created.UID, &checks.UpdateCheckRequest{Period: &okPeriod})
		require.NoError(t, err, "20m is above the 10m limit")

		short := "10m"
		_, err = svc.UpdateCheck(t.Context(), org.Slug, created.UID, &checks.UpdateCheckRequest{Period: &short})
		require.Error(t, err, "a period equal to the stored limit is rejected")
		require.Contains(t, err.Error(), "lower than the check period")
	})
}
