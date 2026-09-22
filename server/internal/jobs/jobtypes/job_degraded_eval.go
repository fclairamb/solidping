package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// degradedEvalInterval is how often the degraded rules are re-measured.
//
// A minute, matching the fleet's dominant check period: evaluating less often
// than checks run would add pure latency to a detector whose whole purpose is to
// notice a pattern the per-probe path cannot. The cost is bounded — one sweep
// reads at most evaluationBatchSize checks and one indexed raw-results query each.
const degradedEvalInterval = time.Minute

// DegradedEvalJobDefinition is the factory for the degraded-detection sweep.
type DegradedEvalJobDefinition struct{}

// Type returns the degraded evaluation job type.
func (d *DegradedEvalJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeDegradedEval
}

// DegradedEvalJobConfig allows overriding the sweep interval, mainly for tests
// and for installs that want to trade detection latency for query load.
type DegradedEvalJobConfig struct {
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// CreateJobRun builds an executable instance.
func (d *DegradedEvalJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg DegradedEvalJobConfig

	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("invalid degraded eval config: %w", err)
		}
	}

	return &DegradedEvalJobRun{config: cfg}, nil
}

// DegradedEvalJobRun is the runtime state for one execution.
type DegradedEvalJobRun struct {
	config DegradedEvalJobConfig
}

func (r *DegradedEvalJobRun) interval() time.Duration {
	if r.config.IntervalSeconds > 0 {
		return time.Duration(r.config.IntervalSeconds) * time.Second
	}

	return degradedEvalInterval
}

// Run evaluates every check's degraded rules and reschedules itself.
//
// The evaluator is reached through jctx.Services.Degraded rather than
// constructed here, for the same reason the burn evaluator is: it needs
// handlers/incidents, and jobtypes cannot import that package without an import
// cycle (see services.DegradedEvaluator).
//
// A sweep that merely found nothing must still re-arm, or one empty minute would
// end degraded detection for the lifetime of the process.
func (r *DegradedEvalJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Degraded == nil {
		log.DebugContext(ctx, "Skipping degraded evaluation (evaluator not wired)")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	evaluated, err := jctx.Services.Degraded.EvaluateDegraded(ctx, jctx.ClockNow())
	if err != nil {
		// Re-arm before surfacing the failure: a retry covers this run, but the
		// steady-state schedule must not depend on the retry succeeding.
		r.rescheduleSelf(ctx, jctx)

		return jobdef.NewRetryableError(fmt.Errorf("evaluate degraded checks: %w", err))
	}

	if evaluated > 0 {
		log.DebugContext(ctx, "Evaluated degraded detection rules", "count", evaluated)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// rescheduleSelf keeps the sweep running.
func (r *DegradedEvalJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(r.interval())

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeDegradedEval), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule degraded evaluation sweep", "error", err)
	}
}
