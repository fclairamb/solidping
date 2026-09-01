package uptimebar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// aggRowWithDurations builds a rollup row carrying the duration columns the
// aggregation job persists, so the tests below can exercise the rollup half of
// the extrema/slow-peak accumulation.
func aggRowWithDurations(
	checkUID, periodType string, start time.Time, total, success int, durMin, durMax, durAvg float32,
) *models.Result {
	return &models.Result{
		CheckUID:         checkUID,
		PeriodType:       periodType,
		PeriodStart:      start,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		DurationMin:      &durMin,
		DurationMax:      &durMax,
		DurationAvg:      &durAvg,
	}
}

// TestBucketStats_DurationRangeIsAbsentWithoutMeasurements is the negative the
// report depends on: a bucket that measured nothing must NOT report "0 ms to
// 0 ms". The second half is the positive control proving the same accessor
// does report a range once something was measured.
func TestBucketStats_DurationRangeIsAbsentWithoutMeasurements(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var empty BucketStats

	_, _, ok := empty.DurationRange()
	r.False(ok, "an empty bucket must report no duration range")

	// A bucket with availability but no duration column (a rollup written
	// before durations existed) is still "no range", not zero.
	countsOnly := BucketStats{Up: 10, Total: 10}
	_, _, ok = countsOnly.DurationRange()
	r.False(ok, "counts without durations must not fabricate a range")

	// Positive control.
	var measured BucketStats

	measured.accumulateRaw(rawRow("c1", models.ResultStatusUp, time.Now(), 120))

	low, high, ok := measured.DurationRange()
	r.True(ok)
	r.InDelta(120, low, 0.001)
	r.InDelta(120, high, 0.001)
}

// TestBucketStats_RawExtremaAndSlowSamples pins the raw tier: extremes track
// the real min/max (a first sample must not let a later zero win by default),
// and slow samples are counted exactly, strictly above the threshold.
func TestBucketStats_RawExtremaAndSlowSamples(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now()

	var stats BucketStats

	for _, dur := range []float32{
		250,
		1000, // exactly at the threshold: NOT slow (strictly above).
		1500,
		4200,
		80,
	} {
		stats.accumulateRaw(rawRow("c1", models.ResultStatusUp, now, dur))
	}

	low, high, ok := stats.DurationRange()
	r.True(ok)
	r.InDelta(80, low, 0.001)
	r.InDelta(4200, high, 0.001)
	r.Equal(5, stats.DurExtremaCnt)
	r.Equal(2, stats.SlowSamples, "1000ms sits exactly on the threshold and must not count")
	r.Zero(stats.SlowPeaks, "raw rows are samples, never peaks")

	// The pinned fields must be untouched by any of this.
	pct, hasPct := stats.AvailabilityPct()
	r.True(hasPct)
	r.InDelta(100, pct, 0.001)

	avg, hasAvg := stats.AvgDuration()
	r.True(hasAvg)
	r.InDelta((250+1000+1500+4200+80)/5.0, avg, 0.001)
}

// TestBucketStats_FailedSamplesStillCountTowardsDurations documents the
// spec-mandated decision: a down/error probe that measured a duration DOES feed
// the response-time stats, on the raw tier exactly as the aggregation job does
// on the rollup tier. If this ever flips, the template copy must flip with it.
func TestBucketStats_FailedSamplesStillCountTowardsDurations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now()

	var stats BucketStats

	stats.accumulateRaw(rawRow("c1", models.ResultStatusUp, now, 100))
	stats.accumulateRaw(rawRow("c1", models.ResultStatusError, now, 5000))

	r.Equal(2, stats.DurCnt)

	_, high, ok := stats.DurationRange()
	r.True(ok)
	r.InDelta(5000, high, 0.001, "the failed sample's duration is included")
	r.Equal(1, stats.SlowSamples)

	// A lifecycle marker still contributes nothing at all — the exclusion rule
	// is unchanged.
	before := stats
	stats.accumulateRaw(rawRowAbandoned("c1", now))
	r.Equal(before, stats)
}

