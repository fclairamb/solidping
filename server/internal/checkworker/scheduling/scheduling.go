// Package scheduling holds the pure, side-effect-free math for cost-aware,
// plan-weighted check scheduling (spec 2026-06-30-09, cost-only offset per
// spec 2026-07-01-02).
//
// It deliberately knows nothing about the database, the worker pool, or
// goroutines — it is a set of small deterministic functions over a job's cost
// signal (cost_ewma_ms), its plan tier (plan_weight), and a Params struct
// populated from config. The worker and the claim path call into it; the unit
// tests exercise the ordering/weighting/timeout math directly.
//
// The delay EWMA (delay_ewma_ms) is pure telemetry: it is still folded via
// UpdateEWMA on every release and reported by the cost-distribution endpoint,
// but it never steers the claim order. Cost is an offender signal (an
// expensive check occupies a runner slot); delay is a victim signal (the check
// started late because the pool was busy) — deprioritizing on delay punished
// starved checks and spiraled unboundedly under sustained overload.
//
// Inert until there is data: the cost EWMA is 0 until a job's first run, so a
// fresh job's effective deadline equals its real scheduled_at (pure FIFO). The
// tier credit and cost-aware timeout are additionally gated by their Params
// knobs (0 = off), so the zero-value Params reproduces today's behavior.
package scheduling

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
)

// DefaultExecutionTimeout is the fallback flat per-check execution ceiling used
// when no global check timeout is configured (Params.CheckTimeout == 0). The
// cost-aware timeout never exceeds the effective ceiling
// (Params.EffectiveCheckTimeout). It is also the EWMA-pin ceiling and the basis
// of MaxDeprioritizeOffset, so all three stay consistent with the default.
const DefaultExecutionTimeout = 15 * time.Second

// CostOffsetWeight is the multiplier applied to cost_ewma_ms in the
// deprioritization offset:
//
//	effective = scheduled_at + cost_ewma_ms × CostOffsetWeight − tier_credit
//
// An expensive check occupies a worker slot for its whole duration, so it is
// pushed back proportionally harder than its raw cost to give fast checks
// first pick of a contended slot.
const CostOffsetWeight = 2

// MaxDeprioritizeOffset is the hard ceiling on the deprioritization offset.
// It is structural — cost_ewma_ms is pinned to DefaultExecutionTimeout on
// timeout, so cost × CostOffsetWeight can never exceed it — but the explicit
// clamp guarantees no future term added to the offset can reintroduce the
// unbounded-offset stranding pathology (delay-era offsets grew to ~95 min,
// far past the 5-min claim window; spec 2026-07-01-02). Being bounded well
// under the claim window is also the anti-starvation guarantee: a job
// MaxDeprioritizeOffset overdue sorts ahead of any on-time job.
const MaxDeprioritizeOffset = CostOffsetWeight * DefaultExecutionTimeout // 30s = weight × execution ceiling

// Check lanes (spec 2026-07-01-03): every check_jobs row carries a lane
// smallint. Fast jobs may occupy any free runner slot; slow jobs are capped at
// pool_size − fast_lane_reserved in flight per worker, so a burst of slow
// probes can never occupy the whole pool and starve due fast checks. The lane
// is a smallint (not a bool) so a third class can be added without another
// migration.
const (
	// LaneFast is the default lane: checks whose cost EWMA sits below the
	// hysteresis band. New jobs start here (first run is FIFO).
	LaneFast uint8 = 0
	// LaneSlow is the capacity-capped lane for expensive checks (cost EWMA at
	// or above LaneSlowThresholdMs).
	LaneSlow uint8 = 1
)

