package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// TestSeamBinWidth is the spec's table: the bin is the smallest step of the
// ladder that is at least windowSpan/responseTimeLimit, clamped to [1 min, 1 h].
//
// The two clamps are the assertions that matter. The floor keeps a very short
// window from asking for sub-minute bins, which would hold one probe each and buy
// nothing over the row fetch this replaces. The ceiling keeps the seam from ever
// being COARSER than the hour rollups it sits next to on the same chart — a
// 90-day page's ideal bin is 21.6 h, and honouring that would make the newest
// part of the series less detailed than its middle.
func TestSeamBinWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		span   time.Duration
		want   time.Duration
		reason string
	}{
		{"24h page", 24 * time.Hour, 15 * time.Minute,
			"ideal 14.4 min rounds up one step; ~96 seam points across the day"},
		{"7d page", 7 * 24 * time.Hour, time.Hour, "ideal 100.8 min is past the ceiling"},
		{"30d page", 30 * 24 * time.Hour, time.Hour, "clamped to the hour ceiling"},
		{"90d page", 90 * 24 * time.Hour, time.Hour,
			"never coarser than the hour rollups beside it, whatever the ideal says"},
		{"1h window", time.Hour, time.Minute, "ideal 36 s is under the floor"},
		{"exactly at a step", 100 * 5 * time.Minute, 5 * time.Minute,
			"the ideal IS a step, so it is taken rather than rounded past"},
		{"just over a step", 100*5*time.Minute + time.Second, 10 * time.Minute,
			"a hair over 5 min takes the next step up, never a finer one"},
		{"zero span", 0, time.Minute, "degenerate input still yields the floor, never 0"},
		{"negative span", -time.Hour, time.Minute, "and so does a reversed window"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, seamBinWidth(tc.span), tc.reason)
		})
	}
}

// TestSeamBinWidthNeverExceedsTheBudget pins the property the table above only
// samples: for every window the chart can render, the seam alone cannot produce
// more points than the chart holds — unless the hour ceiling is what bound the
// width, in which case the seam is only the raw-retention slice of the window and
// the trim's tier split handles the rest.
func TestSeamBinWidthNeverExceedsTheBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for hours := 1; hours <= 24*200; hours++ {
		span := time.Duration(hours) * time.Hour
		width := seamBinWidth(span)

		r.GreaterOrEqual(width, time.Minute, "span %s", span)
		r.LessOrEqual(width, time.Hour, "span %s", span)

		if width == time.Hour {
			continue
		}

		r.LessOrEqual(int(span/width), responseTimeLimit,
			"span %s at %s bins would produce more points than the chart holds", span, width)
	}
}

// seamTestResult builds a seam row the way seamResult does, for the trim tests.
func seamTestResult(periodStart time.Time, total, up int, p95 float32, status int) *models.Result {
	return seamResult(&models.ResponseTimeBin{
		CheckUID:     "check-1",
		BinStart:     periodStart,
		Total:        total,
		Up:           up,
		DurationP95:  &p95,
		StatusCounts: map[int]int{status: total},
	})
}

// TestSeamResultFoldsLikeARollup is the contract that makes the seam usable
// downstream without touching the point builder: uptimebar.StatsForResult must
// fold a seam row through accumulateAgg, so its bin counts survive intact.
//
// The negative control is the whole point. If seam were folded as a RAW row
// instead — which is what reusing models.PeriodTypeRaw for these rows would have
// done — the fold would count it as ONE probe and the point would report 1/1
// instead of 60/59, silently turning a 98.3 % bin into a green 100 % one.
func TestSeamResultFoldsLikeARollup(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	row := seamTestResult(time.Now().UTC().Truncate(time.Hour), 60, 59, 210, int(models.ResultStatusUp))

	stats := uptimebar.StatsForResult(row)
	r.Equal(60, stats.Total, "a seam point's bin count is its denominator")
	r.Equal(59, stats.Up)

	pct, ok := stats.AvailabilityPct()
	r.True(ok)
	r.InDelta(98.33, pct, 0.01)

	// The same numbers read as one probe if the row claimed to be raw.
	asRaw := *row
	asRaw.PeriodType = models.PeriodTypeRaw
	rawStats := uptimebar.StatsForResult(&asRaw)
	r.Equal(1, rawStats.Total, "the control: a raw fold throws the bin's counts away")
}

