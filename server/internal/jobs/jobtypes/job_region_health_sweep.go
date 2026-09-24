package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
)

// regionHealthSweepInterval is how often region liveness is evaluated. A
// minute: the liveness window is five, so a region reads dark 5 to 6 minutes
// after its last worker beat, and RegionHealth is a few bounded scans.
const regionHealthSweepInterval = time.Minute

// RegionHealthSweepJobDefinition is the factory for the region sweep (spec
// 2026-09-25-03).
type RegionHealthSweepJobDefinition struct{}

// Type returns the region sweep job type.
func (d *RegionHealthSweepJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeRegionHealthSweep
}

// CreateJobRun builds an executable instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *RegionHealthSweepJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	cfg, err := decodeSweepConfig(config, "region health sweep")
	if err != nil {
		return nil, err
	}

	return &RegionHealthSweepJobRun{config: cfg}, nil
}

// RegionHealthSweepJobRun is the runtime state for one execution.
type RegionHealthSweepJobRun struct {
	config sweepIntervalConfig
}

// Run sweeps the cloud regions and reschedules itself — through
// periodicSweep, so it re-arms even when a sweep fails.
//
// It runs whatever `platform_watchdog.enabled` says: a disabled watchdog only
// means there is no operator to tell. The org notices and the gauges do not
// depend on it.
func (r *RegionHealthSweepJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	return periodicSweep{
		jobType:         jobdef.JobTypeRegionHealthSweep,
		defaultInterval: regionHealthSweepInterval,
		config:          r.config,
		what:            "region health",
		logMessage:      "Region sweep recorded transitions",
		run: func(ctx context.Context, jctx *jobdef.JobContext) (int, bool, error) {
			reporter := regionChecksServiceFor(jctx)
			if reporter == nil {
				return 0, false, nil
			}

			deps := &regionsweep.Deps{
				DB:       jctx.DBService,
				Health:   reporter,
				Operator: operatorNoticeDeps(jctx),
				Logger:   jctx.Logger,
			}

			if jctx.Services != nil {
				deps.Jobs = jctx.Services.Jobs
			}

			if jctx.AppConfig != nil {
				deps.BaseURL = jctx.AppConfig.Server.BaseURL
			}

			result, err := regionsweep.Sweep(ctx, deps)
			if err != nil {
				return 0, true, err
			}

			return len(result.Transitions), true, nil
		},
	}.execute(ctx, jctx)
}
