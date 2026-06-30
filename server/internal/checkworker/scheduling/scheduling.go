// Package scheduling holds the pure, side-effect-free math for cost-aware,
// plan-weighted check scheduling (spec 2026-06-30-09).
//
// It deliberately knows nothing about the database, the worker pool, or
// goroutines — it is a set of small deterministic functions over a job's cost
// signal (cost_ewma_ms), its plan tier (plan_weight), and a Params struct
// populated from config. The worker and the claim path call into it; the unit
// tests exercise the ordering/weighting/classification/timeout math directly.
//
// Off-by-default: with the zero-value Params (all knobs 0) every function
// reproduces today's behavior — no penalty, no credit, never slow, the flat
// 30s timeout — so the feature is inert until config turns it on.
package scheduling

import "time"

// DefaultExecutionTimeout is the historical flat per-check execution ceiling.
// The cost-aware timeout never exceeds it.
const DefaultExecutionTimeout = 30 * time.Second

// Params bundles every tunable knob the scheduling math reads. It is built once
// from config and passed by value (it is tiny and immutable per call).
//
// Durations are time.Duration; thresholds that the DB stores in milliseconds are
// kept as float64 ms to match cost_ewma_ms.
type Params struct {
	// SlowCostThresholdMs classifies a job as "slow" when its cost EWMA exceeds
	// this many milliseconds. 0 disables slow classification entirely (no job is
	// ever slow), which also disables the slow-lane cap.
	SlowCostThresholdMs float64

	// PenaltyCap bounds how far a slow job's effective deadline may be pushed
	// past its real scheduled_at. The cap is what makes anti-starvation work:
	// effective stays anchored to the absolute scheduled_at, so a repeatedly
	// skipped job's scheduled_at recedes into the past and eventually sorts
	// ahead of fresh work. 0 disables the cost penalty.
	PenaltyCap time.Duration

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
}

// IsSlow reports whether a job with the given cost EWMA is classified as slow.
// A non-positive threshold disables classification (nothing is slow).
func (p Params) IsSlow(costEWMAMs float64) bool {
	if p.SlowCostThresholdMs <= 0 {
		return false
	}

	return costEWMAMs > p.SlowCostThresholdMs
}

// CostPenalty returns how far past scheduled_at a job's effective deadline is
// pushed for being expensive. Only slow jobs incur a penalty; it scales with how
// far the cost exceeds the threshold and is hard-capped at PenaltyCap.
//
// Returning the cap (rather than something unbounded) is what guarantees
// anti-starvation: the effective deadline can never drift more than PenaltyCap
// past the real schedule, so the absolute scheduled_at anchor always wins
// eventually.
func (p Params) CostPenalty(costEWMAMs float64) time.Duration {
	if p.PenaltyCap <= 0 || !p.IsSlow(costEWMAMs) {
		return 0
	}

	// Scale linearly with the overshoot past the threshold, expressed in
	// multiples of the threshold, then clamp to the cap. A job at exactly 2×
	// the threshold gets ~1× the cap's worth of penalty before clamping.
	overshoot := (costEWMAMs - p.SlowCostThresholdMs) / p.SlowCostThresholdMs
	penalty := time.Duration(overshoot * float64(p.PenaltyCap))
	if penalty > p.PenaltyCap {
		penalty = p.PenaltyCap
	}
	if penalty < 0 {
		penalty = 0
	}

	return penalty
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

// EffectiveScheduledAt computes the WFQ ordering key:
//
//	effective = scheduled_at + cost_penalty(cost) − tier_credit(weight)
//
// The claim SELECT still gates on the real scheduled_at and only orders by this
// value, so with spare capacity every check runs on time; the penalty/credit
// only decide who wins a slot when the due-batch exceeds capacity (Option A).
func (p Params) EffectiveScheduledAt(scheduledAt time.Time, costEWMAMs float64, planWeight int) time.Time {
	return scheduledAt.
		Add(p.CostPenalty(costEWMAMs)).
		Add(-p.TierCredit(planWeight))
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

// EWMAAlpha is the smoothing factor for the cost EWMA: cost = α·sample +
// (1−α)·prev. A modest α keeps a single slow blip from immediately flipping a
// job into the slow lane while still tracking sustained cost within a handful of
// runs.
const EWMAAlpha = 0.3

// UpdateCostEWMA folds a new duration sample into the running EWMA. The first
// sample (prev == 0) seeds the average directly so a job's cost converges from
// its very first run rather than starting biased toward 0.
func UpdateCostEWMA(prev, sampleMs float64) float64 {
	if sampleMs < 0 {
		sampleMs = 0
	}
	if prev <= 0 {
		return sampleMs
	}

	return EWMAAlpha*sampleMs + (1-EWMAAlpha)*prev
}