// TestSeamResultStatusUsesTheAggregationRule pins that a seam bin classifies with
// the aggregation job's own promotion rules (uptimebar.DominantStatus), not with
// "the worst status seen". A bin holding 58 ups and one warning is DEGRADED — the
// same answer the hour rollup that eventually replaces that bin will carry — and
// a bin with no countable probe carries no status at all rather than a
// manufactured one.
func TestSeamResultStatusUsesTheAggregationRule(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	promoted := seamResult(&models.ResponseTimeBin{
		CheckUID: "check-1",
		BinStart: time.Now().UTC(),
		Total:    59,
		Up:       59,
		StatusCounts: map[int]int{
			int(models.ResultStatusUp):      58,
			int(models.ResultStatusWarning): 1,
		},
	})
	r.NotNil(promoted.Status)
	r.Equal(int(models.ResultStatusDegraded), *promoted.Status,
		"a warning in the bin promotes it to degraded, exactly as the rollup does")

	empty := seamResult(&models.ResponseTimeBin{
		CheckUID:     "check-1",
		BinStart:     time.Now().UTC(),
		StatusCounts: map[int]int{},
	})
	r.Nil(empty.Status, "a bin with nothing countable must not invent a status")
}

// TestTrimWindowedResponseTimeRows_SeamIsBudgetedAsTheRawTier walks the trim with
// seam rows in place of raw ones and pins the three things the spec asks for:
// the seam draws from the RAW tier's budget, the merged series stays newest-first
// across tiers, and the whole thing stays inside responseTimeLimit.
func TestTrimWindowedResponseTimeRows_SeamIsBudgetedAsTheRawTier(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC().Truncate(time.Hour)
	windowStart := now.AddDate(0, 0, -7)

	rows := make([]*models.Result, 0, 200)

	// 26 hourly seam bins — what a 7-day page's seam really looks like: the raw
	// clamp is 24 h + a 2 h margin, and seamBinWidth(7d) is 1 h.
	for i := range 26 {
		rows = append(rows, seamTestResult(now.Add(-time.Duration(i)*time.Hour), 60, 60, 120,
			int(models.ResultStatusUp)))
	}

	// Hour rollups just past the seam, then day rollups to the window's old end.
	for i := 26; i < 26+40; i++ {
		rows = append(rows, trimTestResult("hour-"+time.Duration(i).String(),
			models.PeriodTypeHour, now.Add(-time.Duration(i)*time.Hour)))
	}

	for i := 1; i <= 6; i++ {
		rows = append(rows, trimTestResult("day-"+time.Duration(i).String(),
			models.PeriodTypeDay, now.AddDate(0, 0, -i)))
	}

	sortResponseTimeRows(rows)

	kept := trimWindowedResponseTimeRows(rows, windowStart, now, 24)

	r.NotEmpty(kept)
	r.LessOrEqual(len(kept), responseTimeLimit, "the trim stays within the point budget")

	byTier := map[string]int{}
	for _, row := range kept {
		byTier[row.PeriodType]++
	}

	r.Positive(byTier[models.PeriodTypeSeam], "the seam must survive the trim")
	r.Positive(byTier[models.PeriodTypeHour], "so must the hour rollups next to it")
	r.Positive(byTier[models.PeriodTypeDay], "and the day rollups anchoring the old end")

	// The raw tier's share of a 7-day window is the raw clamp's 26 h — ~15 points —
	// so the seam is budgeted exactly as the raw tier was, never as the coarse
	// (day) tail it would fall into if the trim's switch had no seam case.
	r.LessOrEqual(byTier[models.PeriodTypeSeam], 26,
		"the seam cannot exceed the bins it has")
	r.GreaterOrEqual(byTier[models.PeriodTypeSeam], tierBudgetFloor,
		"and it gets at least the tier floor, like any tier with rows in the window")

	// One timeline: strictly descending period_start across the merged tiers.
	for i := 1; i < len(kept); i++ {
		r.False(kept[i].PeriodStart.After(kept[i-1].PeriodStart),
			"the merged series must stay newest-first across tiers")
	}
}

// TestTrimWindowedResponseTimeRows_SeamOnlyRegionStillRenders pins that a region
// whose ONLY in-window points are seam bins is kept. It is the common case for a
// young check — nothing has been rolled up yet — and a trim that recognised only
// raw and rollup rows would classify seam into the coarse tail, give it the day
// tier's budget, and (worse) a signal check that did not understand the seam's
// DurationP95 would delete the region outright.
func TestTrimWindowedResponseTimeRows_SeamOnlyRegionStillRenders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC().Truncate(15 * time.Minute)
	windowStart := now.Add(-24 * time.Hour)

	rows := make([]*models.Result, 0, 96)
	for i := range 96 {
		rows = append(rows, seamTestResult(now.Add(-time.Duration(i)*15*time.Minute), 15, 15, 88,
			int(models.ResultStatusUp)))
	}

	byRegion := map[string]map[string][]*models.Result{
		"check-1": {"eu2": rows},
	}

	trimResponseTimeSeries(byRegion, windowStart, now, 24)

	kept := byRegion["check-1"]["eu2"]
	r.NotEmpty(kept, "a seam-only region must survive")
	r.LessOrEqual(len(kept), responseTimeLimit)
	r.True(responseTimeRowsHaveSignal(kept), "seam rows carry their signal in DurationP95")

	points := buildResponseTimeData(kept, 99.9, 99.0)
	r.True(responseTimePointsHaveSignal(points),
		"and the built points keep it — buildResponseTimeData already prefers DurationP95")
	r.Len(points, len(kept))
	r.Equal(15, points[0].TotalChecks, "each point carries its bin's probe count")
}

