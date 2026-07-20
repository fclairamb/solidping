package scheduling_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
)

func TestJitterFor(t *testing.T) {
	t.Parallel()

	t.Run("Deterministic", func(t *testing.T) {
		t.Parallel()

		j1 := scheduling.JitterFor("check-a", time.Minute)
		j2 := scheduling.JitterFor("check-a", time.Minute)
		require.Equal(t, j1, j2, "same UID + period must always produce the same jitter")
	})

	t.Run("Bounded", func(t *testing.T) {
		t.Parallel()

		for _, uid := range []string{"a", "b", "c", "e2e55fa2-1719-49c3-b3f9-af0682db3b55"} {
			j := scheduling.JitterFor(uid, time.Minute)
			require.GreaterOrEqual(t, j, time.Duration(0))
			require.Less(t, j, time.Minute)
		}
	})

	t.Run("SpreadsDifferentChecks", func(t *testing.T) {
		t.Parallel()

		// Not a strict guarantee for any two arbitrary strings, but across a
		// reasonable sample the hash must not collapse everything to the same
		// value — that would defeat the whole point of the jitter.
		seen := make(map[time.Duration]bool)
		for i := range 20 {
			uid := "check-" + string(rune('a'+i))
			seen[scheduling.JitterFor(uid, time.Minute)] = true
		}
		require.Greater(t, len(seen), 1, "20 different UIDs should not all collapse to the same jitter")
	})

	t.Run("ZeroOrNegativeBasePeriod", func(t *testing.T) {
		t.Parallel()

		require.Zero(t, scheduling.JitterFor("check-a", 0))
		require.Zero(t, scheduling.JitterFor("check-a", -time.Minute))
	})
}