// Params bundles every tunable knob the scheduling math reads. It is built once
// from config and passed by value (it is tiny and immutable per call).
//
// Durations are time.Duration; thresholds that the DB stores in milliseconds are
// kept as float64 ms to match cost_ewma_ms.
type Params struct {
	// SlowThresholdMs is the dead-band on the deprioritization offset: a job's
	// effective deadline is pushed past its real scheduled_at only once its
	// weighted cost EWMA (cost_ewma_ms × CostOffsetWeight) reaches this many
	// milliseconds. Below it the offset is 0 (effective == scheduled_at), so
	// small per-run variance never reorders fast checks. 0 disables the
	// dead-band (any positive offset applies).
	SlowThresholdMs float64

	// TierCreditPerWeight is the deadline credit granted per unit of plan_weight
	// (paid jobs sort earlier under contention). The total credit is capped at
	// TierCreditMax. 0 disables the tier credit.
	TierCreditPerWeight time.Duration

	// TierCreditMax caps the total tier credit regardless of plan_weight, so a
	// very high weight cannot pull a job arbitrarily far into the past and
	// starve free work. 0 means "no separate cap" — only PerWeight × weight
	// applies.
	TierCreditMax time.Duration

	// CheckTimeout is the configured global per-check execution ceiling
	// (config scheduling.check_timeout_ms). It is the flat timeout when the
	// cost-aware timeout is off, and the upper clamp bound when it is on. 0 =
	// unset, falling back to DefaultExecutionTimeout (15s). Read the effective
	// value via EffectiveCheckTimeout.
	CheckTimeout time.Duration

	// CostTimeoutFactor multiplies cost_ewma_ms to derive the per-check
	// execution timeout (clamped to [CostTimeoutFloor, EffectiveCheckTimeout]).
	// 0 disables the cost-aware timeout (the flat configured ceiling is used).
	CostTimeoutFactor float64

	// CostTimeoutFloor is the minimum cost-aware timeout, so a fast or
	// never-run check is never given an unreasonably short ceiling. Only
	// consulted when CostTimeoutFactor > 0.
	CostTimeoutFloor time.Duration

	// LaneSlowThresholdMs is the promote edge of the lane hysteresis band: a
	// job whose cost EWMA reaches this many ms is classified LaneSlow on its
	// next post-exec write. <= 0 disables lane classification entirely
	// (ClassifyLane holds the current lane), so the zero-value Params keeps
	// every job wherever the DB says it is.
	LaneSlowThresholdMs float64

	// LaneFastThresholdMs is the demote edge of the band: a job whose cost
	// EWMA drops below this many ms returns to LaneFast. Must be strictly
	// below LaneSlowThresholdMs (validated at config load); the [fast, slow)
	// dead-band stops a check hovering at one threshold from flipping lanes
	// every run and churning the partial claim indexes.
	LaneFastThresholdMs float64
}

// ClassifyLane returns the lane a job belongs to after folding in its latest
// cost EWMA, with hysteresis (spec 2026-07-01-03 D2): promote to LaneSlow at
// costEWMAMs >= LaneSlowThresholdMs, demote to LaneFast below
// LaneFastThresholdMs, and hold prevLane inside the band. The classifier is
// deliberately cost-only — delay is a victim signal (spec 2026-07-01-02);
// classifying on it would send starved fast checks into the slow lane, the
// exact inversion of the goal. A zero-cost fresh job classifies fast. With
// LaneSlowThresholdMs <= 0 the classifier is off and prevLane is returned
// unchanged.
func ClassifyLane(prevLane uint8, costEWMAMs float64, params Params) uint8 {
	if params.LaneSlowThresholdMs <= 0 {
		return prevLane
	}

	switch {
	case costEWMAMs >= params.LaneSlowThresholdMs:
		return LaneSlow
	case costEWMAMs < params.LaneFastThresholdMs:
		return LaneFast
	default:
		// Inside the hysteresis dead-band: hold the current lane.
		return prevLane
	}
}

