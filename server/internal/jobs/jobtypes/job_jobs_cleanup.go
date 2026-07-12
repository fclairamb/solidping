package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// jobs_cleanup retention defaults and batching (spec 2026-07-11-17). The windows
// resolve from the performance.* parameters at each run (env → global DB
// parameter → these defaults), so an operator can retune them without a restart.
const (
	// defaultJobsSoftDeleteHours is how long a finished job stays visible before
	// stage 1 soft-deletes it.
	defaultJobsSoftDeleteHours = 48
	// defaultJobsHardDeleteHours is the grace window after soft-deletion before
	// stage 2 physically deletes the row.
	defaultJobsHardDeleteHours = 24
	// jobsCleanupBatchSize bounds each stage's per-statement row count so a large
	// backlog never holds one long transaction. The run loops a stage until a
	// batch comes back short.
	jobsCleanupBatchSize = 10_000
	// jobsCleanupInterval is the self-reschedule delay — this is a daily
	// maintenance job.
	jobsCleanupInterval = 24 * time.Hour
)

// JobsCleanupJobDefinition is the factory for the jobs-table retention cleaner.
type JobsCleanupJobDefinition struct{}

// Type returns the jobs cleanup job type.
func (d *JobsCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeJobsCleanup
}

// JobsCleanupJobConfig is the empty config for the cleaner.
type JobsCleanupJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *JobsCleanupJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg JobsCleanupJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &JobsCleanupJobRun{}, nil
}

// JobsCleanupJobRun is the runtime state for one cleanup execution.
type JobsCleanupJobRun struct{}

// Run soft-deletes finished jobs older than the soft-delete window (stage 1),
// hard-deletes rows soft-deleted past the grace window (stage 2), then
// reschedules itself one interval out. Its own terminal rows flow through the
// same two stages, so the cleaner self-cleans.
func (r *JobsCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	log.InfoContext(ctx, "Starting jobs cleanup job")

	// Both windows resolve at run time through the shared retention resolver
	// (env → global DB parameter → default); no legacy koanf fallback exists for
	// these keys, so the legacy argument is 0 and it drops straight to the
	// default when nothing overrides it.
	softHours := resolveRetentionTier(ctx, jctx,
		systemconfig.KeyPerfJobsSoftDeleteHours, "SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS",
		0, defaultJobsSoftDeleteHours)
	hardHours := resolveRetentionTier(ctx, jctx,
		systemconfig.KeyPerfJobsHardDeleteHours, "SP_PERFORMANCE_JOBS_HARD_DELETE_HOURS",
		0, defaultJobsHardDeleteHours)

	now := time.Now()
	softBefore := now.Add(-time.Duration(softHours) * time.Hour)
	hardBefore := now.Add(-time.Duration(hardHours) * time.Hour)

	softCount, err := r.softDeletePass(ctx, jctx, softBefore)
	if err != nil {
		log.ErrorContext(ctx, "Failed to soft-delete finished jobs", "error", err)

		return jobdef.NewRetryableError(err)
	}

	log.InfoContext(ctx, "Soft-deleted finished jobs", "count", softCount)

	hardCount, err := r.hardDeletePass(ctx, jctx, hardBefore)
	if err != nil {
		log.ErrorContext(ctx, "Failed to delete soft-deleted jobs", "error", err)

		return jobdef.NewRetryableError(err)
	}

	log.InfoContext(ctx, "Deleted soft-deleted jobs", "count", hardCount)

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// softDeletePass runs stage 1 in batches until a short batch drains the backlog.
func (r *JobsCleanupJobRun) softDeletePass(ctx context.Context, jctx *jobdef.JobContext, before time.Time) (int64, error) {
	var total int64

	for {
		n, err := jctx.DBService.SoftDeleteFinishedJobs(ctx, before, jobsCleanupBatchSize)
		if err != nil {
			return total, err
		}

		total += n

		if n < jobsCleanupBatchSize {
			break
		}
	}

	return total, nil
}

// hardDeletePass runs stage 2 in batches. A short batch ends the run: whatever
// remains is either not yet past the grace window or still FK-referenced, and
// drains on a later run.
func (r *JobsCleanupJobRun) hardDeletePass(ctx context.Context, jctx *jobdef.JobContext, before time.Time) (int64, error) {
	var total int64

	for {
		n, err := jctx.DBService.DeleteSoftDeletedJobs(ctx, before, jobsCleanupBatchSize)
		if err != nil {
			return total, err
		}

		total += n

		if n < jobsCleanupBatchSize {
			break
		}
	}

	return total, nil
}

func (r *JobsCleanupJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(jobsCleanupInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeJobsCleanup), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule jobs cleanup", "error", err)
	}
}
