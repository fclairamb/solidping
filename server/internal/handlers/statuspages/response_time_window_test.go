package statuspages

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// --- Spec 2026-09-21-03: the response-time series is bounded to the page's
// own history window, equalized across regions, and deaf to 0 ms markers. ---

// seedRegionRaw writes `count` raw rows for one region of a check, newest at
// `newest`, at a 1-minute cadence.
func seedRegionRaw(
	ctx context.Context, t *testing.T, svc *Service, orgUID, checkUID, region string,
	newest time.Time, count int,
) {
	t.Helper()

	for i := range count {
		row := models.NewResult(orgUID, checkUID, models.ResultStatusUp, float32(40+i%9))
		row.PeriodStart = newest.Add(-time.Duration(i) * time.Minute)
		row.Region = &region
		require.NoError(t, svc.db.CreateResult(ctx, row))
	}
}

// TestFetchRecentResults_WindowDropsRetiredRegion pins A.1 + A.3: a region
// whose last point predates the page's history window must not appear at all,
// while the live region keeps its in-window points.
func TestFetchRecentResults_WindowDropsRetiredRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC()

	check := models.NewCheck(org.UID, "multi-region", "http")
	check.Regions = []string{"eu2", "old"}
	r.NoError(svc.db.CreateCheck(ctx, check))

	// The live region probes every minute right up to now; the retired one
	// stopped five weeks ago.
	seedRegionRaw(ctx, t, svc, org.UID, check.UID, "eu2", now, 50)
	seedRegionRaw(ctx, t, svc, org.UID, check.UID, "old", now.Add(-35*24*time.Hour), 50)

	// A 30-day page: the window starts (HistoryDays-1) full days before
	// today's midnight, matching the availability bar.
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := todayStart.AddDate(0, 0, -29)

	got := svc.fetchRecentResults(ctx, org.UID, []string{check.UID}, true,
		windowStart, svc.uptimebarHints(ctx, org.UID))

	byRegion := got[check.UID]
	r.Len(byRegion, 1, "the retired region must be dropped entirely — no empty series, no legend entry")
	r.NotContains(byRegion, "old")
	r.Contains(byRegion, "eu2")
	r.NotEmpty(byRegion["eu2"])

	for _, row := range byRegion["eu2"] {
		r.False(row.PeriodStart.Before(windowStart),
			"no point older than the page's history window may survive")
	}
}

// TestFetchRecentResults_RetiredRegionKeepsItsOwnWindow is the same fixture on
// a window that still covers the retired region: there, BOTH series exist —
// the drop is driven by the window, not by the region's age as such.
func TestFetchRecentResults_RetiredRegionKeepsItsOwnWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC()

	check := models.NewCheck(org.UID, "multi-region-wide", "http")
	check.Regions = []string{"eu2", "old"}
	r.NoError(svc.db.CreateCheck(ctx, check))

	seedRegionRaw(ctx, t, svc, org.UID, check.UID, "eu2", now, 50)
	seedRegionRaw(ctx, t, svc, org.UID, check.UID, "old", now.Add(-35*24*time.Hour), 50)

	// Raw older than the retention clamp is fetched by nobody — in production
	// it has been rolled up and deleted — so the retired region's in-window
	// history survives as day rollups, exactly as the aggregator would have
	// left it.
	region := "old"
	for i := 36; i <= 40; i++ {
		row := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
		row.PeriodType = models.PeriodTypeDay
		row.PeriodStart = now.AddDate(0, 0, -i)
		row.Region = &region

		total, ok, p95 := 1440, 1440, float32(45)
		row.TotalChecks = &total
		row.SuccessfulChecks = &ok
		row.DurationP95 = &p95

		require.NoError(t, svc.db.CreateResult(ctx, row))
	}

	// A 90-day page still covers the region that stopped five weeks ago.
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := todayStart.AddDate(0, 0, -89)

	got := svc.fetchRecentResults(ctx, org.UID, []string{check.UID}, true,
		windowStart, svc.uptimebarHints(ctx, org.UID))

	r.Len(got[check.UID], 2, "inside a wide enough window both regions are real series")
}