// TierCredit returns how far before scheduled_at a paid job's effective deadline
// is pulled, as a function of its plan weight. Free jobs (weight <= 0) get no
// credit. The result is capped at TierCreditMax (when set) so a very high weight
// cannot starve free work.
func (p Params) TierCredit(planWeight int) time.Duration {
	if p.TierCreditPerWeight <= 0 || planWeight <= 0 {
		return 0
	}

	credit := time.Duration(planWeight) * p.TierCreditPerWeight
	if p.TierCreditMax > 0 && credit > p.TierCreditMax {
		credit = p.TierCreditMax
	}

	return credit
}

// EffectiveScheduledAt computes the WFQ claim ORDER BY key:
//
//	effective = scheduled_at + deprioritize_offset(cost) − tier_credit(weight)
//
// The deprioritization offset is the job's cost EWMA weighted by
// CostOffsetWeight (a slower check sorts later), but only once that value
// crosses SlowThresholdMs — below the threshold the offset is 0, so fast
// checks keep their real-schedule (FIFO) order and small per-run variance
// never reorders them. The offset is a pure offender penalty: the delay EWMA
// is deliberately absent (telemetry-only), because penalizing on delay pushed
// already-starved checks further back and spiraled unboundedly under overload
// (spec 2026-07-01-02). A paid job sorts earlier via the tier credit. The
// claim SELECT gates on the real scheduled_at and only orders by this value,
// so with spare capacity every check runs on time; the offset only decides who
// wins a slot when the due-batch exceeds capacity (Option A).
//
// Anti-starvation invariant: the offset is clamped to MaxDeprioritizeOffset
// (30s = CostOffsetWeight × the 15s execution ceiling), so a job that is more
// than 30s overdue sorts ahead of ANY on-time job, however cheap. A skipped
// job's receding scheduled_at therefore buys it priority within half a minute.
//
// Every write path that skips an execution must uphold this by leaving the
// job's stored effective_scheduled_at behind (receding), not by re-anchoring it
// to the new schedule. The per-org rate-limit deferral used to violate it — it
// advanced scheduled_at AND re-anchored effective_scheduled_at, so a job turned
// away by the token bucket never accumulated overdue-ness and, with phases
// being a stable hash of the check UID, the same checks lost every single
// window (a 1-minute check went 7.5 hours with zero results in production).
// checkjobsvc.DeferLeaseRateLimited now upholds the invariant on both the
// in-process worker path and the deported-agent claim path (spec
// 2026-08-26-02).
func (p Params) EffectiveScheduledAt(scheduledAt time.Time, costEWMAMs float64, planWeight int) time.Time {
	return scheduledAt.
		Add(p.deprioritizeOffset(costEWMAMs)).
		Add(-p.TierCredit(planWeight))
}

// deprioritizeOffset is how far past scheduled_at a job is pushed for being
// expensive: its cost EWMA weighted by CostOffsetWeight — but only once that
// value reaches SlowThresholdMs. Below the threshold it returns 0 (effective
// stays at scheduled_at), so trivial cost never reorders fast checks. A
// non-positive threshold disables the dead-band (any positive offset applies).
// The result is clamped to MaxDeprioritizeOffset (30s) so the offset stays
// structurally bounded no matter what the inputs are.
func (p Params) deprioritizeOffset(costEWMAMs float64) time.Duration {
	total := costEWMAMs * CostOffsetWeight
	if total < p.SlowThresholdMs {
		return 0
	}

	offset := msToDuration(total)
	if offset > MaxDeprioritizeOffset {
		return MaxDeprioritizeOffset
	}

	return offset
}

// msToDuration converts a non-negative millisecond scalar to a Duration,
// flooring negatives to 0 so a stray negative can never pull a job's effective
// deadline earlier than its real schedule.
func msToDuration(ms float64) time.Duration {
	if ms <= 0 {
		return 0
	}

	return time.Duration(ms * float64(time.Millisecond))
}

