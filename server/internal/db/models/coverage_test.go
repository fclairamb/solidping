package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func TestExpectedProbesAndCoverage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The capacity figure keeps its region multiplier.
	r.InDelta(1440, models.ExpectedProbes(24*time.Hour, time.Minute, 0), 0.001)
	r.InDelta(4320, models.ExpectedProbes(24*time.Hour, time.Minute, 3), 0.001)
	r.Zero(models.ExpectedProbes(0, time.Minute, 1))
	r.Zero(models.ExpectedProbes(time.Hour, 0, 1))

	// The coverage denominator does not.
	r.InDelta(1440, models.ExpectedPeriods(24*time.Hour, time.Minute), 0.001)
	r.Zero(models.ExpectedPeriods(0, time.Minute))
	r.Zero(models.ExpectedPeriods(time.Hour, 0))

	// An 8h gap in 24h of one-minute probes: 960 of 1440 → two thirds.
	coverage, ok := models.Coverage(960, models.ExpectedPeriods(24*time.Hour, time.Minute))
	r.True(ok)
	r.InDelta(2.0/3.0, coverage, 0.001)

	coverage, ok = models.Coverage(2000, 1440)
	r.True(ok)
	r.InDelta(1.0, coverage, 0.0001, "more results than expected is full coverage, never more")

	_, ok = models.Coverage(10, 0)
	r.False(ok)

	created := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	check := &models.Check{
		Period: timeutils.Duration(time.Minute), CreatedAt: created, Regions: []string{"eu-west", "us-east"},
	}
	dayStart := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)

	r.InDelta(360, check.ExpectedPeriodsBetween(dayStart, dayStart.Add(24*time.Hour), now), 0.001,
		"clamped to [createdAt, now), and two regions still expect one result per period")
}

// Spec 2026-11-08-05: a bucket is measured in proportion to its results,
// capped at one per period whatever the number of regions that produced them.
func TestCoverageTally(t *testing.T) {
	t.Parallel()

	t.Run("two regions reporting count once", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var tally models.CoverageTally

		tally.AddExpected(24) // 2 hours of 5-minute periods
		tally.AddBucket(24, 12)
		tally.AddBucket(12, 12)

		coverage, ok := tally.Coverage()
		r.True(ok)
		r.InDelta(1.0, coverage, 0.0001)
	})

	t.Run("an absent bucket is unmeasured", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var tally models.CoverageTally

		tally.AddExpected(24)
		tally.AddBucket(24, 12)

		coverage, ok := tally.Coverage()
		r.True(ok)
		r.InDelta(0.5, coverage, 0.0001, "the surplus of one bucket never fills another")
	})

	t.Run("a period longer than the bucket", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		// A 6-hour check read in 1-hour buckets: one result in one bucket
		// covers its whole period.
		var tally models.CoverageTally

		tally.AddExpected(1)

		for range 6 {
			tally.AddBucket(0, 1.0/6)
		}

		tally.AddBucket(1, 1.0/6)

		coverage, ok := tally.Coverage()
		r.True(ok)
		r.InDelta(1.0, coverage, 0.0001)
	})

	t.Run("merge and nothing expected", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var tally models.CoverageTally

		_, ok := tally.Coverage()
		r.False(ok)

		tally.Add(models.CoverageTally{Covered: 3, Expected: 4})
		tally.Add(models.CoverageTally{Covered: 1, Expected: 4})

		coverage, ok := tally.Coverage()
		r.True(ok)
		r.InDelta(0.5, coverage, 0.0001)
	})
}

func TestStaleThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(5*time.Minute, models.StaleThreshold(10*time.Second))
	r.Equal(5*time.Minute, models.StaleThreshold(time.Minute))
	r.Equal(30*time.Minute, models.StaleThreshold(10*time.Minute))
}
