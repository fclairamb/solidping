package uptimebar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// dayRow builds an aggregated daily rollup row (mirrors hourRow in bucketing_test).
func dayRow(checkUID string, total, success int, start time.Time) *models.Result {
	return &models.Result{
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeDay,
		PeriodStart:      start,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
	}
}

// monthRow builds a terminal monthly rollup row.
func monthRow(checkUID string, total, success int, start time.Time) *models.Result {
	return &models.Result{
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeMonth,
		PeriodStart:      start,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
	}
}

// TestWindowAvailability folds a result set into a single BucketStats per check.
// It exercises the canonical counting rules and the disjoint-tier no-double-count
// guarantee that the per-period availability number relies on.
func TestWindowAvailability(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	start := now.Add(-365 * 24 * time.Hour)

	tests := []struct {
		name      string
		rows      []*models.Result
		wantUp    int
		wantTotal int
		wantOK    bool
	}{
		{
			name: "raw only",
			rows: []*models.Result{
				rawRow("c1", models.ResultStatusUp, now.Add(-time.Hour), 10),
				rawRow("c1", models.ResultStatusUp, now.Add(-2*time.Hour), 10),
				rawRow("c1", models.ResultStatusDown, now.Add(-3*time.Hour), 10),
			},
			wantUp:    2,
			wantTotal: 3,
			wantOK:    true,
		},
		{
			name: "aggregated only",
			rows: []*models.Result{
				hourRow("c1", 60, 60, now.Add(-48*time.Hour)),
				dayRow("c1", 1440, 1400, now.Add(-72*time.Hour)),
			},
			wantUp:    1460,
			wantTotal: 1500,
			wantOK:    true,
		},
		{
			name: "mixed disjoint window does not double-count",
			rows: []*models.Result{
				// Raw tier (recent), hour tier (mid), day tier (old) — disjoint
				// age bands, summed straight without de-dup.
				rawRow("c1", models.ResultStatusUp, now.Add(-time.Hour), 10),
				rawRow("c1", models.ResultStatusDown, now.Add(-2*time.Hour), 10),
				hourRow("c1", 100, 95, now.Add(-48*time.Hour)),
				dayRow("c1", 1000, 990, now.Add(-200*24*time.Hour)),
			},
			wantUp:    1086, // 1 + 95 + 990
			wantTotal: 1102, // 2 + 100 + 1000
			wantOK:    true,
		},
		{
			// The regression this guards: with default retention the day tier keeps
			// only ~2 months, so a 365d window's older data lives exclusively in
			// month rows. fakeLister honors filter.PeriodTypes, so if the union
			// query dropped the month tier these rows would vanish and the totals
			// would silently shrink to the day-side numbers.
			name: "window spanning the day→month boundary counts both tiers",
			rows: []*models.Result{
				// Young side of the boundary: day rollups within the day-tier band.
				dayRow("c1", 1440, 1430, now.Add(-10*24*time.Hour)),
				dayRow("c1", 1440, 1440, now.Add(-30*24*time.Hour)),
				// Old side of the boundary: terminal month rollups.
				monthRow("c1", 43200, 43000, now.Add(-120*24*time.Hour)),
				monthRow("c1", 43200, 42800, now.Add(-150*24*time.Hour)),
			},
			wantUp:    88670, // 1430 + 1440 + 43000 + 42800
			wantTotal: 89280, // 1440 + 1440 + 43200 + 43200
			wantOK:    true,
		},
		{
			name: "down/timeout/error count as failures",
			rows: []*models.Result{
				rawRow("c1", models.ResultStatusUp, now.Add(-time.Hour), 10),
				rawRow("c1", models.ResultStatusDown, now.Add(-2*time.Hour), 10),
				rawRow("c1", models.ResultStatusTimeout, now.Add(-3*time.Hour), 10),
				rawRow("c1", models.ResultStatusError, now.Add(-4*time.Hour), 10),
			},
			wantUp:    1,
			wantTotal: 4,
			wantOK:    true,
		},
		{
			name: "created/running lifecycle markers are skipped",
			rows: []*models.Result{
				rawRow("c1", models.ResultStatusCreated, now.Add(-time.Hour), 0),
				rawRow("c1", models.ResultStatusRunning, now.Add(-2*time.Hour), 0),
				rawRow("c1", models.ResultStatusUp, now.Add(-3*time.Hour), 10),
			},
			wantUp:    1,
			wantTotal: 1,
			wantOK:    true,
		},
		{
			name: "warning counts as up",
			rows: []*models.Result{
				rawRow("c1", models.ResultStatusWarning, now.Add(-time.Hour), 10),
				rawRow("c1", models.ResultStatusUp, now.Add(-2*time.Hour), 10),
			},
			wantUp:    2,
			wantTotal: 2,
			wantOK:    true,
		},
		{
			name:      "empty window reports no data",
			rows:      nil,
			wantUp:    0,
			wantTotal: 0,
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			lister := &fakeLister{results: tc.rows}

			out, err := WindowAvailability(
				context.Background(), lister, "org", []string{"c1"}, start, now, hints())
			r.NoError(err)

			stats := out["c1"]
			r.Equal(tc.wantUp, stats.Up)
			r.Equal(tc.wantTotal, stats.Total)

			_, ok := stats.AvailabilityPct()
			r.Equal(tc.wantOK, ok)
		})
	}
}

