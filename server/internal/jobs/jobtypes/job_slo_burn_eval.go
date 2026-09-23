package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// sloBurnEvalInterval is how often burn rates are re-measured.
//
// A minute, not an hour: the fast-burn policy exists to catch "you are spending
// the month in an afternoon", and an alert that arrives up to an hour late has
// already given away most of what it was protecting. The cost is bounded — the
// sweep reads at most one bounded batch of policies and each one is a handful of
// indexed availability queries.
const sloBurnEvalInterval = time.Minute

// SLOBurnEvalJobDefinition is the factory for the burn-rate evaluator sweep.
type SLOBurnEvalJobDefinition struct{}

// Type returns the SLO burn evaluation job type.
func (d *SLOBurnEvalJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeSLOBurnEval
}

// CreateJobRun builds an executable instance.
func (d *SLOBurnEvalJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	cfg, err := decodeSweepConfig(config, "slo burn eval")
	if err != nil {
		return nil, err
	}

	return &SLOBurnEvalJobRun{config: cfg}, nil
}

// SLOBurnEvalJobRun is the runtime state for one execution.
type SLOBurnEvalJobRun struct {
	config sweepIntervalConfig
}

// Run evaluates every enabled burn-rate alert policy and reschedules itself.
//
// The evaluator is reached through jctx.Services.SLOBurn rather than constructed
// here: it needs handlers/incidents, and jobtypes cannot import that package
// without an import cycle (see services.SLOBurnEvaluator).
func (r *SLOBurnEvalJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	return periodicSweep{
		jobType:    jobdef.JobTypeSLOBurnEval,
		interval:   r.config.interval(sloBurnEvalInterval),
		what:       "slo burn rates",
		logMessage: "Evaluated SLO burn-rate policies",
		run: func(ctx context.Context, jctx *jobdef.JobContext) (int, bool, error) {
			if jctx.Services == nil || jctx.Services.SLOBurn == nil {
				return 0, false, nil
			}

			evaluated, err := jctx.Services.SLOBurn.EvaluateBurnRates(ctx, jctx.ClockNow())

			return evaluated, true, err
		},
	}.execute(ctx, jctx)
}
