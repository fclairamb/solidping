package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runReportSchedules(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	sched := models.NewReportSchedule(fix.org.UID, "Monthly", models.ReportFrequencyMonthly)
	sched.Recipients = []string{"ops@acme.com"}
	must.NoError(svc.CreateReportSchedule(ctx, sched))

	enabled, err := svc.ListEnabledReportSchedules(ctx)
	must.NoError(err)
	must.Len(enabled, 1)

	off := false
	name := "Monthly (paused)"
	must.NoError(svc.UpdateReportSchedule(ctx, sched.UID, models.ReportScheduleUpdate{Name: &name, Enabled: &off}))

	enabled, err = svc.ListEnabledReportSchedules(ctx)
	must.NoError(err)
	must.Empty(enabled, "a disabled schedule leaves the work list")

	got, err := svc.GetReportSchedule(ctx, fix.org.UID, sched.UID)
	must.NoError(err)
	must.Equal(name, got.Name)

	// MarkReportScheduleRun is idempotent per period: only the first call wins.
	period := time.Now().Truncate(24 * time.Hour)
	won, err := svc.MarkReportScheduleRun(ctx, sched.UID, period, time.Now())
	must.NoError(err)
	must.True(won)

	won, err = svc.MarkReportScheduleRun(ctx, sched.UID, period, time.Now())
	must.NoError(err)
	must.False(won, "the same period is never reported twice")

	must.NoError(svc.DeleteReportSchedule(ctx, fix.org.UID, sched.UID))
	_, err = svc.GetReportSchedule(ctx, fix.org.UID, sched.UID)
	must.Error(err)

	runReportSchedulesOrgDeleted(t, fix)
}

// Deleting an organization removes its schedules from the work list.
func runReportSchedulesOrgDeleted(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	enabled, err := svc.ListEnabledReportSchedules(ctx)
	must.NoError(err)
	must.Empty(enabled)

	gone := models.NewOrganization("contract-gone-org", "Gone Org")
	must.NoError(svc.CreateOrganization(ctx, gone))

	orphan := models.NewReportSchedule(gone.UID, "Orphan", models.ReportFrequencyMonthly)
	orphan.Recipients = []string{"ops@acme.com"}
	must.NoError(svc.CreateReportSchedule(ctx, orphan))

	enabled, err = svc.ListEnabledReportSchedules(ctx)
	must.NoError(err)
	must.Len(enabled, 1)

	must.NoError(svc.DeleteOrganization(ctx, gone.UID))

	enabled, err = svc.ListEnabledReportSchedules(ctx)
	must.NoError(err)
	must.Empty(enabled, "a deleted organization's schedules are not swept")
}