// TestFetchRecentResults_WindowCeilPinsToRollupSpan pins A.1's hard ceiling:
// an absurd window (a 400-day HistoryDays) is clamped to
// responseTimeRollupSpan, so rollups older than the ceiling never reach the
// chart even though the caller asked for them.
func TestFetchRecentResults_WindowCeilPinsToRollupSpan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC()

	check := models.NewCheck(org.UID, "ancient", "http")
	check.Regions = []string{"eu2"}
	r.NoError(svc.db.CreateCheck(ctx, check))

	region := "eu2"

	// One hour rollup inside the 200-day ceiling, one far beyond it.
	inRollups := make([]*models.Result, 0, 1)
	for _, ageDays := range []int{150, 300} {
		row := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
		row.PeriodType = models.PeriodTypeHour
		row.PeriodStart = now.AddDate(0, 0, -ageDays)
		row.Region = &region

		total, ok, p95 := 60, 60, float32(42)
		row.TotalChecks = &total
		row.SuccessfulChecks = &ok
		row.DurationP95 = &p95

		require.NoError(t, svc.db.CreateResult(ctx, row))
		if ageDays == 150 {
			inRollups = append(inRollups, row)
		}
	}

	// Far beyond the ceiling: the fetch must clamp the window to
	// responseTimeRollupSpan (200 days), not honor 400 days.
	windowStart := now.AddDate(0, 0, -400)

	got := svc.fetchRecentResults(ctx, org.UID, []string{check.UID}, true,
		windowStart, svc.uptimebarHints(ctx, org.UID))

	rows := got[check.UID]["eu2"]
	r.Len(rows, 1, "only the rollup inside the 200-day ceiling survives")
	r.Equal(inRollups[0].UID, rows[0].UID)
}

// TestFetchRecentResults_WindowedTrimSpreadsTheWindow pins A.2's shape on a
// real fetch: a 1-minute check with 1 500 in-window raw rows and day rollups
// comes back with points spanning the whole window, not the newest 100
// minutes of raw.
func TestFetchRecentResults_WindowedTrimSpreadsTheWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC()

	check := models.NewCheck(org.UID, "dense", "http")
	check.Regions = []string{"eu2"}
	r.NoError(svc.db.CreateCheck(ctx, check))

	// 1 500 raw rows: 1 400 within the last 24 h (raw retention) and 100 more
	// reaching 40 h back — past the default 24 h retention clamp, so they
	// exist only to be cut by it. Plus day rollups for 30 days.
	seedRegionRaw(ctx, t, svc, org.UID, check.UID, "eu2", now, 1500)

	region := "eu2"
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	for i := 1; i <= 30; i++ {
		row := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
		row.PeriodType = models.PeriodTypeDay
		row.PeriodStart = dayStart.AddDate(0, 0, -i)
		row.Region = &region

		total, ok, p95 := 1440, 1440, float32(50)
		row.TotalChecks = &total
		row.SuccessfulChecks = &ok
		row.DurationP95 = &p95

		require.NoError(t, svc.db.CreateResult(ctx, row))
	}

	todayStart := dayStart
	windowStart := todayStart.AddDate(0, 0, -29)

	got := svc.fetchRecentResults(ctx, org.UID, []string{check.UID}, true,
		windowStart, svc.uptimebarHints(ctx, org.UID))

	rows := got[check.UID]["eu2"]
	r.NotEmpty(rows)
	r.LessOrEqual(len(rows), responseTimeLimit, "the windowed trim stays within the budget")

	// The series must SPAN the window: its oldest point sits near windowStart,
	// its newest near now — not 100 minutes of raw at the right edge.
	oldest := rows[len(rows)-1].PeriodStart
	newest := rows[0].PeriodStart
	r.False(oldest.Before(windowStart.AddDate(0, 0, -1)),
		"the oldest kept point reaches the window's old end")
	r.Less(oldest, windowStart.AddDate(0, 0, 2),
		"the oldest kept point is not the recent end — the window is covered")
	r.True(newest.After(now.Add(-2*time.Hour)), "the newest kept point reaches now")

	// And both tiers contribute: raw for the seam, day rollups for the tail.
	periodTypes := map[string]int{}
	for _, row := range rows {
		periodTypes[row.PeriodType]++
	}
	r.Positive(periodTypes[models.PeriodTypeDay], "day rollups anchor the window's old end")
	r.Positive(periodTypes[models.PeriodTypeRaw], "raw covers the recent seam")
}