// TestBucketStats_RollupExtremaAndSlowPeaks pins the rollup tier: extremes come
// from duration_min/duration_max, and a rollup row contributes a PEAK, never a
// sample count it cannot know.
func TestBucketStats_RollupExtremaAndSlowPeaks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now()

	var stats BucketStats

	stats.accumulateAgg(aggRowWithDurations("c1", models.PeriodTypeHour, now, 60, 60, 90, 320, 140))
	stats.accumulateAgg(aggRowWithDurations("c1", models.PeriodTypeHour, now, 60, 59, 70, 2400, 210))

	low, high, ok := stats.DurationRange()
	r.True(ok)
	r.InDelta(70, low, 0.001)
	r.InDelta(2400, high, 0.001)

	r.Zero(stats.SlowSamples, "a rollup can never claim an exact slow-sample count")
	r.Equal(1, stats.SlowPeaks)

	// A rollup without duration columns contributes no extrema and no peak —
	// the negative control for the switch above.
	var bare BucketStats

	bare.accumulateAgg(hourRow("c1", 60, 60, now))
	_, _, ok = bare.DurationRange()
	r.False(ok)
	r.Zero(bare.SlowPeaks)
}

// TestBucketStats_AddMergesTheNewCounters is the guard the Add doc comment
// promises: a counter added to BucketStats must not be silently forgotten by
// the group-merge path.
func TestBucketStats_AddMergesTheNewCounters(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	left := BucketStats{
		Up: 10, Total: 10,
		DurCnt: 10, DurSum: 1000,
		DurMin: 50, DurMax: 300, DurExtremaCnt: 10,
		SlowSamples: 1, SlowPeaks: 2,
	}
	right := BucketStats{
		Up: 5, Total: 6,
		DurCnt: 6, DurSum: 900,
		DurMin: 20, DurMax: 6000, DurExtremaCnt: 6,
		SlowSamples: 3, SlowPeaks: 1,
	}

	merged := left
	merged.Add(right)

	r.Equal(15, merged.Up)
	r.Equal(16, merged.Total)
	r.Equal(16, merged.DurCnt)
	r.InDelta(1900, merged.DurSum, 0.001)
	r.InDelta(20, merged.DurMin, 0.001)
	r.InDelta(6000, merged.DurMax, 0.001)
	r.Equal(16, merged.DurExtremaCnt)
	r.Equal(4, merged.SlowSamples)
	r.Equal(3, merged.SlowPeaks)

	// Merging a bucket that measured nothing must not drag the minimum to zero.
	withEmpty := left
	withEmpty.Add(BucketStats{Up: 3, Total: 3})
	r.InDelta(50, withEmpty.DurMin, 0.001)
	r.Equal(10, withEmpty.DurExtremaCnt)

	// ...and merging INTO an unmeasured bucket seeds from the other side
	// rather than keeping a zero minimum.
	var seeded BucketStats

	seeded.Add(right)
	r.InDelta(20, seeded.DurMin, 0.001)
	r.InDelta(6000, seeded.DurMax, 0.001)
	r.Equal(6, seeded.DurExtremaCnt)
}

// TestWindowAvailability_CarriesLatencyAcrossTiers is the end-to-end read-path
// check: a window spanning a day rollup and fresh raw rows returns one merged
// BucketStats whose extrema span BOTH tiers, with samples and peaks counted in
// their own units.
func TestWindowAvailability_CarriesLatencyAcrossTiers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	start := now.Add(-72 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		// Older than the raw clamp: only reachable through the rollup tier.
		aggRowWithDurations("c1", models.PeriodTypeDay, now.Add(-48*time.Hour), 1440, 1439, 45, 3300, 190),
		// Inside the raw band.
		rawRow("c1", models.ResultStatusUp, now.Add(-30*time.Minute), 120),
		rawRow("c1", models.ResultStatusDown, now.Add(-20*time.Minute), 9000),
	}}

	byCheck, err := WindowAvailability(
		context.Background(), lister, "org", []string{"c1"}, start, now.Add(time.Minute), hints())
	r.NoError(err)

	stats := byCheck["c1"]

	low, high, ok := stats.DurationRange()
	r.True(ok)
	r.InDelta(45, low, 0.001, "the rollup tier's minimum wins")
	r.InDelta(9000, high, 0.001, "the raw tier's maximum wins")
	r.Equal(1, stats.SlowSamples)
	r.Equal(1, stats.SlowPeaks)
}
