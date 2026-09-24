package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// checkFreshnessSweepInterval is how often silent checks are looked for. A
// minute: the threshold is at least five, so sweeping less often would only
// add latency to "this region went dark", and the sweep is one indexed query.
const checkFreshnessSweepInterval = time.Minute

// CheckFreshnessSweepJobDefinition is the factory for the freshness sweep
// (spec 2026-09-25-02).
type CheckFreshnessSweepJobDefinition struct{}

// Type returns the freshness sweep job type.
func (d *CheckFreshnessSweepJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeCheckFreshnessSweep
}

// CreateJobRun builds an executable instance.
func (d *CheckFreshnessSweepJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	cfg, err := decodeSweepConfig(config, "check freshness sweep")
	if err != nil {
		return nil, err
	}

	return &CheckFreshnessSweepJobRun{config: cfg}, nil
}

// CheckFreshnessSweepJobRun is the runtime state for one execution.
type CheckFreshnessSweepJobRun struct {
	config sweepIntervalConfig
}

// Run sweeps for stale checks and reschedules itself — through periodicSweep,
// so it re-arms even when a sweep fails.
//
// The sweeper is reached through jctx.Services.Freshness rather than
// constructed here: it needs handlers/incidents, and jobtypes cannot import
// that package without an import cycle (see services.FreshnessSweeper).
func (r *CheckFreshnessSweepJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	return periodicSweep{
		jobType:         jobdef.JobTypeCheckFreshnessSweep,
		defaultInterval: checkFreshnessSweepInterval,
		config:          r.config,
		what:            "check freshness",
		logMessage:      "Moved silent checks to stale",
		run: func(ctx context.Context, jctx *jobdef.JobContext) (int, bool, error) {
			if jctx.Services == nil || jctx.Services.Freshness == nil {
				return 0, false, nil
			}

			marked, err := jctx.Services.Freshness.SweepStale(ctx, jctx.ClockNow())

			return marked, true, err
		},
	}.execute(ctx, jctx)
}