// TestTrimResponseTimeSeries_WindowDropsEmptyRegion pins the A.3 rule at the
// trim level: a region whose rows all fall outside the window is deleted, so
// buildResponseTimeSeries never sees it.
func TestTrimResponseTimeSeries_WindowDropsEmptyRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	windowStart := now.Add(-30 * 24 * time.Hour)

	byRegion := map[string]map[string][]*models.Result{
		"check-1": {
			"live": {trimTestResult("uid-live", models.PeriodTypeRaw, now.Add(-time.Hour))},
			"gone": {trimTestResult("uid-gone", models.PeriodTypeRaw, now.Add(-60*24*time.Hour))},
		},
	}

	trimResponseTimeSeries(byRegion, windowStart, now, 24)

	r.Contains(byRegion["check-1"], "live")
	r.NotContains(byRegion["check-1"], "gone",
		"a region with no point inside the window must be deleted, not kept as an empty series")
}

// TestTrimWindowedResponseTimeRows_Budgets pins the sub-sampling arithmetic:
// within responseTimeLimit points, newest and oldest of each tier survive,
// the order is newest-first, and every kept row is in-window.
func TestTrimWindowedResponseTimeRows_Budgets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := dayStart.AddDate(0, 0, -29) // ~30-day window, day-aligned

	var rows []*models.Result

	// 600 raw rows at 1-minute cadence (10 h of seam — capped by retention in
	// real data, but the trim does not care).
	for i := range 600 {
		rows = append(rows, trimTestResult(
			"raw-"+string(rune('a'+i%26))+string(rune('0'+i/26)),
			models.PeriodTypeRaw, now.Add(-time.Duration(i)*time.Minute)))
	}

	// 200 hour rollups covering ~8 days.
	for i := range 200 {
		rows = append(rows, trimTestResult("hour", models.PeriodTypeHour, now.Add(-time.Duration(i+10)*time.Hour)))
	}

	// 30 day rollups: 29 inside the window (dayStart−29 … dayStart−1) and one
	// (dayStart−30) a day BEFORE windowStart, which the trim must drop.
	for i := 1; i <= 30; i++ {
		rows = append(rows, trimTestResult("day", models.PeriodTypeDay, dayStart.AddDate(0, 0, -i)))
	}

	kept := trimWindowedResponseTimeRows(rows, windowStart, now, 24)

	r.LessOrEqual(len(kept), responseTimeLimit)

	// Newest-first, every row inside the window.
	for i := 1; i < len(kept); i++ {
		r.False(kept[i-1].PeriodStart.Before(kept[i].PeriodStart), "kept rows must stay newest-first")
	}

	for _, row := range kept {
		r.False(row.PeriodStart.Before(windowStart))
	}

	// The raw tier's newest point and the day tier's oldest IN-WINDOW point
	// (dayStart−29 = windowStart) both survive — the sub-sample covers its
	// tier's full span, not its recent end only, and the out-of-window
	// dayStart−30 rollup does not.
	hasNewestRaw, hasOldestDay := false, false
	for _, row := range kept {
		if row.PeriodType == models.PeriodTypeRaw && row.PeriodStart.Equal(now) {
			hasNewestRaw = true
		}
		if row.PeriodType == models.PeriodTypeDay &&
			row.PeriodStart.Equal(dayStart.AddDate(0, 0, -29)) {
			hasOldestDay = true
		}
	}
	r.True(hasNewestRaw, "the raw tier keeps its newest row")
	r.True(hasOldestDay, "the day tier keeps its oldest row — the window's old end is represented")
}

