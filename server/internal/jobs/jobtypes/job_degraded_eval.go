package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// degradedEvalInterval is how often the degraded rules are re-measured.
//
// A minute, matching the fleet's dominant check period: evaluating less often
// than checks run would add pure latency to a detector whose whole purpose is to
// notice a pattern the per-probe path cannot. The cost is bounded — one sweep
// reads at most one bounded batch of checks and one indexed raw-results query
// each.
const degradedEvalInterval = time.Minute

// DegradedEvalJobDefinition is the factory for the degraded-detection sweep.
type DegradedEvalJobDefinition struct{}

// Type returns the degraded evaluation job type.
func (d *DegradedEvalJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeDegradedEval
}

// CreateJobRun builds an executable instance.
func (d *DegradedEvalJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	cfg, err := decodeSweepConfig(config, "degraded eval")
	if err != nil {
		return nil, err
	}

	return &DegradedEvalJobRun{config: cfg}, nil
}

// DegradedEvalJobRun is the runtime state for one execution.
type DegradedEvalJobRun struct {
	config sweepIntervalConfig
}

// Run evaluates every check's degraded rules and reschedules itself.
//
// The evaluator is reached through jctx.Services.Degraded rather than
// constructed here: it needs handlers/incidents, and jobtypes cannot import that
// package without an import cycle (see services.DegradedEvaluator).
func (r *DegradedEvalJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	return periodicSweep{
		jobType:         jobdef.JobTypeDegradedEval,
		defaultInterval: degradedEvalInterval,
		config:          r.config,
		what:            "degraded checks",
		logMessage:      "Evaluated degraded detection rules",
		run: func(ctx context.Context, jctx *jobdef.JobContext) (int, bool, error) {
			if jctx.Services == nil || jctx.Services.Degraded == nil {
				return 0, false, nil
			}

			evaluated, err := jctx.Services.Degraded.EvaluateDegraded(ctx, jctx.ClockNow())

			return evaluated, true, err
		},
	}.execute(ctx, jctx)
}
