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
// paid jobs must sort ahead of slow, delayed, and free jobs, while the absolute
// anchor guarantees no permanent starvation.
func TestEffectiveOrdering(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// All jobs are due at the same real scheduled_at; vary cost / delay / tier.
	fastFree := p.EffectiveScheduledAt(base, 200, 0, 0)
	slowFree := p.EffectiveScheduledAt(base, 30000, 0, 0)
	fastPaid := p.EffectiveScheduledAt(base, 200, 0, 2)
	slowPaid := p.EffectiveScheduledAt(base, 30000, 0, 2)
	delayedFree := p.EffectiveScheduledAt(base, 200, 30000, 0)

	// A slow check is de-prioritized relative to a fast one (same tier) — it
	// sorts LATER (larger effective deadline).
	require.True(t, fastFree.Before(slowFree),
		"fast free check must sort before slow free check")

	// A chronically-delayed check is likewise de-prioritized.
	require.True(t, fastFree.Before(delayedFree),
		"a delayed check must sort after an on-time check")

	// Paid is impacted less than free — at equal cost, the paid job sorts
	// earlier (smaller effective deadline).
	require.True(t, fastPaid.Before(fastFree),
		"fast paid check must sort before fast free check")
	require.True(t, slowPaid.Before(slowFree),
		"slow paid check must sort before slow free check at equal cost")
}

// TestAntiStarvation: a permanently-skipped check has its real scheduled_at
// recede into the past; because the cost+delay offset is bounded, its effective
// deadline eventually sorts ahead of fresh work. No cap knob is needed — the
// absolute scheduled_at anchor is the guarantee.
func TestAntiStarvation(t *testing.T) {
	t.Parallel()

	p := defaultParams()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Fresh fast job due right now.
	freshFast := p.EffectiveScheduledAt(now, 200, 0, 0)

	// Expensive, chronically-delayed job that was due well in the past (skipped
	// for a long time) — far enough that its bounded cost+delay offset cannot
	// keep it behind the fresh job.
	starved := p.EffectiveScheduledAt(now.Add(-2*time.Minute), 30000, 30000, 0)

	require.True(t, starved.Before(freshFast),
		"a long-overdue check must eventually outrank fresh work (anchored to scheduled_at)")
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
// cost/delay history, effective == scheduled_at (pure FIFO), and the cost-aware
// timeout falls back to the flat 30s ceiling.
func TestZeroParamsAreInert(t *testing.T) {
	t.Parallel()

	var p scheduling.Params
	base := time.Now()

	require.Zero(t, p.TierCredit(99))
	require.Equal(t, base, p.EffectiveScheduledAt(base, 0, 0, 99),
		"with zero params and no cost/delay history, effective == scheduled_at (pure FIFO)")
	require.Equal(t, scheduling.DefaultExecutionTimeout, p.ExecutionTimeout(200))
}

// TestCostDelayOffsetApplied verifies that with the dead-band disabled (zero
// threshold) the cost/delay offset is applied directly: a job with accumulated
// cost/delay sorts later than its real schedule.
func TestCostDelayOffsetApplied(t *testing.T) {
	t.Parallel()

	var p scheduling.Params // SlowThresholdMs 0 → no dead-band
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	require.Equal(t, base.Add(1500*time.Millisecond),
		p.EffectiveScheduledAt(base, 1000, 500, 0),
		"effective = scheduled + cost_ewma + delay_ewma when no tier credit applies")
}

// TestSlowThreshold verifies the configurable dead-band: the combined cost+delay
// EWMA must reach SlowThresholdMs before effective_scheduled_at is pushed past
// the real scheduled_at.
func TestSlowThreshold(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	p := scheduling.Params{SlowThresholdMs: 2000}

	// Combined cost+delay below the threshold → no change (pure FIFO).
	require.Equal(t, base, p.EffectiveScheduledAt(base, 800, 900, 0),
		"below the threshold, effective stays at scheduled_at")

	// At/over the threshold → the full cost+delay offset applies.
	require.Equal(t, base.Add(2100*time.Millisecond),
		p.EffectiveScheduledAt(base, 1500, 600, 0),
		"once cost+delay reaches the threshold, the full offset applies")

	// A non-positive threshold disables the dead-band (any positive offset applies).
	noBand := scheduling.Params{SlowThresholdMs: 0}
	require.Equal(t, base.Add(50*time.Millisecond), noBand.EffectiveScheduledAt(base, 30, 20, 0),
		"threshold 0 applies even a tiny offset")
}