// TestTrimWindowedResponseTimeRows_SeamMarkerPhantomStaysDropped is spec
// 2026-09-21-03 A.4, re-pinned on the seam path.
//
// The phantom was a NULL-region series made of nothing but the one-time "Check
// created" lifecycle marker, which can carry a literal 0 duration and would
// otherwise render as a one-point 0 ms chart series. The seam aggregate drops the
// excluded statuses before binning, so such a bin never materialises at all — but
// the downstream guard must still hold for a seam bin that legitimately has probes
// and no durations (a region that was fully down), which is the case below.
func TestTrimWindowedResponseTimeRows_SeamMarkerPhantomStaysDropped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC().Truncate(15 * time.Minute)
	windowStart := now.Add(-24 * time.Hour)

	// A seam bin with probes but NO duration at all — what AggregateResponseTimeBins
	// returns for a bin whose every probe failed before timing anything.
	durationless := seamResult(&models.ResponseTimeBin{
		CheckUID:     "check-1",
		BinStart:     now,
		Total:        15,
		Up:           0,
		StatusCounts: map[int]int{int(models.ResultStatusDown): 15},
	})

	// And a zero-duration one, the shape the marker phantom took.
	zero := float32(0)
	zeroed := seamResult(&models.ResponseTimeBin{
		CheckUID:     "check-1",
		BinStart:     now.Add(-15 * time.Minute),
		Total:        1,
		Up:           0,
		DurationP95:  &zero,
		StatusCounts: map[int]int{int(models.ResultStatusDown): 1},
	})

	byRegion := map[string]map[string][]*models.Result{
		"check-1": {
			"": {durationless, zeroed},
			"eu2": {
				seamTestResult(now, 15, 15, 88, int(models.ResultStatusUp)),
				seamTestResult(now.Add(-15*time.Minute), 15, 15, 91, int(models.ResultStatusUp)),
			},
		},
	}

	trimResponseTimeSeries(byRegion, windowStart, now, 24)

	r.NotContains(byRegion["check-1"], "",
		"a seam series with no usable duration must not reach the chart as a phantom region")
	r.Contains(byRegion["check-1"], "eu2", "the real region is untouched")
}

// TestBuildAvailabilityData_SeamRowsRenderAsPoints walks the whole public builder
// with seam rows and pins that the payload shape is IDENTICAL to what rollup rows
// produce — same fields, same values, nothing new for the front end to learn.
// That is what makes the status0 response-time-chart unit tests pass unchanged
// under this spec.
//
// The values are the discriminator: each point carries its BIN's probe count and
// availability, not a single probe's, so a bin with one failed probe out of fifteen
// renders as a degraded 93.3 % point rather than as a green one.
func TestBuildAvailabilityData_SeamRowsRenderAsPoints(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC().Truncate(15 * time.Minute)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	healthy := seamTestResult(now.Add(-15*time.Minute), 15, 15, 88, int(models.ResultStatusUp))
	degraded := seamResult(&models.ResponseTimeBin{
		CheckUID:    "check-1",
		BinStart:    now,
		Total:       15,
		Up:          14,
		DurationP95: func() *float32 { value := float32(310); return &value }(),
		StatusCounts: map[int]int{
			int(models.ResultStatusUp):   14,
			int(models.ResultStatusDown): 1,
		},
	})

	data := buildAvailabilityData(
		nil, map[string][]*models.Result{"eu2": {degraded, healthy}},
		todayStart, 1, false, true, 99.9, 99.0,
	)

	r.NotNil(data)
	r.Len(data.ResponseTimeSeries, 1)

	points := data.ResponseTimeSeries[0].Points
	r.Len(points, 2, "one point per seam bin")

	// buildResponseTimeData reverses to oldest-first.
	r.Equal(15, points[0].TotalChecks)
	r.Equal(15, points[0].SuccessfulChecks)
	r.Equal(statusUp, points[0].AvailabilityStatus)
	r.NotNil(points[0].DurationP95)
	r.InDelta(88.0, *points[0].DurationP95, 0.001)

	r.Equal(15, points[1].TotalChecks)
	r.Equal(14, points[1].SuccessfulChecks)
	r.NotNil(points[1].AvailabilityPct)
	r.InDelta(93.33, *points[1].AvailabilityPct, 0.01,
		"the point's availability is over its whole bin, not over one probe")
	r.Equal(statusDegraded, points[1].AvailabilityStatus,
		"one failed sample out of fifteen is degraded, never down (the small-bucket guard)")
	r.InDelta(310.0, *points[1].DurationP95, 0.001)
}
