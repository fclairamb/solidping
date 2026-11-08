package uptimebar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// coverageFixture is a 5-minute check created long ago, read over the last 48
// hours: 24 hourly rollups, then 24 hours of raw results. Every period of the
// first 12 hours was probed from one region, every later period from two
// (a placement change mid-window). skipRaw drops the raw periods it matches.
func coverageFixture(skipRaw func(at time.Time) bool) (*models.Check, []*models.Result, time.Time, time.Time) {
	now := time.Now().UTC()
	start := now.Truncate(time.Hour).Add(-48 * time.Hour)

	check := &models.Check{
		UID:       "c1",
		Period:    timeutils.Duration(5 * time.Minute),
		Regions:   []string{"eu-west", "us-east"},
		CreatedAt: start.Add(-90 * 24 * time.Hour),
	}

	var rows []*models.Result

	for h := range 24 {
		perHour := 12
		if h >= 12 {
			perHour = 24
		}

		rows = append(rows, hourRow("c1", perHour, perHour, start.Add(time.Duration(h)*time.Hour)))
	}

	for at := start.Add(24 * time.Hour); at.Before(now); at = at.Add(5 * time.Minute) {
		if skipRaw != nil && skipRaw(at) {
			continue
		}

		for _, region := range check.Regions {
			row := rawRow("c1", models.ResultStatusUp, at, 10)
			row.Region = &region
			rows = append(rows, row)
		}
	}

	return check, rows, start, now
}

// A check whose region count changed mid-window reads fully measured when
// every period had a result from some region (spec 2026-11-08-05). The old
// formula expected two results per period over the whole window, so the
// one-region half read as half unmeasured.
func TestWindowAvailabilityWithCoverage_RegionChangeIsNotUnmeasured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	check, rows, start, now := coverageFixture(nil)

	out, err := WindowAvailabilityWithCoverage(
		context.Background(), &fakeAggregator{results: rows}, "org", []*models.Check{check}, start, now, now, hints())
	r.NoError(err)

	folded := out["c1"]
	coverage, ok := folded.Coverage.Coverage()
	r.True(ok)
	r.InDelta(1.0, coverage, 0.0001)

	// Stats is the same whole-window fold WindowAvailability returns.
	plain, err := WindowAvailability(
		context.Background(), &fakeAggregator{results: rows}, "org", []string{"c1"}, start, now, hints())
	r.NoError(err)
	r.Equal(plain["c1"].Total, folded.Stats.Total)
	r.Equal(plain["c1"].Up, folded.Stats.Up)
}

// A real gap (no result from any region for 2 hours) reads 2 hours unmeasured.
func TestWindowAvailabilityWithCoverage_RealGapIsUnmeasured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gapStart time.Time

	check, rows, start, now := coverageFixture(func(at time.Time) bool {
		if gapStart.IsZero() {
			gapStart = at.Add(6 * time.Hour)
		}

		return !at.Before(gapStart) && at.Before(gapStart.Add(2*time.Hour))
	})

	out, err := WindowAvailabilityWithCoverage(
		context.Background(), &fakeAggregator{results: rows}, "org", []*models.Check{check}, start, now, now, hints())
	r.NoError(err)

	coverage, ok := out["c1"].Coverage.Coverage()
	r.True(ok)

	unmeasured := (1 - coverage) * now.Sub(start).Seconds()
	r.InDelta((2 * time.Hour).Seconds(), unmeasured, (5 * time.Minute).Seconds())
}

// Day and month rollups are measured by their own length: a full day and a
// half day read 1.5 days measured, a full month reads a month measured.
func TestWindowAvailabilityWithCoverage_DayAndMonthTiers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -4, 0)
	monthEnd := monthStart.AddDate(0, 1, 0)
	monthDays := int(monthEnd.Sub(monthStart).Hours() / 24)

	check := &models.Check{
		UID:       "c1",
		Period:    timeutils.Duration(5 * time.Minute),
		Regions:   []string{"eu-west", "us-east", "ap-south"},
		CreatedAt: monthStart.AddDate(-1, 0, 0),
	}

	rows := []*models.Result{
		// Three regions' worth of a month: capped at one result per period.
		monthRow("c1", monthDays*288*3, monthDays*288*3, monthStart),
		dayRow("c1", 288, 288, monthEnd),
		dayRow("c1", 144, 144, monthEnd.Add(24*time.Hour)),
	}

	end := monthEnd.Add(10 * 24 * time.Hour)

	out, err := WindowAvailabilityWithCoverage(
		context.Background(), &fakeAggregator{results: rows}, "org", []*models.Check{check}, monthStart, end, now, hints())
	r.NoError(err)

	tally := out["c1"].Coverage
	r.InDelta(float64(monthDays*288+288+144), tally.Covered, 0.001)
	r.InDelta(float64((monthDays+10)*288), tally.Expected, 0.001)
}

// The guards: no check, an empty window, and a check created after the window.
func TestWindowAvailabilityWithCoverage_Guards(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now().UTC()
	lister := &fakeAggregator{}

	out, err := WindowAvailabilityWithCoverage(context.Background(), lister, "org", nil, now.Add(-time.Hour), now, now,
		hints())
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter())

	young := &models.Check{UID: "c1", Period: timeutils.Duration(time.Minute), CreatedAt: now}

	out, err = WindowAvailabilityWithCoverage(context.Background(), lister, "org", []*models.Check{young, young},
		now.Add(-time.Hour), now, now, hints())
	r.NoError(err)

	_, ok := out["c1"].Coverage.Coverage()
	r.False(ok, "nothing was expected from a check that did not exist yet")
}