// EffectiveCheckTimeout is the configured global per-check execution ceiling,
// falling back to DefaultExecutionTimeout (15s) when CheckTimeout is unset (0).
// It is the flat timeout when the cost-aware timeout is off, the upper clamp
// bound when it is on, and the ceiling a timed-out check's cost EWMA is pinned
// to.
func (p Params) EffectiveCheckTimeout() time.Duration {
	if p.CheckTimeout > 0 {
		return p.CheckTimeout
	}

	return DefaultExecutionTimeout
}

// ExecutionTimeout returns the per-check execution ceiling. With the cost-aware
// timeout enabled (CostTimeoutFactor > 0) it is clamp(factor × cost,
// floor, EffectiveCheckTimeout); otherwise the flat EffectiveCheckTimeout. A
// job with no cost signal yet (cost 0, never ran) gets the full
// EffectiveCheckTimeout — NOT the floor (spec 2026-07-01-04 D4): giving a first
// run only the floor would time out a legitimately slow check (e.g. a fresh
// browser probe), pin its cost EWMA to the ceiling, and poison the signal
// before it ever measured honestly.
func (p Params) ExecutionTimeout(costEWMAMs float64) time.Duration {
	ceiling := p.EffectiveCheckTimeout()

	if p.CostTimeoutFactor <= 0 || costEWMAMs <= 0 {
		return ceiling
	}

	want := time.Duration(p.CostTimeoutFactor * costEWMAMs * float64(time.Millisecond))

	floor := p.CostTimeoutFloor
	if floor <= 0 {
		floor = ceiling
	}
	if want < floor {
		want = floor
	}
	if want > ceiling {
		want = ceiling
	}

	return want
}

// EWMAAlpha is the smoothing factor for the cost and delay EWMAs: value =
// α·sample + (1−α)·prev. A modest α keeps a single blip from dominating while
// still tracking a sustained change within a handful of runs.
const EWMAAlpha = 0.3

// UpdateEWMA folds a new millisecond sample into a running EWMA (used for both
// the cost and the delay signals). The first sample (prev <= 0) seeds the
// average directly so the value converges from its very first run rather than
// starting biased toward 0. Negative samples are floored to 0.
func UpdateEWMA(prev, sampleMs float64) float64 {
	if sampleMs < 0 {
		sampleMs = 0
	}
	if prev <= 0 {
		return sampleMs
	}

	return EWMAAlpha*sampleMs + (1-EWMAAlpha)*prev
}

// PostExecInput is everything the post-exec accounting needs about one finished
// execution: the job's stored scheduling state before the run, the outcome, and
// the next tick.
type PostExecInput struct {
	// PrevCostEWMAMs / PrevDelayEWMAMs / PrevLane are the job's stored values.
	PrevCostEWMAMs  float64
	PrevDelayEWMAMs float64
	PrevLane        uint8
	// PlanWeight is the job's plan tier weight (tier credit input).
	PlanWeight int
	// ScheduledAt is the schedule the probe was supposed to start at, and
	// EffectiveScheduledAt the stored ordering key used as its fallback.
	ScheduledAt          *time.Time
	EffectiveScheduledAt *time.Time
	// DurationMs is the measured probe duration in milliseconds.
	DurationMs float64
	// TimedOut pins the cost sample to the execution ceiling.
	TimedOut bool
	// ExecStart is the wall-clock instant the outbound probe began. nil means
	// no delay sample is available (e.g. an agent predating the wire field), in
	// which case the delay EWMA is carried over unchanged rather than being fed
	// a fabricated 0.
	ExecStart *time.Time
	// NextScheduledAt is the job's next tick, the anchor of the new effective
	// deadline.
	NextScheduledAt time.Time
}

// PostExecState is the scheduling state written back with the lease release.
type PostExecState struct {
	CostEWMAMs           float64
	DelayEWMAMs          float64
	EffectiveScheduledAt time.Time
	Lane                 uint8
}

