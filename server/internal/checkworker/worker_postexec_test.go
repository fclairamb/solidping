package checkworker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestBuildSubmitRequestUsesPostExec pins the in-process worker to the SHARED
// post-exec accounting. Both executors — the in-cluster worker (which persists
// the state itself through DirectBackend) and the server-side submit path
// (which recomputes it for results arriving over the agent transport) — must
// score an identical execution identically, otherwise migrating a region to
// platform agents would quietly change its scheduling (spec 2026-07-27-01 P5).
func TestBuildSubmitRequestUsesPostExec(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	params := scheduling.Params{
		SlowThresholdMs:     1000,
		CheckTimeout:        30 * time.Second,
		TierCreditPerWeight: 2 * time.Second,
		TierCreditMax:       10 * time.Second,
		CostTimeoutFactor:   3,
		CostTimeoutFloor:    2 * time.Second,
		LaneSlowThresholdMs: 1200,
		LaneFastThresholdMs: 800,
	}

	worker := &CheckWorker{schedParams: params}

	now := time.Now()
	scheduledAt := now.Add(-2 * time.Second)
	effective := now.Add(-1 * time.Second)
	region := "eu-west-1"

	job := &models.CheckJob{
		UID:                  "job-1",
		Period:               timeutils.Duration(time.Minute),
		ScheduledAt:          &scheduledAt,
		EffectiveScheduledAt: &effective,
		CostEWMAMs:           850,
		DelayEWMAMs:          40,
		Lane:                 0,
		PlanWeight:           3,
		Region:               &region,
	}

	result := checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: 1750 * time.Millisecond,
	}

	execStart := now

	req := worker.buildSubmitRequest(job, result, execStart)
	r.NotNil(req.Sched, "the in-process path persists the scheduling state itself")

	want := params.PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:       job.CostEWMAMs,
		PrevDelayEWMAMs:      job.DelayEWMAMs,
		PrevLane:             job.Lane,
		PlanWeight:           job.PlanWeight,
		ScheduledAt:          job.ScheduledAt,
		EffectiveScheduledAt: job.EffectiveScheduledAt,
		DurationMs:           float64(result.Duration.Milliseconds()),
		TimedOut:             false,
		ExecStart:            &execStart,
		NextScheduledAt:      req.NextScheduledAt,
	})

	r.InDelta(want.CostEWMAMs, req.Sched.CostEWMAMs, 1e-9)
	r.InDelta(want.DelayEWMAMs, req.Sched.DelayEWMAMs, 1e-9)
	r.Equal(want.EffectiveScheduledAt, req.Sched.EffectiveScheduledAt)
	r.Equal(want.Lane, req.Sched.Lane)

	// execStart travels with the result so the server-side path can compute the
	// same delay sample when the executor is remote.
	r.Equal(execStart, req.ExecStart)
}

// TestBuildSubmitRequestTimeoutPinsCost: the worker's timeout handling is the
// shared CostSampleMs rule, not a local special case.
func TestBuildSubmitRequestTimeoutPinsCost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	params := scheduling.Params{CheckTimeout: 30 * time.Second, SlowThresholdMs: 1000}
	worker := &CheckWorker{schedParams: params}

	now := time.Now()
	// A region-bearing job: resolveResultRegion falls back to the worker's own
	// registration otherwise, which this bare struct has none of.
	region := "eu-west-1"
	job := &models.CheckJob{
		UID:         "job-1",
		Period:      timeutils.Duration(time.Minute),
		ScheduledAt: &now,
		CostEWMAMs:  1000,
		Region:      &region,
	}

	req := worker.buildSubmitRequest(job, checkerdef.Result{
		Status:   checkerdef.StatusTimeout,
		Duration: 5 * time.Millisecond,
	}, now)

	r.NotNil(req.Sched)
	r.InDelta(
		scheduling.UpdateEWMA(1000, float64(params.EffectiveCheckTimeout().Milliseconds())),
		req.Sched.CostEWMAMs, 1e-9,
		"a timed-out probe is scored at the execution ceiling, not its truncated duration")
}
