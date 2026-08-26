package scheduling_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
)

// defaultParams is a representative "feature on" configuration used across the
// ordering/weighting tests. Knobs are chosen so the math is easy to reason about.
func defaultParams() scheduling.Params {
	return scheduling.Params{
		TierCreditPerWeight: 5 * time.Second,
		TierCreditMax:       15 * time.Second,
		CostTimeoutFactor:   3,
		CostTimeoutFloor:    2 * time.Second,
	}
}

func TestTierCredit(t *testing.T) {
	t.Parallel()

	p := defaultParams()

	require.Zero(t, p.TierCredit(0), "free tier (weight 0) gets no credit")
	require.Zero(t, p.TierCredit(-1), "negative weight gets no credit")
	require.Equal(t, 5*time.Second, p.TierCredit(1), "weight 1 = one unit of credit")
	require.Equal(t, 10*time.Second, p.TierCredit(2))
	require.Equal(t, p.TierCreditMax, p.TierCredit(100), "credit is capped regardless of weight")

	// Higher weight never gets LESS credit than lower weight.
	require.GreaterOrEqual(t, p.TierCredit(3), p.TierCredit(1))
}

// TestEffectiveOrdering is the heart of the spec: under contention, fast and
// paid jobs must sort ahead of slow and free jobs, while the absolute anchor
// guarantees no permanent starvation. The offset is cost-only (spec
// 2026-07-01-02): delay is telemetry and never appears in the signature.
func TestEffectiveOrdering(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// All jobs are due at the same real scheduled_at; vary cost / tier.
	fastFree := p.EffectiveScheduledAt(base, 200, 0)
	slowFree := p.EffectiveScheduledAt(base, 30000, 0)
	fastPaid := p.EffectiveScheduledAt(base, 200, 2)
	slowPaid := p.EffectiveScheduledAt(base, 30000, 2)

	// A slow check is de-prioritized relative to a fast one (same tier) — it
	// sorts LATER (larger effective deadline).
	require.True(t, fastFree.Before(slowFree),
		"fast free check must sort before slow free check")

	// Paid is impacted less than free — at equal cost, the paid job sorts
	// earlier (smaller effective deadline).
	require.True(t, fastPaid.Before(fastFree),
		"fast paid check must sort before fast free check")
	require.True(t, slowPaid.Before(slowFree),
		"slow paid check must sort before slow free check at equal cost")
}

// TestAntiStarvation: a permanently-skipped check has its real scheduled_at
// recede into the past; because the cost offset is clamped to
// MaxDeprioritizeOffset (30s), a job that far overdue sorts ahead of ANY
// on-time job — even one with zero offset. No cap knob is needed — the
// absolute scheduled_at anchor plus the bounded offset is the guarantee.
func TestAntiStarvation(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Fresh job due right now with no offset at all (never-run: cost 0).
	freshZeroOffset := p.EffectiveScheduledAt(now, 0, 0)

	// Maximally-penalized job that is just over MaxDeprioritizeOffset overdue:
	// even the worst-case clamped offset cannot keep it behind fresh work.
	overdue := now.Add(-scheduling.MaxDeprioritizeOffset - time.Second)
	starved := p.EffectiveScheduledAt(overdue, 1e9, 0)

	require.True(t, starved.Before(freshZeroOffset),
		"a job more than MaxDeprioritizeOffset overdue must outrank any on-time job")
}

// TestMaxDeprioritizeOffsetInvariant asserts the structural bound (spec
// 2026-07-01-02 D2): no cost input — however absurd — can push a job's
// effective deadline more than MaxDeprioritizeOffset past its scheduled_at, so
// the delay-era stranding pathology (offsets beyond the claim window) cannot
// be reintroduced by any stored EWMA value.
func TestMaxDeprioritizeOffsetInvariant(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	bound := base.Add(scheduling.MaxDeprioritizeOffset)

	for _, p := range []scheduling.Params{{}, defaultParams(), {SlowThresholdMs: 2000}} {
		for _, cost := range []float64{0, 30000, 1e6, 1e9, 1e15} {
			got := p.EffectiveScheduledAt(base, cost, 0)
			require.False(t, got.After(bound),
				"offset must never exceed MaxDeprioritizeOffset (cost=%v, params=%+v)", cost, p)
		}
	}

	// The clamp binds exactly at the ceiling: an absurd cost yields
	// scheduled_at + MaxDeprioritizeOffset, not more.
	var p scheduling.Params
	require.Equal(t, bound, p.EffectiveScheduledAt(base, 1e9, 0),
		"an absurd cost clamps to exactly scheduled_at + MaxDeprioritizeOffset")

	// The ceiling is structurally consistent with the cost pinning: a check
	// pinned to the execution ceiling on timeout reaches exactly the clamp.
	pinned := float64(scheduling.DefaultExecutionTimeout.Milliseconds())
	require.Equal(t, bound, p.EffectiveScheduledAt(base, pinned, 0),
		"cost pinned to the execution ceiling lands exactly on MaxDeprioritizeOffset")
}

func TestExecutionTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		params     scheduling.Params
		costEWMAMs float64
		want       time.Duration
	}{
		{
			name:       "disabled falls back to the flat default ceiling",
			params:     scheduling.Params{CostTimeoutFactor: 0},
			costEWMAMs: 200,
			want:       scheduling.DefaultExecutionTimeout,
		},
		{
			name:       "fast check gets a short ceiling above the floor",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 1000, // 3 × 1000ms = 3s
			want:       3 * time.Second,
		},
		{
			name:       "very fast check is clamped up to the floor",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 100, // 3 × 100ms = 300ms < floor
			want:       2 * time.Second,
		},
		{
			name:       "no cost signal yet gets the full default ceiling, not the floor",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 0,
			want:       scheduling.DefaultExecutionTimeout,
		},
		{
			name:       "chronic offender is clamped to the default ceiling",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 60000, // 3 × 60s = 180s, clamped
			want:       scheduling.DefaultExecutionTimeout,
		},
		{
			name: "configured CheckTimeout raises the upper clamp bound",
			params: scheduling.Params{
				CheckTimeout: 25 * time.Second, CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second,
			},
			costEWMAMs: 60000, // 3 × 60s = 180s, clamped to the 25s configured ceiling
			want:       25 * time.Second,
		},
		{
			name: "configured CheckTimeout lowers the upper clamp bound",
			params: scheduling.Params{
				CheckTimeout: 8 * time.Second, CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second,
			},
			costEWMAMs: 60000, // 3 × 60s = 180s, clamped to the 8s configured ceiling
			want:       8 * time.Second,
		},
		{
			name:       "configured CheckTimeout is the flat timeout when cost-aware is off",
			params:     scheduling.Params{CheckTimeout: 20 * time.Second, CostTimeoutFactor: 0},
			costEWMAMs: 200,
			want:       20 * time.Second,
		},
		{
			name:       "unset CheckTimeout falls back to the default ceiling",
			params:     scheduling.Params{CheckTimeout: 0, CostTimeoutFactor: 0},
			costEWMAMs: 200,
			want:       scheduling.DefaultExecutionTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.params.ExecutionTimeout(tt.costEWMAMs))
		})
	}
}

// TestExecutionTimeoutDefaultOnValues pins the spec 2026-07-01-04 D4
// verification table for the production defaults (factor 3, floor 5s) with the
// new 15s global ceiling (spec 2026-07-10-11): a never-run job keeps the full
// 15s (cold-start fix), a fast check's worst-case slot occupancy drops to the
// 5s floor, a mid-cost check gets 3× its measured cost, and a slow check is
// clamped to the 15s ceiling.
func TestExecutionTimeoutDefaultOnValues(t *testing.T) {
	t.Parallel()

	p := scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 5 * time.Second}

	require.Equal(t, 15*time.Second, p.ExecutionTimeout(0), "cost 0 (never ran) → full default ceiling")
	require.Equal(t, 5*time.Second, p.ExecutionTimeout(200), "cost 200ms → 600ms, clamped up to the 5s floor")
	require.Equal(t, 12*time.Second, p.ExecutionTimeout(4000), "cost 4s → 3×4s = 12s")
	require.Equal(t, 15*time.Second, p.ExecutionTimeout(20000), "cost 20s → 60s, clamped to the 15s ceiling")
}