// PostExec folds one execution's outcome into the job's scheduling state: the
// cost and delay EWMAs, the recomputed effective deadline, and the
// hysteresis-classified lane.
//
// This is the single implementation of the post-exec accounting. The in-process
// worker computes it locally and persists it through DirectBackend; the
// server-side submit path (handlers/workers) computes the same thing for
// results that arrive over the agent transport, which is what keeps fair
// scheduling honest on a shared cloud region served by platform agents (spec
// 2026-07-27-01 item 4).
//
// The effective deadline and the lane are derived from the cost EWMA ONLY
// (specs 2026-07-01-02 / 2026-07-01-03): the delay EWMA is persisted as
// telemetry but never steers claim order or lane. That is also why an
// agent-reported ExecStart cannot distort scheduling — a clock-skewed sample
// only moves a number nothing reads back, and the floor at 0 absorbs backwards
// clocks.
func (p Params) PostExec(input *PostExecInput) PostExecState {
	cost := UpdateEWMA(input.PrevCostEWMAMs, p.CostSampleMs(input.DurationMs, input.TimedOut))

	delay := input.PrevDelayEWMAMs
	if input.ExecStart != nil {
		delay = UpdateEWMA(input.PrevDelayEWMAMs,
			DelaySampleMs(input.ScheduledAt, input.EffectiveScheduledAt, *input.ExecStart))
	}

	return PostExecState{
		CostEWMAMs:           cost,
		DelayEWMAMs:          delay,
		EffectiveScheduledAt: p.EffectiveScheduledAt(input.NextScheduledAt, cost, input.PlanWeight),
		Lane:                 ClassifyLane(input.PrevLane, cost, p),
	}
}

// CostSampleMs derives the cost sample (ms) folded into the EWMA from one
// execution. A timeout is pinned to the execution ceiling so a perpetually
// timing-out check converges to the maximum cost (and stays slow-classified)
// rather than being scored by the partial duration measured before the deadline
// fired.
func (p Params) CostSampleMs(durationMs float64, timedOut bool) float64 {
	if timedOut {
		return float64(p.EffectiveCheckTimeout().Milliseconds())
	}

	return durationMs
}

// DelaySampleMs is the scheduling-delay sample (ms): how far past the job's real
// scheduled_at the probe actually started, floored at 0 — true start lateness vs
// the schedule the user configured (spec 2026-07-01-02 D4). The runner sleeps
// until scheduled_at, so under spare capacity the probe starts on time and this
// is 0; it only goes positive when contention pushes the start past the
// schedule. Falls back to the effective deadline when the row has no
// scheduled_at (should not happen in practice).
func DelaySampleMs(scheduledAt, effectiveScheduledAt *time.Time, execStart time.Time) float64 {
	ref := scheduledAt
	if ref == nil {
		ref = effectiveScheduledAt
	}

	if ref == nil {
		return 0
	}

	lateness := execStart.Sub(*ref)
	if lateness < 0 {
		return 0
	}

	return float64(lateness.Milliseconds())
}

// ParamsFromConfig converts the koanf SchedulingConfig (seconds / ms scalars)
// into typed Params. Every zero value maps to "feature off", so the default
// config reproduces pure-FIFO, flat-30s behavior.
//
// It lives here rather than in the worker because both sides of the post-exec
// accounting need it: the in-process worker and the server-side submit path
// that scores results arriving over the agent transport.
func ParamsFromConfig(cfg config.SchedulingConfig) Params {
	secs := func(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }
	millis := func(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

	return Params{
		SlowThresholdMs:     cfg.SlowThresholdMs,
		CheckTimeout:        millis(cfg.CheckTimeoutMs),
		TierCreditPerWeight: secs(cfg.TierCreditSeconds),
		TierCreditMax:       secs(cfg.TierCreditMaxSeconds),
		CostTimeoutFactor:   cfg.CostTimeoutFactor,
		CostTimeoutFloor:    millis(cfg.CostTimeoutFloorMs),
		LaneSlowThresholdMs: cfg.LaneSlowThresholdMs,
		LaneFastThresholdMs: cfg.LaneFastThresholdMs,
	}
}