func TestRegionIndex(t *testing.T) {
	t.Parallel()

	regionOf := func(s string) *string { return &s }

	tests := []struct {
		name    string
		region  *string
		regions []string
		want    int
	}{
		{
			name:    "FirstInSortedOrder",
			region:  regionOf("default"),
			regions: []string{"us-1", "eu-2", "default"},
			want:    0, // sorted: default, eu-2, us-1
		},
		{
			name:    "SecondInSortedOrder",
			region:  regionOf("eu-2"),
			regions: []string{"us-1", "eu-2", "default"},
			want:    1,
		},
		{
			name:    "ThirdInSortedOrder",
			region:  regionOf("us-1"),
			regions: []string{"us-1", "eu-2", "default"},
			want:    2,
		},
		{
			name:    "NilRegion",
			region:  nil,
			regions: []string{"us-1", "eu-2", "default"},
			want:    0,
		},
		{
			name:    "EmptyRegions",
			region:  regionOf("us-1"),
			regions: nil,
			want:    0,
		},
		{
			name:    "RegionMissingFromCheck",
			region:  regionOf("ap-3"),
			regions: []string{"us-1", "eu-2", "default"},
			want:    0, // stale job about to be reconciled away
		},
		{
			name:    "SingleRegion",
			region:  regionOf("default"),
			regions: []string{"default"},
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := scheduling.RegionIndex(tc.region, tc.regions)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRegionSpread(t *testing.T) {
	t.Parallel()

	durPtr := func(d time.Duration) *time.Duration { return &d }

	tests := []struct {
		name       string
		basePeriod time.Duration
		n          int
		override   *time.Duration
		want       time.Duration
	}{
		{name: "DefaultIsPeriodOverN", basePeriod: time.Minute, n: 3, override: nil, want: 20 * time.Second},
		{name: "DefaultTwoRegions", basePeriod: time.Minute, n: 2, override: nil, want: 30 * time.Second},
		{name: "OverrideUsedVerbatim", basePeriod: time.Minute, n: 3, override: durPtr(time.Second), want: time.Second},
		{name: "OverrideZeroFiresTogether", basePeriod: time.Minute, n: 3, override: durPtr(0), want: 0},
		{name: "SingleRegionNoSpread", basePeriod: time.Minute, n: 1, override: nil, want: 0},
		{name: "SingleRegionOverrideStillZero", basePeriod: time.Minute, n: 1, override: durPtr(time.Second), want: 0},
		{name: "ZeroRegionsNoSpread", basePeriod: time.Minute, n: 0, override: nil, want: 0},
		{name: "ZeroBasePeriodNoSpread", basePeriod: 0, n: 3, override: nil, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := scheduling.RegionSpread(tc.basePeriod, tc.n, tc.override)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestNextAligned(t *testing.T) {
	t.Parallel()

	checkUID := "e2e55fa2-1719-49c3-b3f9-af0682db3b55"
	regions := []string{"us-1", "eu-2", "default"} // sorted: default(0), eu-2(1), us-1(2)

	regionOf := func(s string) *string { return &s }

	// Since spec 2026-07-20-05 every region runs at the FULL base period
	// (jobPeriod == basePeriod), staggered by the inter-region spread.
	basePeriod := time.Minute
	jobPeriod := basePeriod
	spread := scheduling.RegionSpread(basePeriod, len(regions), nil) // 20s for 3 regions

	// phaseOf reduces a tick to its second-of-period, the value the phase
	// formula pins (mod jobPeriod).
	phaseOf := func(tt time.Time) int64 { return tt.Unix() % int64(jobPeriod/time.Second) }
	diffSecs := func(a, b int64) int64 {
		d := a - b
		if d < 0 {
			d += int64(jobPeriod / time.Second)
		}

		return d
	}

	t.Run("ResultIsAlwaysStrictlyFuture", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		for _, region := range []*string{regionOf("default"), regionOf("eu-2"), regionOf("us-1")} {
			next := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, region, regions, spread)
			require.True(t, next.After(now), "next scheduled tick must be strictly after now")
			require.LessOrEqual(t, next.Sub(now), jobPeriod, "should never wait more than one full period")
		}
	})

	t.Run("RegionsAreLeveledBySpread", func(t *testing.T) {
		t.Parallel()

		now := time.Now()

		nextDefault := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions, spread)
		nextEu2 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions, spread)
		nextUs1 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("us-1"), regions, spread)

		// Consecutive regions' phases must be exactly one spread (20s) apart —
		// the new-formula leveling. Each region still fires every full period.
		require.Equal(t, int64(spread/time.Second),
			diffSecs(phaseOf(nextEu2), phaseOf(nextDefault)), "eu-2 is one spread after default")
		require.Equal(t, int64(2*spread/time.Second),
			diffSecs(phaseOf(nextUs1), phaseOf(nextDefault)), "us-1 is two spreads after default")
	})

	t.Run("ZeroSpreadFiresAllRegionsTogether", func(t *testing.T) {
		t.Parallel()

		now := time.Now()

		pd := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions, 0))
		pe := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions, 0))
		pu := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("us-1"), regions, 0))

		require.Equal(t, pd, pe, "spread=0 → every region shares the jitter-only phase")
		require.Equal(t, pd, pu, "spread=0 → every region shares the jitter-only phase")
	})

	t.Run("OverrideSpreadStaggersByOverride", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		override := 5 * time.Second

		pd := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions, override))
		pe := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions, override))
		pu := phaseOf(scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("us-1"), regions, override))

		require.EqualValues(t, 5, diffSecs(pe, pd), "override spread applied between region 0 and 1")
		require.EqualValues(t, 10, diffSecs(pu, pd), "override spread applied between region 0 and 2")
	})

	t.Run("LateRunResumesAtNextPhaseTick_NoLockstep", func(t *testing.T) {
		t.Parallel()

		region := regionOf("default")

		onTimeNow := time.Now()
		onTimeNext := scheduling.NextAligned(onTimeNow, basePeriod, jobPeriod, checkUID, region, regions, spread)

		// Late: released well after its original tick — it must resume at the
		// NEXT phase-aligned tick, not now+period (which would lose the phase).
		lateNow := onTimeNext.Add(37 * time.Second)
		lateNext := scheduling.NextAligned(lateNow, basePeriod, jobPeriod, checkUID, region, regions, spread)

		require.Equal(t, phaseOf(onTimeNext), phaseOf(lateNext),
			"a late run must resume at the next phase-aligned tick, not drift to a new phase")
		require.True(t, lateNext.After(lateNow))
	})

	t.Run("RestartAfterLongGapReturnsToOriginalPhase", func(t *testing.T) {
		t.Parallel()

		region := regionOf("default")

		now := time.Now()
		firstNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, region, regions, spread)

		muchLater := firstNext.Add(53 * time.Minute).Add(17 * time.Second)
		afterRestart := scheduling.NextAligned(muchLater, basePeriod, jobPeriod, checkUID, region, regions, spread)

		require.Equal(t, phaseOf(firstNext), phaseOf(afterRestart),
			"phase must be identical across an arbitrarily long gap (self-healing after restart)")
		require.True(t, afterRestart.After(muchLater))
	})

	t.Run("NoRegionJobGetsJitterOnlyPhase", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		next := scheduling.NextAligned(now, basePeriod, basePeriod, checkUID, nil, nil, 0)
		require.True(t, next.After(now))
		require.LessOrEqual(t, next.Sub(now), basePeriod)
	})

	t.Run("SingleRegionJobGetsJitterOnlyPhase", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		single := []string{"default"}
		singleSpread := scheduling.RegionSpread(basePeriod, len(single), nil) // 0

		next := scheduling.NextAligned(now, basePeriod, basePeriod, checkUID, regionOf("default"), single, singleSpread)
		require.True(t, next.After(now))
		require.LessOrEqual(t, next.Sub(now), basePeriod)
	})

	t.Run("RegionMissingFromCheckFallsBackToIndexZero", func(t *testing.T) {
		t.Parallel()

		now := time.Now()

		// A stale job whose region no longer appears in check.Regions degrades
		// to i=0 (same phase as index 0), never panics.
		staleNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("ap-3"), regions, spread)
		defaultNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions, spread)

		require.Equal(t, phaseOf(defaultNext), phaseOf(staleNext),
			"region missing from check.Regions should fall back to the same phase as index 0")
	})

	t.Run("ZeroBasePeriodFallsBackToNowPlusJobPeriod", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		next := scheduling.NextAligned(now, 0, time.Minute, checkUID, regionOf("default"), regions, spread)
		require.WithinDuration(t, now.Add(time.Minute), next, time.Second)
	})

	t.Run("ZeroJobPeriodFallsBackToNowPlusJobPeriod", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		next := scheduling.NextAligned(now, time.Minute, 0, checkUID, regionOf("default"), regions, spread)
		require.Equal(t, now, next)
	})

	t.Run("DeterministicAcrossCalls", func(t *testing.T) {
		t.Parallel()

		now := time.Now()

		n1 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions, spread)
		n2 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions, spread)
		require.Equal(t, n1, n2, "same inputs must always produce the same output (cross-process agreement)")
	})
}