// TestEffectiveCheckTimeout covers the configurable global check timeout (spec
// 2026-07-10-11): a configured CheckTimeout is used verbatim; unset (0) falls
// back to the 15s DefaultExecutionTimeout.
func TestEffectiveCheckTimeout(t *testing.T) {
	t.Parallel()

	require.Equal(t, 15*time.Second, scheduling.DefaultExecutionTimeout,
		"the built-in default ceiling is 15s")
	require.Equal(t, scheduling.DefaultExecutionTimeout, (scheduling.Params{}).EffectiveCheckTimeout(),
		"unset CheckTimeout falls back to the default ceiling")
	require.Equal(t, 22*time.Second, (scheduling.Params{CheckTimeout: 22 * time.Second}).EffectiveCheckTimeout(),
		"a configured CheckTimeout is used verbatim")
}

func TestUpdateEWMA(t *testing.T) {
	t.Parallel()

	// First sample seeds the average directly.
	require.InDelta(t, 500.0, scheduling.UpdateEWMA(0, 500), 0.001)

	// Subsequent samples blend toward the new value.
	got := scheduling.UpdateEWMA(1000, 2000)
	require.InDelta(t, scheduling.EWMAAlpha*2000+(1-scheduling.EWMAAlpha)*1000, got, 0.001)
	require.Greater(t, got, 1000.0, "a higher sample pulls the EWMA up")
	require.Less(t, got, 2000.0, "EWMA lags the new sample")

	// Negative samples are floored to 0.
	require.InDelta(t, 0.0, scheduling.UpdateEWMA(0, -5), 0.001)

	// A sustained high value converges upward over a few runs.
	v := 200.0
	for i := 0; i < 20; i++ {
		v = scheduling.UpdateEWMA(v, 30000)
	}
	require.Greater(t, v, 20000.0, "sustained 30s samples converge toward 30s")
}

// TestZeroParamsAreInert verifies the no-data baseline: with zero Params and no
// cost history, effective == scheduled_at (pure FIFO), and the cost-aware
// timeout falls back to the flat default (15s) ceiling.
func TestZeroParamsAreInert(t *testing.T) {
	t.Parallel()

	var p scheduling.Params
	base := time.Now()

	require.Zero(t, p.TierCredit(99))
	require.Equal(t, base, p.EffectiveScheduledAt(base, 0, 99),
		"with zero params and no cost history, effective == scheduled_at (pure FIFO)")
	require.Equal(t, scheduling.DefaultExecutionTimeout, p.ExecutionTimeout(200))
}

// TestCostOffsetApplied verifies that with the dead-band disabled (zero
// threshold) the cost offset is applied directly: a job with accumulated cost
// sorts later than its real schedule by exactly cost × CostOffsetWeight — and
// nothing else. Delay cannot leak into the offset: EffectiveScheduledAt no
// longer takes a delay parameter at all (spec 2026-07-01-02 D1).
func TestCostOffsetApplied(t *testing.T) {
	t.Parallel()

	var p scheduling.Params // SlowThresholdMs 0 → no dead-band
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	require.Equal(t, base.Add(2000*time.Millisecond),
		p.EffectiveScheduledAt(base, 1000, 0),
		"effective = scheduled + cost_ewma×2 when no tier credit applies")
}

// laneParams returns the default lane hysteresis band (promote at 2000ms,
// demote below 1000ms) used across the ClassifyLane tests.
func laneParams() scheduling.Params {
	return scheduling.Params{
		LaneSlowThresholdMs: 2000,
		LaneFastThresholdMs: 1000,
	}
}

