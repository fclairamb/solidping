package auth

// The org default reminder interval (spec 2026-10-12-16): GET returns 0 when
// unset, PATCH stores the value and range-checks it before any write.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestOrgSettingsReminderInterval(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("reminder-a", "Reminder A")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Equal(0, got.ReminderIntervalSeconds, "absent means off")

	hourly := 3600
	updated, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{ReminderIntervalSeconds: &hourly})
	r.NoError(err)
	r.Equal(3600, updated.ReminderIntervalSeconds)

	param, err := dbSvc.GetOrgParameter(ctx, org.UID, models.ParamKeyIncidentReminderInterval)
	r.NoError(err)
	r.NotNil(param)

	// Omitting the field leaves it alone.
	tracing := true
	untouched, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{TracerouteOnFailure: &tracing})
	r.NoError(err)
	r.Equal(3600, untouched.ReminderIntervalSeconds)

	// Out of range is refused, and refused before any other field is written.
	pattern := ".*@acme.com"

	for _, bad := range []int{-1, 899, 86401} {
		_, err = svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
			ReminderIntervalSeconds: &bad, RegistrationEmailPattern: &pattern,
		})
		r.ErrorIs(err, ErrInvalidReminderInterval, "%d", bad)
	}

	still, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Equal(3600, still.ReminderIntervalSeconds)
	r.Empty(still.RegistrationEmailPattern, "a refused PATCH writes nothing")

	// 0 turns it off and drops the row.
	off := 0
	cleared, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{ReminderIntervalSeconds: &off})
	r.NoError(err)
	r.Equal(0, cleared.ReminderIntervalSeconds)

	param, err = dbSvc.GetOrgParameter(ctx, org.UID, models.ParamKeyIncidentReminderInterval)
	r.NoError(err)
	r.Nil(param)
}
