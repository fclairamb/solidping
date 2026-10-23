package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runReportSchedules(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	sched := models.NewReportSchedule(f.org.UID, "Monthly", models.ReportFrequencyMonthly)
	sched.Recipients = []string{"ops@acme.com"}
	r.NoError(svc.CreateReportSchedule(ctx, sched))

	enabled, err := svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Len(enabled, 1)

	off := false
	name := "Monthly (paused)"
	r.NoError(svc.UpdateReportSchedule(ctx, sched.UID, models.ReportScheduleUpdate{Name: &name, Enabled: &off}))

	enabled, err = svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Empty(enabled, "a disabled schedule leaves the work list")

	got, err := svc.GetReportSchedule(ctx, f.org.UID, sched.UID)
	r.NoError(err)
	r.Equal(name, got.Name)

	// MarkReportScheduleRun is idempotent per period: only the first call wins.
	period := time.Now().Truncate(24 * time.Hour)
	won, err := svc.MarkReportScheduleRun(ctx, sched.UID, period, time.Now())
	r.NoError(err)
	r.True(won)

	won, err = svc.MarkReportScheduleRun(ctx, sched.UID, period, time.Now())
	r.NoError(err)
	r.False(won, "the same period is never reported twice")

	r.NoError(svc.DeleteReportSchedule(ctx, f.org.UID, sched.UID))
	_, err = svc.GetReportSchedule(ctx, f.org.UID, sched.UID)
	r.Error(err)
}