// TestClassifyLane covers the hysteresis classifier (spec 2026-07-01-03 D2):
// promote at cost >= slow threshold, demote below the fast threshold, hold in
// the dead-band, and stay inert when the classifier is disabled.
func TestClassifyLane(t *testing.T) {
	t.Parallel()

	p := laneParams()

	tests := []struct {
		name     string
		prevLane uint8
		cost     float64
		params   scheduling.Params
		want     uint8
	}{
		{"zero-cost fresh job stays fast", scheduling.LaneFast, 0, p, scheduling.LaneFast},
		{"cheap job stays fast", scheduling.LaneFast, 42, p, scheduling.LaneFast},
		{"fast job promotes at the slow threshold", scheduling.LaneFast, 2000, p, scheduling.LaneSlow},
		{"fast job promotes above the slow threshold", scheduling.LaneFast, 10000, p, scheduling.LaneSlow},
		{"fast job holds inside the band", scheduling.LaneFast, 1500, p, scheduling.LaneFast},
		{"fast job holds at the demote edge", scheduling.LaneFast, 1000, p, scheduling.LaneFast},
		{"slow job holds inside the band", scheduling.LaneSlow, 1500, p, scheduling.LaneSlow},
		{"slow job holds just below the promote edge", scheduling.LaneSlow, 1999, p, scheduling.LaneSlow},
		{"slow job demotes below the fast threshold", scheduling.LaneSlow, 999, p, scheduling.LaneFast},
		{"slow job stays slow at the promote edge", scheduling.LaneSlow, 2000, p, scheduling.LaneSlow},
		{"timeout-pinned cost stays slow", scheduling.LaneSlow, 30000, p, scheduling.LaneSlow},
		{
			"classifier off holds fast",
			scheduling.LaneFast, 30000,
			scheduling.Params{},
			scheduling.LaneFast,
		},
		{
			"classifier off holds slow",
			scheduling.LaneSlow, 0,
			scheduling.Params{},
			scheduling.LaneSlow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, scheduling.ClassifyLane(tt.prevLane, tt.cost, tt.params))
		})
	}
}

// TestClassifyLaneHysteresisNoFlap is the flap test from the spec: a job
// oscillating between 900ms and 2100ms cost flips lanes only when it crosses a
// band edge, and a job hovering at 1500ms (inside the band) never changes lane
// no matter how many runs it makes — the dead-band is what stops threshold
// hoverers from churning the partial claim indexes on every release.
func TestClassifyLaneHysteresisNoFlap(t *testing.T) {
	t.Parallel()

	p := laneParams()

	// 900 ↔ 2100 crosses both edges: each half-cycle flips exactly once.
	lane := scheduling.LaneFast
	for i := 0; i < 5; i++ {
		lane = scheduling.ClassifyLane(lane, 2100, p)
		require.Equal(t, scheduling.LaneSlow, lane, "2100ms crosses the promote edge")
		lane = scheduling.ClassifyLane(lane, 900, p)
		require.Equal(t, scheduling.LaneFast, lane, "900ms crosses the demote edge")
	}

	// Hovering at 1500ms (inside the band) holds whatever lane the job had —
	// from either side.
	fast := scheduling.LaneFast
	slow := scheduling.LaneSlow
	for i := 0; i < 10; i++ {
		fast = scheduling.ClassifyLane(fast, 1500, p)
		slow = scheduling.ClassifyLane(slow, 1500, p)
	}
	require.Equal(t, scheduling.LaneFast, fast, "a fast job hovering at 1500ms holds its lane")
	require.Equal(t, scheduling.LaneSlow, slow, "a slow job hovering at 1500ms holds its lane")

	// Oscillating strictly inside the band never flips at all.
	lane = scheduling.LaneSlow
	for i := 0; i < 10; i++ {
		lane = scheduling.ClassifyLane(lane, 1100, p)
		lane = scheduling.ClassifyLane(lane, 1900, p)
	}
	require.Equal(t, scheduling.LaneSlow, lane, "in-band oscillation must never flip the lane")
}

// TestSlowThreshold verifies the configurable dead-band: the weighted cost
// EWMA (cost×2) must reach SlowThresholdMs before effective_scheduled_at is
// pushed past the real scheduled_at.
func TestSlowThreshold(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	p := scheduling.Params{SlowThresholdMs: 2000}

	// Weighted cost×2 below the threshold → no change (pure FIFO).
	require.Equal(t, base, p.EffectiveScheduledAt(base, 900, 0),
		"below the threshold, effective stays at scheduled_at")

	// At/over the threshold → the full weighted offset applies.
	require.Equal(t, base.Add(3000*time.Millisecond),
		p.EffectiveScheduledAt(base, 1500, 0),
		"once cost×2 reaches the threshold, the full offset applies")

	// A non-positive threshold disables the dead-band (any positive offset applies).
	noBand := scheduling.Params{SlowThresholdMs: 0}
	require.Equal(t, base.Add(60*time.Millisecond), noBand.EffectiveScheduledAt(base, 30, 0),
		"threshold 0 applies even a tiny offset")
}
