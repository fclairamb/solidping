package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// personal_org_cleanup warns, then deletes, the auto-created personal orgs
// that stayed empty (spec 2026-10-30-04). The rules live in the auth service
// (SweepAbandonedPersonalOrgs), reached through services.PersonalOrgSweeper;
// this job only runs them daily.
const personalOrgCleanupInterval = 24 * time.Hour

// PersonalOrgCleanupJobDefinition is the factory for the sweeper.
type PersonalOrgCleanupJobDefinition struct{}

// Type returns the personal org cleanup job type.
func (d *PersonalOrgCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypePersonalOrgCleanup
}

// CreateJobRun builds an executable instance. The job takes no config.
func (d *PersonalOrgCleanupJobDefinition) CreateJobRun(_ json.RawMessage) (jobdef.JobRunner, error) {
	return &PersonalOrgCleanupJobRun{}, nil
}

// PersonalOrgCleanupJobRun is one sweep.
type PersonalOrgCleanupJobRun struct{}

// Run sweeps, then reschedules itself one interval out. A sweep error is
// retried; a missing sweeper (a bare registry) is a no-op.
func (r *PersonalOrgCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	if jctx.Services == nil || jctx.Services.PersonalOrgs == nil {
		jctx.Logger.DebugContext(ctx, "Skipping personal org cleanup (no sweeper wired)")

		return nil
	}

	warned, deleted, err := jctx.Services.PersonalOrgs.SweepPersonalOrgs(ctx, jctx.ClockNow())
	if err != nil {
		jctx.Logger.ErrorContext(ctx, "Personal org cleanup failed", "error", err)

		return jobdef.NewRetryableError(err)
	}

	jctx.Logger.Log(ctx, levelForCount(int64(warned+deleted)), "Swept abandoned personal organizations",
		"warned", warned, "deleted", deleted)

	r.rescheduleSelf(ctx, jctx)

	return nil
}

func (r *PersonalOrgCleanupJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(personalOrgCleanupInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypePersonalOrgCleanup), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule personal org cleanup", "error", err)
	}
}
