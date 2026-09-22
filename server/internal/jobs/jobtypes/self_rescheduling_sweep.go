package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// sweepIntervalConfig is the one knob every self-rescheduling sweep exposes:
// override the interval, mainly for tests and for installs that want to trade
// detection latency for query load.
type sweepIntervalConfig struct {
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// interval resolves the configured override, or the caller's default.
func (c sweepIntervalConfig) interval(fallback time.Duration) time.Duration {
	if c.IntervalSeconds > 0 {
		return time.Duration(c.IntervalSeconds) * time.Second
	}

	return fallback
}

// decodeSweepConfig parses a sweep's optional interval override.
func decodeSweepConfig(config json.RawMessage, what string) (sweepIntervalConfig, error) {
	var cfg sweepIntervalConfig

	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid %s config: %w", what, err)
		}
	}

	return cfg, nil
}

// periodicSweep is the shape shared by every global evaluator job: the sweep
// itself, plus how to describe and re-arm it.
//
// Extracted because the SLO burn evaluator and the degraded-detection evaluator
// are the same job with a different callback — and were caught as literal
// duplicates. The invariants below are the reason this is one function rather
// than two similar ones:
//
//   - the sweep re-arms even when it found nothing, or one empty minute would
//     end evaluation for the lifetime of the process;
//   - it re-arms BEFORE surfacing a failure, so the steady-state schedule never
//     depends on a retry succeeding;
//   - an unwired evaluator (a process with no such service) re-arms and returns
//     nil rather than erroring forever.
type periodicSweep struct {
	// jobType is what gets rescheduled.
	jobType jobdef.JobType
	// interval is the resolved gap until the next run.
	interval time.Duration
	// what names the sweep in error messages ("degraded checks").
	what string
	// logMessage is the debug line emitted when the sweep did something.
	logMessage string
	// run performs one sweep. `wired` false means the evaluator is absent from
	// this process, which is a normal state, not a failure.
	run func(ctx context.Context, jctx *jobdef.JobContext) (evaluated int, wired bool, err error)
}

// execute runs one sweep and reschedules the next.
func (p periodicSweep) execute(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	evaluated, wired, err := p.run(ctx, jctx)

	if !wired {
		log.DebugContext(ctx, "Skipping sweep (evaluator not wired)", "jobType", string(p.jobType))
		p.reschedule(ctx, jctx)

		return nil
	}

	if err != nil {
		p.reschedule(ctx, jctx)

		return jobdef.NewRetryableError(fmt.Errorf("evaluate %s: %w", p.what, err))
	}

	if evaluated > 0 {
		log.DebugContext(ctx, p.logMessage, "count", evaluated)
	}

	p.reschedule(ctx, jctx)

	return nil
}

// reschedule keeps the sweep running. Best-effort: a failure to enqueue is
// logged, because there is nothing else this run can do about it.
func (p periodicSweep) reschedule(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(p.interval)

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(p.jobType), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule sweep",
			"jobType", string(p.jobType), "error", err)
	}
}
