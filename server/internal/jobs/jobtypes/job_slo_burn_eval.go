package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// sloBurnEvalInterval is how often burn rates are re-measured.
//
// A minute, not an hour: the fast-burn policy exists to catch "you are spending
// the month in an afternoon", and an alert that arrives up to an hour late has
// already given away most of what it was protecting. The cost is bounded — the
// sweep reads at most evaluationBatchSize policies and each one is a handful of
// indexed availability queries.
const sloBurnEvalInterval = time.Minute

// SLOBurnEvalJobDefinition is the factory for the burn-rate evaluator sweep.
type SLOBurnEvalJobDefinition struct{}

// Type returns the SLO burn evaluation job type.
func (d *SLOBurnEvalJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeSLOBurnEval
}

// SLOBurnEvalJobConfig allows overriding the sweep interval, mainly for tests
// and for installs that want to trade alert latency for query load.
type SLOBurnEvalJobConfig struct {
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// CreateJobRun builds an executable instance.
func (d *SLOBurnEvalJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg SLOBurnEvalJobConfig

	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("invalid slo burn eval config: %w", err)
		}
	}

	return &SLOBurnEvalJobRun{config: cfg}, nil
}

// SLOBurnEvalJobRun is the runtime state for one execution.
type SLOBurnEvalJobRun struct {
	config SLOBurnEvalJobConfig
}

func (r *SLOBurnEvalJobRun) interval() time.Duration {
	if r.config.IntervalSeconds > 0 {
		return time.Duration(r.config.IntervalSeconds) * time.Second
	}

	return sloBurnEvalInterval
}

// Run evaluates every enabled burn-rate alert policy and reschedules itself.
//
// The evaluator is reached through jctx.Services.SLOBurn rather than
// constructed here: it needs handlers/incidents, and jobtypes cannot import
// that package without an import cycle (see services.SLOBurnEvaluator).
//
// Rescheduling happens even when the sweep fails, in the deferred sense that a
// retryable error puts the job back on the queue — but a sweep that merely
// found nothing must still re-arm, or one empty minute would end alerting for
// the lifetime of the process.
func (r *SLOBurnEvalJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.SLOBurn == nil {
		log.DebugContext(ctx, "Skipping SLO burn evaluation (evaluator not wired)")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	evaluated, err := jctx.Services.SLOBurn.EvaluateBurnRates(ctx, jctx.ClockNow())
	if err != nil {
		// Re-arm before surfacing the failure: a retry covers this run, but the
		// steady-state schedule must not depend on the retry succeeding.
		r.rescheduleSelf(ctx, jctx)

		return jobdef.NewRetryableError(fmt.Errorf("evaluate slo burn rates: %w", err))
	}

	if evaluated > 0 {
		log.DebugContext(ctx, "Evaluated SLO burn-rate policies", "count", evaluated)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// rescheduleSelf keeps the sweep running.
func (r *SLOBurnEvalJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(r.interval())

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeSLOBurnEval), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule SLO burn evaluation sweep", "error", err)
	}
}
