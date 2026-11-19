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

// The dry-run validate endpoint holds max_delivery_delay to the same rule as
// the write paths.
func TestValidateCheckRejectsDeliveryDelayNotBelowPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, _, org := setupSMTPDeliveryChecksService(t)

	validated, err := svc.ValidateCheck(t.Context(), org.Slug, &checks.ValidateCheckRequest{
		Type: "email", Period: "5m", Config: map[string]any{"max_delivery_delay": "5m"},
	})
	r.NoError(err)
	r.False(validated.Valid)
	r.NotEmpty(validated.Fields)
	r.Equal(checks.CodeInvalidConfig, validated.Fields[len(validated.Fields)-1].Code)
	r.Contains(validated.Fields[len(validated.Fields)-1].Message, "lower than the check period")

	ok, err := svc.ValidateCheck(t.Context(), org.Slug, &checks.ValidateCheckRequest{
		Type: "email", Period: "1h", Config: map[string]any{"max_delivery_delay": "5m"},
	})
	r.NoError(err)
	r.True(ok.Valid)
}

// A dry-run import that updates an existing email check refuses a delay the
// real update would refuse.
func TestImportDryRunUpdateRejectsDeliveryDelayNotBelowPeriod(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "dry-delay", nil)

	entry := func(delay string) map[string]any {
		return map[string]any{
			"name": "Mail", "slug": "mail-delay", "type": "email", "enabled": true, "period": "1h",
			"config": map[string]any{"max_delivery_delay": delay},
		}
	}

	created := rig.importDoc(t, importDocument(rig.org.Slug, entry("10m")), false)
	r.Equal(1, created.Created, "%+v", created.Errors)

	dry := rig.importDoc(t, importDocument(rig.org.Slug, entry("2h")), true)
	r.Zero(dry.Updated)
	r.Len(dry.Errors, 1)
	r.Contains(dry.Errors[0].Error, "lower than the check period")
}
