package jobsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

var errRefused = errors.New("refused")

// CreateJobAdmitted inserts the job only when admit accepts it, returns
// admit's error as is, and runs admit on the transaction that inserts.
func TestCreateJobAdmitted(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("admitted", "Admitted")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	countJobs := func() int {
		count, countErr := dbSvc.DB().NewSelect().TableExpr("jobs").
			Where("organization_uid = ?", org.UID).Where("type = ?", "admitted_test").Count(ctx)
		r.NoError(countErr)

		return count
	}

	_, err = svc.CreateJobAdmitted(ctx, org.UID, "admitted_test", []byte(`{"a":1}`),
		func(context.Context, bun.Tx) error { return errRefused })
	r.ErrorIs(err, errRefused)
	r.Zero(countJobs(), "a refused job is never inserted")

	job, err := svc.CreateJobAdmitted(ctx, org.UID, "admitted_test", []byte(`{"a":1}`),
		func(ctx context.Context, tx bun.Tx) error {
			// admit sees the transaction: nothing inserted yet.
			count, countErr := tx.NewSelect().TableExpr("jobs").
				Where("organization_uid = ?", org.UID).Where("type = ?", "admitted_test").Count(ctx)
			if countErr != nil {
				return countErr
			}

			r.Zero(count)

			return nil
		})
	r.NoError(err)
	r.Equal(models.JobStatusPending, job.Status)
	r.EqualValues(1, job.Config["a"])
	r.Equal(1, countJobs())
}
