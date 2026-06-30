package checkworker

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
)

func TestAdmissionDisabledByDefault(t *testing.T) {
	t.Parallel()

	// Zero config = no caps: every class is always admitted (today's behavior).
	a := newAdmissionController(5, config.SchedulingConfig{})

	for i := 0; i < 100; i++ {
		require.Equal(t, admitOK, a.tryAdmit(true, false), "slow free job must be admitted with no caps")
	}
	inFlight, slow, _ := a.snapshot()
	require.Equal(t, 100, inFlight)
	require.Equal(t, 100, slow)
}

// TestSlowLaneCap: the spec's core slow-lane guarantee — never more than K slow
// checks run at once, and fast checks always find a slot.
func TestSlowLaneCap(t *testing.T) {
	t.Parallel()

	const poolSize, slowMax = 5, 2
	a := newAdmissionController(poolSize, config.SchedulingConfig{
		SlowCostThresholdMs: 1000,
		SlowLaneMax:         slowMax,
	})

	// Fill the slow lane to its cap.
	require.Equal(t, admitOK, a.tryAdmit(true, false))
	require.Equal(t, admitOK, a.tryAdmit(true, false))

	// The next slow check is denied — the slow lane is full.
	require.Equal(t, admitDeniedSlow, a.tryAdmit(true, false),
		"slow checks beyond the cap must be deferred")

	_, slow, _ := a.snapshot()
	require.Equal(t, slowMax, slow, "never more than K slow checks in flight")

	// Fast checks are unaffected and can still fill the remaining slots.
	for i := 0; i < poolSize-slowMax; i++ {
		require.Equal(t, admitOK, a.tryAdmit(false, false),
			"fast checks must always be admittable while the slow lane is capped")
	}

	// Releasing a slow check frees a slow-lane slot for another slow check.
	a.release(true, false)
	require.Equal(t, admitOK, a.tryAdmit(true, false),
		"a released slow slot must become available again")
}

// TestPaidReservedFloor: free-tier in-flight never exceeds poolSize − R, and a
// paid job is always admitted into the reserved slots even under a free stampede.
func TestPaidReservedFloor(t *testing.T) {
	t.Parallel()

	const poolSize, reserved = 5, 2 // 3 slots shareable, 2 reserved for paid
	a := newAdmissionController(poolSize, config.SchedulingConfig{PaidReserved: reserved})

	// A free-tier stampede can occupy at most poolSize − reserved slots.
	admittedFree := 0
	for i := 0; i < poolSize*2; i++ {
		if a.tryAdmit(false, false) == admitOK {
			admittedFree++
		}
	}
	require.Equal(t, poolSize-reserved, admittedFree,
		"free-tier in-flight must be capped at poolSize − R")

	_, _, free := a.snapshot()
	require.Equal(t, poolSize-reserved, free)

	// Despite the free stampede, paid jobs are admitted into the reserved slots.
	require.Equal(t, admitOK, a.tryAdmit(false, true), "paid job claims a reserved slot")
	require.Equal(t, admitOK, a.tryAdmit(false, true), "paid job claims the last reserved slot")

	inFlight, _, _ := a.snapshot()
	require.Equal(t, poolSize, inFlight, "pool fully utilized: free up to the floor + paid in reserve")
}

// TestPaidNotCountedAsFree confirms paid jobs do not consume the free budget, so
// a healthy mix of paid + free never wrongly blocks free work.
func TestPaidNotCountedAsFree(t *testing.T) {
	t.Parallel()

	a := newAdmissionController(5, config.SchedulingConfig{PaidReserved: 2})

	// Admit some paid jobs first.
	require.Equal(t, admitOK, a.tryAdmit(false, true))
	require.Equal(t, admitOK, a.tryAdmit(false, true))

	// Free jobs still get their full poolSize − reserved = 3 budget.
	for i := 0; i < 3; i++ {
		require.Equal(t, admitOK, a.tryAdmit(false, false))
	}
	require.Equal(t, admitDeniedReserve, a.tryAdmit(false, false),
		"free budget is poolSize − R regardless of how many paid jobs run")
}

// TestCapsClampedToPool: a misconfiguration (reserved >= poolSize) must not wedge
// the pool — caps are clamped so at least the intended behavior holds.
func TestCapsClampedToPool(t *testing.T) {
	t.Parallel()

	// reserved = poolSize would block every free job; clamped to poolSize means
	// free budget is 0 (every free job deferred) but paid still flows. Negative
	// is clamped to 0.
	a := newAdmissionController(3, config.SchedulingConfig{PaidReserved: 99, SlowLaneMax: -1})
	require.Equal(t, 3, a.paidReserved)
	require.Equal(t, 0, a.slowLaneMax)
}

func TestClassify(t *testing.T) {
	t.Parallel()

	a := newAdmissionController(5, config.SchedulingConfig{})
	p := scheduling.Params{SlowCostThresholdMs: 1000}

	isSlow, isPaid := a.classify(p, 2000, 0)
	require.True(t, isSlow, "cost above threshold = slow")
	require.False(t, isPaid, "weight 0 = free")

	isSlow, isPaid = a.classify(p, 200, 3)
	require.False(t, isSlow, "cost below threshold = fast")
	require.True(t, isPaid, "weight > 0 = paid")
}

// TestAdmissionConcurrentSafety hammers the controller from many goroutines to
// catch counter races (run with -race).
func TestAdmissionConcurrentSafety(t *testing.T) {
	t.Parallel()

	a := newAdmissionController(8, config.SchedulingConfig{SlowCostThresholdMs: 1, SlowLaneMax: 3, PaidReserved: 2})

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			isSlow := n%2 == 0
			isPaid := n%3 == 0
			if a.tryAdmit(isSlow, isPaid) == admitOK {
				a.release(isSlow, isPaid)
			}
		}(i)
	}
	wg.Wait()

	inFlight, slow, free := a.snapshot()
	require.Equal(t, 0, inFlight, "all admits released: in-flight must return to 0")
	require.Equal(t, 0, slow)
	require.Equal(t, 0, free)
}
