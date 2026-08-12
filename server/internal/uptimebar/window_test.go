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

			out, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, now)
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
	out, err := WindowAvailability(context.Background(), lister, "org", nil, now.Add(-time.Hour), now)
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter, "no query should be issued when there are no checks")

	// end == start (non-positive window) → empty.
	out, err = WindowAvailability(context.Background(), lister, "org", []string{"c1"}, now, now)
	r.NoError(err)
	r.Empty(out)
}

// TestWindowAvailability_Filter asserts the query bounds the window with both
// edges and unions all four disjoint tiers — month included, since with default
// retention (day tier ≈ 2 months) everything older than that lives only in
// month rows (so older buckets are never dropped by a row cap the way the old
// client-side size:1000 limit did).
func TestWindowAvailability_Filter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)

	lister := &fakeLister{}
	_, err := WindowAvailability(context.Background(), lister, "org", []string{"c1"}, start, now)
	r.NoError(err)

	r.NotNil(lister.gotFilter)
	r.Equal("org", lister.gotFilter.OrganizationUID)
	r.Equal([]string{"c1"}, lister.gotFilter.CheckUIDs)
	r.ElementsMatch(
		[]string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth},
		lister.gotFilter.PeriodTypes,
	)
	r.NotNil(lister.gotFilter.PeriodStartAfter)
	r.NotNil(lister.gotFilter.PeriodEndBefore)
	r.True(lister.gotFilter.PeriodStartAfter.Equal(start))
	r.True(lister.gotFilter.PeriodEndBefore.Equal(now))
	r.True(lister.gotFilter.SkipBlobs,
		"availability is computed from status/counts, so the metrics/output blobs "+
			"must be projected away (spec 2026-07-24-02)")
}
