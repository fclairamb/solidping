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

func TestNextAligned(t *testing.T) {
	t.Parallel()

	checkUID := "e2e55fa2-1719-49c3-b3f9-af0682db3b55"
	regions := []string{"us-1", "eu-2", "default"} // sorted: default(0), eu-2(1), us-1(2)

	regionOf := func(s string) *string { return &s }

	t.Run("ResultIsAlwaysStrictlyFuture", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute // 3 regions, split period

		for _, region := range []*string{regionOf("default"), regionOf("eu-2"), regionOf("us-1")} {
			next := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, region, regions)
			require.True(t, next.After(now), "next scheduled tick must be strictly after now")
			require.LessOrEqual(t, next.Sub(now), jobPeriod, "should never wait more than one full period")
		}
	})

	t.Run("RegionsAreLeveled", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute

		nextDefault := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions)
		nextEu2 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions)
		nextUs1 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("us-1"), regions)

		// Each region's phase (mod jobPeriod) must be exactly basePeriod apart
		// from its neighbor's — the D1 leveling guarantee. Compare phases (the
		// value mod jobPeriod), not raw ticks, since ticks can differ by
		// whole multiples of jobPeriod depending on where "now" falls.
		phaseOf := func(t time.Time) time.Duration {
			secs := t.Unix() % int64(jobPeriod/time.Second)
			return time.Duration(secs) * time.Second
		}

		pDefault := phaseOf(nextDefault)
		pEu2 := phaseOf(nextEu2)
		pUs1 := phaseOf(nextUs1)

		diff := func(a, b time.Duration) time.Duration {
			d := a - b
			if d < 0 {
				d += jobPeriod
			}
			return d
		}

		require.InDelta(t, float64(basePeriod), float64(diff(pEu2, pDefault)), float64(time.Second))
		require.InDelta(t, float64(2*basePeriod), float64(diff(pUs1, pDefault)), float64(time.Second))
	})

	t.Run("LateRunResumesAtNextPhaseTick_NoLockstep", func(t *testing.T) {
		t.Parallel()

		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute
		region := regionOf("default")

		// On-time: job would naturally fire at some phase-aligned instant.
		onTimeNow := time.Now()
		onTimeNext := scheduling.NextAligned(onTimeNow, basePeriod, jobPeriod, checkUID, region, regions)

		// Late: simulate the job being released much later than its original
		// tick (e.g. after a long restart) — it must resume at the NEXT
		// phase-aligned tick from the new "now", not now+period (which would
		// re-anchor and lose the phase per F2).
		lateNow := onTimeNext.Add(37 * time.Second) // arbitrary lateness, not a period multiple
		lateNext := scheduling.NextAligned(lateNow, basePeriod, jobPeriod, checkUID, region, regions)

		// The late tick's phase (mod jobPeriod) must match the on-time tick's
		// phase — proving the run resumed on-phase rather than drifting.
		phaseOf := func(t time.Time) int64 {
			return t.Unix() % int64(jobPeriod/time.Second)
		}
		require.Equal(t, phaseOf(onTimeNext), phaseOf(lateNext),
			"a late run must resume at the next phase-aligned tick, not drift to a new phase")
		require.True(t, lateNext.After(lateNow))
	})

	t.Run("RestartAfterLongGapReturnsToOriginalPhase", func(t *testing.T) {
		t.Parallel()

		basePeriod := time.Minute
		jobPeriod := time.Minute
		region := regionOf("default")

		now := time.Now()
		firstNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, region, regions)

		// Simulate a restart many periods later (e.g. server down for an hour).
		muchLater := firstNext.Add(53 * time.Minute).Add(17 * time.Second)
		afterRestart := scheduling.NextAligned(muchLater, basePeriod, jobPeriod, checkUID, region, regions)

		phaseOf := func(t time.Time) int64 { return t.Unix() % int64(jobPeriod/time.Second) }
		require.Equal(t, phaseOf(firstNext), phaseOf(afterRestart),
			"phase must be identical across an arbitrarily long gap (self-healing after restart)")
		require.True(t, afterRestart.After(muchLater))
	})

	t.Run("RegionAddedShiftsIndexButStaysPhaseAligned", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute

		// Before: 2 regions, us-1 is index 1.
		before := []string{"default", "us-1"}
		nextBefore := scheduling.NextAligned(now, basePeriod, 2*basePeriod, checkUID, regionOf("us-1"), before)

		// After: eu-2 added, us-1 is now index 2 (sorted: default, eu-2, us-1).
		after := []string{"default", "eu-2", "us-1"}
		nextAfter := scheduling.NextAligned(now, basePeriod, 3*basePeriod, checkUID, regionOf("us-1"), after)

		require.True(t, nextBefore.After(now))
		require.True(t, nextAfter.After(now))
		// Just assert both are valid future ticks with the new job period;
		// the exact instant differs because both the index and jobPeriod
		// changed, which is expected — reconcile explicitly re-levels on
		// region-set change per D2.
	})

	t.Run("NoRegionJobGetsJitterOnlyPhase", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute

		next := scheduling.NextAligned(now, basePeriod, basePeriod, checkUID, nil, nil)
		require.True(t, next.After(now))
		require.LessOrEqual(t, next.Sub(now), basePeriod)
	})

	t.Run("SingleRegionJobGetsJitterOnlyPhase", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute
		single := []string{"default"}

		next := scheduling.NextAligned(now, basePeriod, basePeriod, checkUID, regionOf("default"), single)
		require.True(t, next.After(now))
		require.LessOrEqual(t, next.Sub(now), basePeriod)
	})

	t.Run("RegionMissingFromCheckFallsBackToIndexZero", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute

		// A stale job whose region no longer appears in check.Regions (about
		// to be reconciled away) must not panic or produce a nonsensical
		// result — it degrades to i=0.
		staleNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("ap-3"), regions)
		defaultNext := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("default"), regions)

		phaseOf := func(t time.Time) int64 { return t.Unix() % int64(jobPeriod/time.Second) }
		require.Equal(t, phaseOf(defaultNext), phaseOf(staleNext),
			"region missing from check.Regions should fall back to the same phase as index 0")
	})

	t.Run("ZeroBasePeriodFallsBackToNowPlusJobPeriod", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		next := scheduling.NextAligned(now, 0, time.Minute, checkUID, regionOf("default"), regions)
		require.WithinDuration(t, now.Add(time.Minute), next, time.Second)
	})

	t.Run("ZeroJobPeriodFallsBackToNowPlusJobPeriod", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		next := scheduling.NextAligned(now, time.Minute, 0, checkUID, regionOf("default"), regions)
		require.Equal(t, now, next)
	})

	t.Run("DeterministicAcrossCalls", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute

		n1 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions)
		n2 := scheduling.NextAligned(now, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions)
		require.Equal(t, n1, n2, "same inputs must always produce the same output (cross-process agreement)")
	})
}
