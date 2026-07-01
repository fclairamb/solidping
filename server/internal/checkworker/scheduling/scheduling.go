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

import "time"

// DefaultExecutionTimeout is the historical flat per-check execution ceiling.
// The cost-aware timeout never exceeds it.
const DefaultExecutionTimeout = 30 * time.Second

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
const MaxDeprioritizeOffset = CostOffsetWeight * DefaultExecutionTimeout // 60s = weight × execution ceiling

// Check lanes (spec 2026-07-01-03): every check_jobs row carries a lane
// smallint. Fast jobs may occupy any free runner slot; slow jobs are capped at
// pool_size − fast_lane_reserved in flight per worker, so a burst of slow
// probes can never occupy the whole pool and starve due fast checks. The lane
// is a smallint (not a bool) so a third class can be added without another
// migration.
const (
	// LaneFast is the default lane: checks whose cost EWMA sits below the
	// hysteresis band. New jobs start here (first run is FIFO).
	LaneFast int16 = 0
	// LaneSlow is the capacity-capped lane for expensive checks (cost EWMA at
	// or above LaneSlowThresholdMs).
	LaneSlow int16 = 1
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

	// CostTimeoutFactor multiplies cost_ewma_ms to derive the per-check
	// execution timeout (clamped to [CostTimeoutFloor, DefaultExecutionTimeout]).
	// 0 disables the cost-aware timeout (the flat 30s ceiling is used).
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
func ClassifyLane(prevLane int16, costEWMAMs float64, p Params) int16 {
	if p.LaneSlowThresholdMs <= 0 {
		return prevLane
	}

	switch {
	case costEWMAMs >= p.LaneSlowThresholdMs:
		return LaneSlow
	case costEWMAMs < p.LaneFastThresholdMs:
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
// wins a slot when the due-batch exceeds capacity (Option A). Anti-starvation:
// the offset is clamped to MaxDeprioritizeOffset (60s), so a skipped job's
// receding scheduled_at sorts it ahead of any on-time job within a minute.
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
// The result is clamped to MaxDeprioritizeOffset so the offset stays
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

// ExecutionTimeout returns the per-check execution ceiling. With the cost-aware
// timeout enabled (CostTimeoutFactor > 0) it is clamp(factor × cost,
// floor, 30s); otherwise the flat DefaultExecutionTimeout. A job with no cost
// signal yet (cost 0) falls back to the floor (or 30s if no floor is set), so a
// brand-new check is never starved of time.
func (p Params) ExecutionTimeout(costEWMAMs float64) time.Duration {
	if p.CostTimeoutFactor <= 0 {
		return DefaultExecutionTimeout
	}

	want := time.Duration(p.CostTimeoutFactor * costEWMAMs * float64(time.Millisecond))

	floor := p.CostTimeoutFloor
	if floor <= 0 {
		floor = DefaultExecutionTimeout
	}
	if want < floor {
		want = floor
	}
	if want > DefaultExecutionTimeout {
		want = DefaultExecutionTimeout
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
