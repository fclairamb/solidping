package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The listing joins organizations, so a soft-deleted org hides its schedules
// even when the schedules themselves were left enabled.
func TestListEnabledReportSchedules_SkipsSoftDeletedOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	sched := models.NewReportSchedule(org.UID, "Monthly", models.ReportFrequencyMonthly)
	sched.Recipients = []string{"ops@acme.com"}
	r.NoError(svc.CreateReportSchedule(ctx, sched))

	list, err := svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Len(list, 1)

	_, err = svc.db.ExecContext(ctx, "UPDATE organizations SET deleted_at = CURRENT_TIMESTAMP WHERE uid = ?", org.UID)
	r.NoError(err)

	list, err = svc.ListEnabledReportSchedules(ctx)
	r.NoError(err)
	r.Empty(list)
}