// TestWindowAvailability_Empty covers the guard inputs: no checks and a
// non-positive window both return an empty map without querying.
func TestWindowAvailability_Empty(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	lister := &fakeLister{results: []*models.Result{rawRow("c1", models.ResultStatusUp, now, 10)}}

	// No checks → empty, query never run.
	out, err := WindowAvailability(context.Background(), lister, "org", nil, now.Add(-time.Hour), now, hints())
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter(), "no query should be issued when there are no checks")

	// end == start (non-positive window) → empty.
	out, err = WindowAvailability(context.Background(), lister, "org", []string{"c1"}, now, now, hints())
	r.NoError(err)
	r.Empty(out)
}

// TestWindowAvailability_RawTierClampedAndDisjoint pins the tier split on the
// window path: the raw query is clamped to the raw-retention band (so it uses
// results_raw_idx instead of scanning), the rollup query keeps the full window,
// and a stale raw row inside an already-rolled-up period is NOT counted on top
// of the rollup that already covers it.
func TestWindowAvailability_RawTierClampedAndDisjoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	start := now.Add(-90 * 24 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		dayRow("c1", 1000, 1000, now.Add(-10*24*time.Hour)),
		// Same period as the rollup above, but raw: impossible in production
		// (rollup + delete happen in one transaction) and excluded by the clamp.
		rawRow("c1", models.ResultStatusDown, now.Add(-10*24*time.Hour).Add(time.Hour), 10),
		// Inside the retention band: counted.
		rawRow("c1", models.ResultStatusUp, now.Add(-time.Hour), 10),
	}}

	out, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, now, hints())
	r.NoError(err)

	stats := out["c1"]
	r.Equal(1001, stats.Total, "the stale raw row must not be double-counted against its rollup")
	r.Equal(1001, stats.Up, "and its 'down' status must not leak into the availability either")

	r.Len(lister.gotFilters, 2, "one query per tier group, not a single straddling union")

	rollup := lister.filterFor(models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth)
	r.NotNil(rollup, "the rollup query spans hour+day+month")
	r.NotContains(rollup.PeriodTypes, models.PeriodTypeRaw)
	r.True(rollup.PeriodStartAfter.Equal(start))
	r.True(rollup.PeriodEndBefore.Equal(now))
	r.Equal(rollupRowCap(hints(), 1, now.Sub(start), true), rollup.Limit)

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw)
	r.WithinDuration(now.Add(-26*time.Hour), *raw.PeriodStartAfter, time.Minute,
		"raw is clamped to RetentionRaw + rawClampMargin, not the caller's 90-day window")
	r.True(raw.PeriodEndBefore.Equal(now))
	r.Equal(rawRowCap(hints(), 1, now.Sub(start)), raw.Limit)

	for _, filter := range lister.gotFilters {
		r.True(filter.SkipBlobs, "both tier queries must project the blobs away (spec 2026-07-24-02)")
	}
}

// TestWindowAvailability_PastWindowSkipsRawQuery covers the window that ends
// before the raw-retention band even starts (e.g. "last year"): no raw row can
// exist in it, so the raw query is skipped entirely rather than issued with an
// empty range.
func TestWindowAvailability_PastWindowSkipsRawQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	end := now.Add(-30 * 24 * time.Hour)
	start := end.Add(-30 * 24 * time.Hour)

	lister := &fakeLister{results: []*models.Result{dayRow("c1", 100, 99, end.Add(-24*time.Hour))}}

	out, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, end, hints())
	r.NoError(err)

	r.Equal(100, out["c1"].Total, "the rollup tiers still answer a fully historical window")
	r.Len(lister.gotFilters, 1, "only the rollup query is issued")
	r.Nil(lister.filterFor(models.PeriodTypeRaw))
}

// TestWindowAvailability_NoDataIsNotHundredPercent pins the contract documented
// on WindowAvailability: a check with no rows in the window is either absent
// from the map or has Total == 0, and must never read as 100%.
func TestWindowAvailability_NoDataIsNotHundredPercent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)

	lister := &fakeLister{}

	out, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, now, hints())
	r.NoError(err)

	stats, present := out["c1"]
	r.False(present, "a check with no rows is absent from the map")

	pct, ok := stats.AvailabilityPct()
	r.False(ok, "an empty BucketStats reports no data, never 100%")
	r.Zero(pct)
}

// TestWindowAvailability_Filter asserts the queries bound the window with both
// edges and, between them, cover all four disjoint tiers — month included, since
// with default retention (day tier ≈ 2 months) everything older than that lives
// only in month rows (so older buckets are never dropped by a row cap the way the
// old client-side size:1000 limit did). The tiers are split across two queries so
// each predicate is implied by one of the PARTIAL indexes on `results`
// (spec 2026-08-17-03).
func TestWindowAvailability_Filter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)

	lister := &fakeLister{}
	_, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, now, hints())
	r.NoError(err)

	covered := make([]string, 0, 4)

	for _, filter := range lister.gotFilters {
		r.Equal("org", filter.OrganizationUID)
		r.Equal([]string{"c1"}, filter.CheckUIDs)
		r.NotNil(filter.PeriodStartAfter)
		r.NotNil(filter.PeriodEndBefore)
		r.True(filter.PeriodEndBefore.Equal(now))
		r.True(filter.SkipBlobs,
			"availability is computed from status/counts, so the metrics/output blobs "+
				"must be projected away (spec 2026-07-24-02)")

		covered = append(covered, filter.PeriodTypes...)
	}

	r.ElementsMatch(
		[]string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth},
		covered,
		"every tier is still covered, exactly once, across the two queries",
	)

	rollup := lister.filterFor(models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth)
	r.NotNil(rollup)
	r.True(rollup.PeriodStartAfter.Equal(start), "the rollup tiers cover the caller's full window")
}
