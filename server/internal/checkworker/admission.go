package checkworker

import (
	"sync"

	"github.com/fclairamb/solidping/server/internal/config"
)

// admissionController is the per-worker capacity-isolation primitive
// (spec 2026-06-30-09, Phase 1). Ordering alone cannot guarantee a fast or paid
// burst finds a free runner once slow/free jobs have committed every slot
// (execution is non-preemptible), so this partitions the pool:
//
//   - Slow lane: at most slowLaneMax slow checks may execute simultaneously,
//     guaranteeing poolSize − slowLaneMax slots stay claimable by fast checks.
//   - Paid reserve: free-tier in-flight is capped at poolSize − paidReserved,
//     reserving paidReserved slots that only paid (plan_weight > 0) jobs may
//     occupy. This is the Q2 hard floor.
//
// It is admission-only: a denied job is rescheduled (its lease released) and
// reclaimed later, never preempted mid-probe. Caps are enforced per-worker
// (availableRunners is per-worker); with homogeneous workers a per-worker
// fraction approximates a global reservation but is not a strict global
// guarantee (D5).
//
// Disabled knobs (0) collapse a lane to "no cap": slowLaneMax 0 → slow checks
// are never throttled, paidReserved 0 → no slots reserved. So the zero config
// reproduces today's single-FIFO-pool behaviour.
type admissionController struct {
	poolSize     int
	slowLaneMax  int // max concurrent slow checks; 0 = unlimited (no slow lane)
	paidReserved int // slots reserved for paid jobs; 0 = none reserved

	mu       sync.Mutex
	inFlight int // total checks currently executing on this worker
	slow     int // currently-executing checks classified as slow
	free     int // currently-executing checks belonging to free-tier orgs
}

// newAdmissionController builds a controller for a pool of poolSize runners.
// Caps are clamped to [0, poolSize] so a misconfiguration cannot wedge the pool
// (e.g. paidReserved >= poolSize would block every free job forever).
func newAdmissionController(poolSize int, c config.SchedulingConfig) *admissionController {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > poolSize {
			return poolSize
		}

		return v
	}

	return &admissionController{
		poolSize:     poolSize,
		slowLaneMax:  clamp(c.SlowLaneMax),
		paidReserved: clamp(c.PaidReserved),
	}
}

// Admission-denial reasons, used as the metric class label and "" on success.
const (
	admitOK            = ""
	admitDeniedSlow    = "slow_lane"
	admitDeniedReserve = "paid_reserved"
)

// tryAdmit attempts to reserve a runner slot for a job with the given class.
// Returns admitOK and records the occupancy if the job may run now; otherwise a
// non-empty reason naming the cap that would be breached (the caller then
// reschedules the job). Every successful admit must be paired with exactly one
// release.
func (a *admissionController) tryAdmit(isSlow, isPaid bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Slow lane: never let more than slowLaneMax slow checks run at once, so
	// fast checks always have poolSize − slowLaneMax slots available.
	if isSlow && a.slowLaneMax > 0 && a.slow >= a.slowLaneMax {
		return admitDeniedSlow
	}

	// Paid reserve: free-tier in-flight may not exceed poolSize − paidReserved,
	// keeping paidReserved slots claimable only by paid jobs.
	if !isPaid && a.paidReserved > 0 && a.free >= a.poolSize-a.paidReserved {
		return admitDeniedReserve
	}

	a.inFlight++
	if isSlow {
		a.slow++
	}
	if !isPaid {
		a.free++
	}

	return admitOK
}

// release returns the slot reserved by a matching tryAdmit. The class flags must
// match the admit call so the per-class counters stay balanced.
func (a *admissionController) release(isSlow, isPaid bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inFlight > 0 {
		a.inFlight--
	}
	if isSlow && a.slow > 0 {
		a.slow--
	}
	if !isPaid && a.free > 0 {
		a.free--
	}
}

// snapshot returns the current occupancy counters (for metrics / tests).
func (a *admissionController) snapshot() (inFlight, slow, free int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight, a.slow, a.free
}

// classify decides whether a job is slow and/or paid from its denormalized
// scheduling fields. A non-positive plan_weight is free tier; slowness is
// delegated to the pure scheduling math (cost EWMA vs threshold).
func (a *admissionController) classify(p schedClassifier, costEWMAMs float64, planWeight int) (isSlow, isPaid bool) {
	return p.IsSlow(costEWMAMs), planWeight > 0
}

// schedClassifier is the slice of scheduling.Params the admission controller
// needs — kept as an interface so the classifier can be exercised in isolation.
type schedClassifier interface {
	IsSlow(costEWMAMs float64) bool
}
