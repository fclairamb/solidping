package scheduling_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
)

// postExecParams is a fully-enabled parameter set: every knob non-zero so a
// regression in any one of them shows up.
func postExecParams() scheduling.Params {
	return scheduling.Params{
		SlowThresholdMs:     1000,
		CheckTimeout:        30 * time.Second,
		TierCreditPerWeight: 2 * time.Second,
		TierCreditMax:       10 * time.Second,
		CostTimeoutFactor:   3,
		CostTimeoutFloor:    2 * time.Second,
		LaneSlowThresholdMs: 1200,
		LaneFastThresholdMs: 800,
	}
}

// TestPostExecMatchesPrimitives is the parity test the accounting hoist rests
// on: PostExec must be exactly the composition of the primitives the in-process
// worker used to apply inline (UpdateEWMA over CostSampleMs / DelaySampleMs,
// EffectiveScheduledAt, ClassifyLane). If it ever drifts, the in-cluster and
// the agent paths would score the same execution differently — which is the
// whole failure this spec exists to prevent.
func TestPostExecMatchesPrimitives(t *testing.T) {
	t.Parallel()

	params := postExecParams()
	now := time.Now()
	scheduledAt := now.Add(-3 * time.Second)
	effective := now.Add(-1 * time.Second)
	execStart := now
	next := now.Add(time.Minute)

	in := scheduling.PostExecInput{
		PrevCostEWMAMs:       900,
		PrevDelayEWMAMs:      120,
		PrevLane:             0,
		PlanWeight:           3,
		ScheduledAt:          &scheduledAt,
		EffectiveScheduledAt: &effective,
		DurationMs:           2500,
		TimedOut:             false,
		ExecStart:            &execStart,
		NextScheduledAt:      next,
	}

	got := params.PostExec(&in)

	wantCost := scheduling.UpdateEWMA(in.PrevCostEWMAMs, params.CostSampleMs(in.DurationMs, in.TimedOut))
	wantDelay := scheduling.UpdateEWMA(in.PrevDelayEWMAMs,
		scheduling.DelaySampleMs(in.ScheduledAt, in.EffectiveScheduledAt, execStart))

	r := require.New(t)
	r.InDelta(wantCost, got.CostEWMAMs, 1e-9)
	r.InDelta(wantDelay, got.DelayEWMAMs, 1e-9)
	r.Equal(params.EffectiveScheduledAt(next, wantCost, in.PlanWeight), got.EffectiveScheduledAt)
	r.Equal(scheduling.ClassifyLane(in.PrevLane, wantCost, params), got.Lane)
}

// TestPostExecTimeoutPinsCostSample: a timeout is scored at the execution
// ceiling, not at the partial duration measured before the deadline fired.
func TestPostExecTimeoutPinsCostSample(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	params := postExecParams()
	next := time.Now().Add(time.Minute)

	timedOut := params.PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:  1000,
		DurationMs:      5, // the probe barely started before the deadline
		TimedOut:        true,
		NextScheduledAt: next,
	})
	cheap := params.PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:  1000,
		DurationMs:      5,
		TimedOut:        false,
		NextScheduledAt: next,
	})

	r.Greater(timedOut.CostEWMAMs, cheap.CostEWMAMs,
		"a timeout must cost more than the truncated duration it measured")
	r.InDelta(
		scheduling.UpdateEWMA(1000, float64(params.EffectiveCheckTimeout().Milliseconds())),
		timedOut.CostEWMAMs, 1e-9)
}

// TestPostExecWithoutExecStartCarriesDelay: an executor that does not report a
// probe start (an agent predating the wire field) contributes NO delay sample —
// the stored EWMA is carried over rather than being fed a fabricated 0, which
// would silently decay the metric towards zero on every remote result.
func TestPostExecWithoutExecStartCarriesDelay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	params := postExecParams()
	scheduledAt := time.Now().Add(-time.Minute)

	got := params.PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:  500,
		PrevDelayEWMAMs: 250,
		ScheduledAt:     &scheduledAt,
		DurationMs:      500,
		ExecStart:       nil,
		NextScheduledAt: time.Now().Add(time.Minute),
	})

	r.InDelta(250.0, got.DelayEWMAMs, 1e-9, "no sample means the delay EWMA is untouched")
	r.NotEqual(500.0, got.DelayEWMAMs)
}

// TestPostExecDelayIsFlooredAtZero: a backwards agent clock (execStart before
// the schedule) yields 0, never a negative sample. This is what makes accepting
// an agent-reported timestamp safe.
func TestPostExecDelayIsFlooredAtZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	params := postExecParams()
	now := time.Now()
	scheduledAt := now.Add(time.Hour) // agent clock is an hour behind
	execStart := now

	got := params.PostExec(&scheduling.PostExecInput{
		PrevDelayEWMAMs: 0,
		ScheduledAt:     &scheduledAt,
		DurationMs:      10,
		ExecStart:       &execStart,
		NextScheduledAt: now.Add(time.Minute),
	})

	r.Zero(got.DelayEWMAMs)
}

// TestPostExecDelayNeverSteersScheduling: the delay EWMA is telemetry. Two runs
// differing ONLY in their delay sample must produce the same effective deadline
// and the same lane — that is why an agent's (skewable) clock cannot distort
// fair scheduling.
func TestPostExecDelayNeverSteersScheduling(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	params := postExecParams()
	now := time.Now()
	next := now.Add(time.Minute)
	onTime := now
	veryLate := now.Add(time.Hour)
	scheduledAt := now

	base := scheduling.PostExecInput{
		PrevCostEWMAMs:  900,
		PrevDelayEWMAMs: 100,
		PlanWeight:      2,
		ScheduledAt:     &scheduledAt,
		DurationMs:      1500,
		NextScheduledAt: next,
	}

	punctual := base
	punctual.ExecStart = &onTime
	late := base
	late.ExecStart = &veryLate

	got1 := params.PostExec(&punctual)
	got2 := params.PostExec(&late)

	r.NotEqual(got1.DelayEWMAMs, got2.DelayEWMAMs, "the delay sample did differ")
	r.InDelta(got1.CostEWMAMs, got2.CostEWMAMs, 1e-9)
	r.Equal(got1.EffectiveScheduledAt, got2.EffectiveScheduledAt)
	r.Equal(got1.Lane, got2.Lane)
}