// TestResponseTimeTierBudgets_FloorsAndOverflow pins the allocation rule.
func TestResponseTimeTierBudgets_FloorsAndOverflow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	windowStart := now.AddDate(0, 0, -29) // ~30-day window
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// A 30-day window: raw seam ~26 h, hours for the last week, days for the
	// rest. Total budget must stay within responseTimeLimit.
	rawRows := make([]*models.Result, 400)
	for i := range rawRows {
		rawRows[i] = trimTestResult("raw", models.PeriodTypeRaw, now.Add(-time.Duration(i)*time.Minute))
	}

	hourRows := make([]*models.Result, 168)
	for i := range hourRows {
		hourRows[i] = trimTestResult("hour", models.PeriodTypeHour, now.Add(-time.Duration(i+10)*time.Hour))
	}

	dayRows := make([]*models.Result, 23)
	for i := range dayRows {
		dayRows[i] = trimTestResult("day", models.PeriodTypeDay, dayStart.AddDate(0, 0, -(i+1)))
	}

	rawBudget, hourBudget, dayBudget := responseTimeTierBudgets(
		now.Sub(windowStart), rawRows, hourRows, dayRows, now, windowStart, 24)

	r.LessOrEqual(rawBudget+hourBudget+dayBudget, responseTimeLimit)
	r.GreaterOrEqual(rawBudget, tierBudgetFloor)
	r.GreaterOrEqual(hourBudget, tierBudgetFloor)
	r.GreaterOrEqual(dayBudget, tierBudgetFloor)

	// A window whose only rows are raw (no rollups yet — a brand-new check)
	// gives everything to raw.
	rawOnly := make([]*models.Result, 500)
	for i := range rawOnly {
		rawOnly[i] = trimTestResult("raw", models.PeriodTypeRaw, now.Add(-time.Duration(i)*time.Minute))
	}
	rb, hb, db := responseTimeTierBudgets(
		now.Sub(windowStart), rawOnly, nil, nil, now, windowStart, 24)
	r.Equal(0, hb)
	r.Equal(0, db)
	r.LessOrEqual(rb, responseTimeLimit)

	// Many sparse tiers cannot overflow: floors push up, the clamp pulls back.
	wideStart := now.AddDate(0, 0, -99)
	manyRaw := make([]*models.Result, 200)
	for i := range manyRaw {
		manyRaw[i] = trimTestResult("raw", models.PeriodTypeRaw, now.Add(-time.Duration(i)*time.Minute))
	}

	manyHour := make([]*models.Result, 200)
	for i := range manyHour {
		manyHour[i] = trimTestResult("hour", models.PeriodTypeHour, now.Add(-time.Duration(i+10)*time.Hour))
	}

	manyDay := make([]*models.Result, 95)
	for i := range manyDay {
		manyDay[i] = trimTestResult("day", models.PeriodTypeDay, dayStart.AddDate(0, 0, -(i+1)))
	}

	rb2, hb2, db2 := responseTimeTierBudgets(
		now.Sub(wideStart), manyRaw, manyHour, manyDay, now, wideStart, 24)
	r.LessOrEqual(rb2+hb2+db2, responseTimeLimit)

	_ = dayStart
}

// TestBuildResponseTimeSeries_NullRegionZeroMarkerYieldsNoSeries pins A.4: a
// NULL-region row carrying a literal 0 ms duration — the shape the screenshot's
// phantom "unknown region" series came from — must not manufacture a series.
func TestBuildResponseTimeSeries_NullRegionZeroMarkerYieldsNoSeries(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	zero := float32(0)
	created := int(models.ResultStatusCreated)

	marker := &models.Result{
		UID:         "marker",
		PeriodType:  models.PeriodTypeRaw,
		PeriodStart: time.Now().UTC(),
		Duration:    &zero,
		Status:      &created,
		// Region nil — the NULL/legacy region bucket.
	}

	series := buildResponseTimeSeries(map[string][]*models.Result{"": {marker}}, 99.9, 99.0)
	r.Empty(series, "a NULL-region 0 ms marker is not a series")
}

// TestBuildResponseTimeData_LifecycleMarkerDropsItsDuration pins the point
// builder half of A.4: a lifecycle marker's duration is dropped entirely, even
// when the row carries a literal 0.
func TestBuildResponseTimeData_LifecycleMarkerDropsItsDuration(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	zero := float32(0)
	created := int(models.ResultStatusCreated)
	up := int(models.ResultStatusUp)
	real := float32(42)

	// Newest-first, the order buildResponseTimeData consumes: the real probe
	// is newer than the marker, so after the builder's reversal the probe is
	// points[0] and the marker points[1].
	rows := []*models.Result{
		{UID: "marker", PeriodType: models.PeriodTypeRaw, Duration: &zero, Status: &created},
		{UID: "real", PeriodType: models.PeriodTypeRaw, Duration: &real, Status: &up},
	}

	points := buildResponseTimeData(rows, 99.9, 99.0)
	r.Len(points, 2)
	r.NotNil(points[0].DurationP95)
	r.Nil(points[1].DurationP95,
		"a lifecycle marker's 0 ms duration is treated like a nil one")
}

// TestResponseTimePointsHaveSignal_ZeroIsNoSignal pins the signal half of A.4.
func TestResponseTimePointsHaveSignal_ZeroIsNoSignal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	zero := float32(0)
	real := float32(42)

	r.False(responseTimePointsHaveSignal([]ResponseTimePoint{{DurationP95: &zero}}))
	r.True(responseTimePointsHaveSignal([]ResponseTimePoint{{DurationP95: &real}}))
	r.False(responseTimePointsHaveSignal(nil))
}

var _ = uptimebar.Hints{} // keep the import when hints go unused in a fixture
