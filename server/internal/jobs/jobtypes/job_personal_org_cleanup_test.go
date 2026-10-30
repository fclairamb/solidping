package jobtypes

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

var errSweepFailed = errors.New("sweep failed")

// fakePersonalOrgSweeper records the sweeps the job asks for.
type fakePersonalOrgSweeper struct {
	calls int
	err   error
}

func (f *fakePersonalOrgSweeper) SweepPersonalOrgs(context.Context, time.Time) (int, int, error) {
	f.calls++

	return 1, 1, f.err
}

// TestPersonalOrgCleanupRunsTheSweepAndReschedules: the job delegates to the
// auth service's sweep (spec 2026-10-30-04), reschedules itself daily, retries
// a failed sweep, and is a no-op on a bare registry.
func TestPersonalOrgCleanupRunsTheSweepAndReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	sweeper := &fakePersonalOrgSweeper{}
	jctx := &jobdef.JobContext{
		Services: &services.Registry{
			Jobs:         jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil),
			PersonalOrgs: sweeper,
		},
		DBService: dbSvc,
		Logger:    slog.Default(),
	}

	def, ok := GetJobDefinition(jobdef.JobTypePersonalOrgCleanup)
	r.True(ok, "the job type is registered")

	run, err := def.CreateJobRun(nil)
	r.NoError(err)
	r.NoError(run.Run(ctx, jctx))
	r.Equal(1, sweeper.calls)

	count, err := dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypePersonalOrgCleanup)).
		Count(ctx)
	r.NoError(err)
	r.Positive(count, "the sweeper enqueues its next run")

	sweeper.err = errSweepFailed
	runErr := run.Run(ctx, jctx)
	r.Error(runErr)

	var retryable *jobdef.RetryableError
	r.ErrorAs(runErr, &retryable)

	r.NoError(run.Run(ctx, &jobdef.JobContext{Services: &services.Registry{}, Logger: slog.Default()}))
}
