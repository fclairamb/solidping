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
		SlowCostThresholdMs: 1000, // > 1s of cost EWMA = slow
		PenaltyCap:          20 * time.Second,
		TierCreditPerWeight: 5 * time.Second,
		TierCreditMax:       15 * time.Second,
		CostTimeoutFactor:   3,
		CostTimeoutFloor:    2 * time.Second,
	}
}

func TestIsSlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		threshold  float64
		costEWMAMs float64
		want       bool
	}{
		{"disabled threshold never slow", 0, 999999, false},
		{"below threshold is fast", 1000, 500, false},
		{"at threshold is fast (strict >)", 1000, 1000, false},
		{"above threshold is slow", 1000, 1001, true},
		{"well above threshold is slow", 1000, 30000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := scheduling.Params{SlowCostThresholdMs: tt.threshold}
			require.Equal(t, tt.want, p.IsSlow(tt.costEWMAMs))
		})
	}
}

func TestCostPenalty(t *testing.T) {
	t.Parallel()

	p := defaultParams()

	tests := []struct {
		name       string
		costEWMAMs float64
		wantZero   bool
		atCap      bool
	}{
		{"fast job no penalty", 200, true, false},
		{"slow job gets a penalty", 1500, false, false},
		{"very slow job is capped", 1_000_000, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := p.CostPenalty(tt.costEWMAMs)
			if tt.wantZero {
				require.Zero(t, got)

				return
			}
			require.Positive(t, got)
			require.LessOrEqual(t, got, p.PenaltyCap, "penalty must never exceed the cap (anti-starvation)")
			if tt.atCap {
				require.Equal(t, p.PenaltyCap, got, "an extreme cost must clamp exactly to the cap")
			}
		})
	}

	// Disabled penalty (cap 0) is always zero, even for slow jobs.
	t.Run("disabled penalty cap", func(t *testing.T) {
		t.Parallel()
		disabled := scheduling.Params{SlowCostThresholdMs: 1000, PenaltyCap: 0}
		require.Zero(t, disabled.CostPenalty(50000))
	})
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
// guarantees no permanent starvation.
func TestEffectiveOrdering(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// All four jobs are due at the same real scheduled_at.
	fastFree := p.EffectiveScheduledAt(base, 200, 0)
	slowFree := p.EffectiveScheduledAt(base, 30000, 0)
	fastPaid := p.EffectiveScheduledAt(base, 200, 2)
	slowPaid := p.EffectiveScheduledAt(base, 30000, 2)

	// Requirement 1: a slow check is de-prioritized relative to a fast one
	// (same tier) — it sorts LATER (larger effective deadline).
	require.True(t, fastFree.Before(slowFree),
		"fast free check must sort before slow free check")

	// Requirement 2: paid is impacted less than free — at equal cost, the paid
	// job sorts earlier (smaller effective deadline).
	require.True(t, fastPaid.Before(fastFree),
		"fast paid check must sort before fast free check")
	require.True(t, slowPaid.Before(slowFree),
		"slow paid check must sort before slow free check at equal cost")

	// A slow paid check still beats a slow free check (the credit protects it).
	require.True(t, slowPaid.Before(slowFree))
}

// TestAntiStarvation: a permanently-slow free check that keeps getting skipped
// has its real scheduled_at recede into the past; because the penalty is capped,
// its effective deadline eventually sorts ahead of a fresh fast job.
func TestAntiStarvation(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Fresh fast job due right now.
	freshFast := p.EffectiveScheduledAt(now, 200, 0)

	// Slow job that was due well in the past (skipped for a long time) — far
	// enough that even the maxed-out penalty cannot keep it behind the fresh job.
	overdueBy := p.PenaltyCap + time.Minute
	starvedSlow := p.EffectiveScheduledAt(now.Add(-overdueBy), 1_000_000, 0)

	require.True(t, starvedSlow.Before(freshFast),
		"a long-overdue slow check must eventually outrank fresh work (capped penalty)")
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
			name:       "disabled falls back to flat 30s",
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
			name:       "no cost signal yet uses the floor, not zero",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 0,
			want:       2 * time.Second,
		},
		{
			name:       "chronic offender is clamped to the 30s ceiling",
			params:     scheduling.Params{CostTimeoutFactor: 3, CostTimeoutFloor: 2 * time.Second},
			costEWMAMs: 60000, // 3 × 60s = 180s, clamped
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

func TestUpdateCostEWMA(t *testing.T) {
	t.Parallel()

	// First sample seeds the average directly.
	require.InDelta(t, 500.0, scheduling.UpdateCostEWMA(0, 500), 0.001)

	// Subsequent samples blend toward the new value.
	got := scheduling.UpdateCostEWMA(1000, 2000)
	require.InDelta(t, scheduling.EWMAAlpha*2000+(1-scheduling.EWMAAlpha)*1000, got, 0.001)
	require.Greater(t, got, 1000.0, "a higher sample pulls the EWMA up")
	require.Less(t, got, 2000.0, "EWMA lags the new sample")

	// Negative samples are floored to 0.
	require.InDelta(t, 0.0, scheduling.UpdateCostEWMA(0, -5), 0.001)

	// A sustained high cost converges upward over a few runs.
	cost := 200.0
	for i := 0; i < 20; i++ {
		cost = scheduling.UpdateCostEWMA(cost, 30000)
	}
	require.Greater(t, cost, 1000.0, "sustained 30s cost must eventually classify as slow")
}

// TestZeroParamsAreInert verifies the off-by-default contract: the zero-value
// Params reproduces today's behaviour exactly.
func TestZeroParamsAreInert(t *testing.T) {
	t.Parallel()

	var p scheduling.Params
	base := time.Now()

	require.False(t, p.IsSlow(1_000_000))
	require.Zero(t, p.CostPenalty(1_000_000))
	require.Zero(t, p.TierCredit(99))
	require.Equal(t, base, p.EffectiveScheduledAt(base, 1_000_000, 99),
		"with zero params, effective == scheduled_at (pure FIFO)")
	require.Equal(t, scheduling.DefaultExecutionTimeout, p.ExecutionTimeout(200))
}
