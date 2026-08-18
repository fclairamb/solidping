package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// defaultAbandonedResultReaperInterval is how often the sweep runs. Coarse
// relative to the stuck-job reaper (1 min, defaultReaperInterval): the
// eligibility threshold itself is already several check-periods wide (see
// models.AbandonedResultThreshold), so nothing is lost by checking less
// often, and the candidate set (models.ResultStatusCreated /
// ResultStatusRunning raw rows) is tiny regardless.
const defaultAbandonedResultReaperInterval = 5 * time.Minute

// AbandonedResultReaperJobDefinition is the factory for the abandoned-result reaper.
type AbandonedResultReaperJobDefinition struct{}

// Type returns the abandoned-result reaper job type.
func (d *AbandonedResultReaperJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeAbandonedResultReaper
}

// AbandonedResultReaperJobConfig is the empty config for the reaper.
type AbandonedResultReaperJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *AbandonedResultReaperJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg AbandonedResultReaperJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &AbandonedResultReaperJobRun{}, nil
}

// AbandonedResultReaperJobRun is the runtime state for one reaper execution.
type AbandonedResultReaperJobRun struct{}

// Run finalizes raw results abandoned in a lifecycle-marker status past their
// check's plausible execution window and reschedules itself one interval out.
// The sweep itself lives in db.Service.ReapAbandonedResults (atomic per row,
// dialect-parity guaranteed by sync-pg-to-sqlite); this runner records
// metrics, warns when anything was reaped, and self-reschedules — same shape
// as StuckJobReaperJobRun.
func (r *AbandonedResultReaperJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.DBService == nil {
		// No DB service wired (e.g. minimal test setup): nothing to reap and
		// no way to reschedule. Treat as a successful no-op.
		log.InfoContext(ctx, "Skipping abandoned-result reaper (DB service not available)")

		return nil
	}

	outcome, err := jctx.DBService.ReapAbandonedResults(ctx)
	if err != nil {
		log.ErrorContext(ctx, "Failed to reap abandoned results", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("reap abandoned results: %w", err))
	}

	prommetrics.RecordResultsReaped(outcome.Reaped)

	if outcome.Reaped > 0 {
		// A non-zero reap is a signal, not routine — warn so it surfaces.
		log.WarnContext(ctx, "Reaped abandoned results",
			"reaped", outcome.Reaped, "candidates", outcome.Candidates)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

func (r *AbandonedResultReaperJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(defaultAbandonedResultReaperInterval)

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeAbandonedResultReaper), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule abandoned-result reaper", "error", err)
	}
}
