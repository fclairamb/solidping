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

// Fallback timeout/interval used when AppConfig is unavailable (e.g. unit tests
// without full config wiring). Production values come from
// jctx.AppConfig.Jobs (SP_JOBS_STUCK_TIMEOUT / SP_JOBS_REAPER_INTERVAL).
const (
	defaultStuckTimeout   = 15 * time.Minute
	defaultReaperInterval = time.Minute
)

// StuckJobReaperJobDefinition is the factory for the stuck-job reaper.
type StuckJobReaperJobDefinition struct{}

// Type returns the stuck-job reaper job type.
func (d *StuckJobReaperJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeStuckJobReaper
}

// StuckJobReaperJobConfig is the empty config for the reaper.
type StuckJobReaperJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *StuckJobReaperJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg StuckJobReaperJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &StuckJobReaperJobRun{}, nil
}

// StuckJobReaperJobRun is the runtime state for one reaper execution.
type StuckJobReaperJobRun struct{}

// Run recovers jobs stuck in 'running' past the configured timeout and
// reschedules itself one interval out. Recovery itself lives in
// jobsvc.ReapStuckJobs (atomic, guarded); this runner reads config, records
// metrics, warns when anything was reaped, and self-reschedules.
func (r *StuckJobReaperJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	timeout := r.timeout(jctx)

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		// No job service wired (e.g. minimal test setup): nothing to reap and
		// no way to reschedule. Treat as a successful no-op.
		log.InfoContext(ctx, "Skipping stuck-job reaper (services not available)")

		return nil
	}

	result, err := jctx.Services.Jobs.ReapStuckJobs(ctx, timeout)
	if err != nil {
		log.ErrorContext(ctx, "Failed to reap stuck jobs", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("reap stuck jobs: %w", err))
	}

	prommetrics.RecordJobReaped("retried", result.Retried)
	prommetrics.RecordJobReaped("failed", result.Failed)

	if result.Retried > 0 || result.Failed > 0 {
		// A non-zero reap is a signal, not routine — warn so it surfaces.
		log.WarnContext(ctx, "Reaped stuck jobs",
			"retried", result.Retried, "failed", result.Failed, "timeout", timeout.String())
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// timeout resolves the stuck-job timeout from config, falling back to the
// package default when config is unavailable.
func (r *StuckJobReaperJobRun) timeout(jctx *jobdef.JobContext) time.Duration {
	if jctx.AppConfig != nil && jctx.AppConfig.Jobs.StuckTimeout > 0 {
		return jctx.AppConfig.Jobs.StuckTimeout
	}

	return defaultStuckTimeout
}

// interval resolves the reaper interval from config, falling back to the
// package default when config is unavailable.
func (r *StuckJobReaperJobRun) interval(jctx *jobdef.JobContext) time.Duration {
	if jctx.AppConfig != nil && jctx.AppConfig.Jobs.ReaperInterval > 0 {
		return jctx.AppConfig.Jobs.ReaperInterval
	}

	return defaultReaperInterval
}

func (r *StuckJobReaperJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(r.interval(jctx))

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeStuckJobReaper), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule stuck-job reaper", "error", err)
	}
}
