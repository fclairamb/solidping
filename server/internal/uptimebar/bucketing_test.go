package uptimebar

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeLister returns a fixed result set, capturing the filter it was called
// with. It mimics the real DB's "ORDER BY period_start DESC" + "LIMIT" behavior
// (see postgres.ListResults / sqlite.ListResults) so tests can catch a
// regression where a row-count Limit gets reintroduced, and honors the filter's
// PeriodTypes so a fixture row from a tier the query doesn't ask for is NOT
// returned — without that fidelity, a test seeding month rows would pass even
// if the union query dropped the month tier and the under-count went undetected.
type fakeLister struct {
	results   []*models.Result
	gotFilter *models.ListResultsFilter
}

func (f *fakeLister) ListResults(
	_ context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	f.gotFilter = filter

	matching := make([]*models.Result, 0, len(f.results))

	for _, row := range f.results {
		if len(filter.PeriodTypes) > 0 && !slices.Contains(filter.PeriodTypes, row.PeriodType) {
			continue
		}

		matching = append(matching, row)
	}

	sort.SliceStable(matching, func(i, j int) bool {
		return matching[i].PeriodStart.After(matching[j].PeriodStart)
	})

	if filter.Limit > 0 && filter.Limit < len(matching) {
		matching = matching[:filter.Limit]
	}

	return &models.ListResultsResponse{Results: matching}, nil
}

func rawRow(checkUID string, status models.ResultStatus, start time.Time, dur float32) *models.Result {
	s := int(status)

	return &models.Result{
		CheckUID:    checkUID,
		PeriodType:  models.PeriodTypeRaw,
		PeriodStart: start,
		Status:      &s,
		Duration:    &dur,
	}
}

func hourRow(checkUID string, total, success int, start time.Time) *models.Result {
	return &models.Result{
		CheckUID:         checkUID,
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      start,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
	}
}

// dayRow is defined in window_test.go (same package) and reused here.

// TestBucketStats_AvailabilityPct covers the empty/non-empty distinction the
// caller uses to choose between a real status and "no data".
func TestBucketStats_AvailabilityPct(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, ok := BucketStats{}.AvailabilityPct()
	r.False(ok, "an empty bucket reports ok=false → caller renders noData")

	pct, ok := BucketStats{Up: 3, Total: 4}.AvailabilityPct()
	r.True(ok)
	r.InDelta(75.0, pct, 0.0001)
}

// TestBucketAvailability_RawSpansCurrentAndPreviousHour is the direct regression
// for the bug: a check with only raw rows in the current AND the previous hour
// fills BOTH buckets (the previous hour used to read "No data" on the status
// page because the raw→hour rollup lags).
func TestBucketAvailability_RawSpansCurrentAndPreviousHour(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	prevHour := currentHour.Add(-time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, prevHour.Add(5*time.Minute), 40),
		rawRow("c1", models.ResultStatusUp, currentHour.Add(5*time.Minute), 50),
	}}

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, 0, 0)
	r.NoError(err)

	byBucket := out["c1"]
	r.NotNil(byBucket)

	prev, ok := byBucket[prevHour].AvailabilityPct()
	r.True(ok, "the previous hour must be filled from raw, not noData")
	r.InDelta(100.0, prev, 0.0001)

	cur, ok := byBucket[currentHour].AvailabilityPct()
	r.True(ok)
	r.InDelta(100.0, cur, 0.0001)
}

// TestBucketAvailability_RawAndRollupNoDoubleCount asserts the non-overlap union:
// raw rows in a recent bucket + a stored hour row in an older bucket both fill,
// and neither inflates the other.
func TestBucketAvailability_RawAndRollupNoDoubleCount(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	olderHour := currentHour.Add(-5 * time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		// Recent bucket: raw only (2 up, 1 down → 2/3).
		rawRow("c1", models.ResultStatusUp, currentHour.Add(time.Minute), 40),
		rawRow("c1", models.ResultStatusUp, currentHour.Add(2*time.Minute), 40),
		rawRow("c1", models.ResultStatusDown, currentHour.Add(3*time.Minute), 0),
		// Older bucket: a stored hour rollup (60 total, 60 up → 100%).
		hourRow("c1", 60, 60, olderHour),
	}}

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, 0, 0)
	r.NoError(err)

	byBucket := out["c1"]

	cur := byBucket[currentHour]
	r.Equal(3, cur.Total, "raw bucket total is exactly the raw rows, not inflated by the rollup")
	r.Equal(2, cur.Up)
	curPct, _ := cur.AvailabilityPct()
	r.InDelta(66.6667, curPct, 0.01)

	older := byBucket[olderHour]
	r.Equal(60, older.Total, "rollup bucket reads the stored counts, not the raw rows")
	r.Equal(60, older.Up)
	olderPct, _ := older.AvailabilityPct()
	r.InDelta(100.0, olderPct, 0.0001)
}

// TestBucketAvailability_WarningCountsAsUpLifecycleExcluded pins the success rule
// for the raw tier: warning counts as up; created/running are excluded from the
// denominator.
func TestBucketAvailability_WarningCountsAsUpLifecycleExcluded(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(1*time.Minute), 40),
		rawRow("c1", models.ResultStatusWarning, currentHour.Add(2*time.Minute), 70),
		rawRow("c1", models.ResultStatusDown, currentHour.Add(3*time.Minute), 0),
		rawRow("c1", models.ResultStatusCreated, currentHour.Add(4*time.Minute), 0),
		rawRow("c1", models.ResultStatusRunning, currentHour.Add(5*time.Minute), 0),
	}}

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, 0, 0)
	r.NoError(err)

	stats := out["c1"][currentHour]
	r.Equal(3, stats.Total, "created/running lifecycle markers are excluded from the denominator")
	r.Equal(2, stats.Up, "up + warning both count as success")
	pct, ok := stats.AvailabilityPct()
	r.True(ok)
	r.InDelta(66.6667, pct, 0.01)
}

// TestBucketAvailability_EmptyBucketAbsent asserts a bucket with zero rows is
// absent from the inner map, so the caller knows to render noData.
func TestBucketAvailability_EmptyBucketAbsent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(time.Minute), 40),
	}}

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, 0, 0)
	r.NoError(err)

	byBucket := out["c1"]
	_, present := byBucket[bucketStart]
	r.False(present, "a bucket with no rows must be absent from the map")
	r.Len(byBucket, 1, "only the one bucket that has a row is present")
}

// TestBucketAvailability_MultiCheckSingleQuery confirms several checks are
// bucketed independently from one batched query with a bounded limit.
func TestBucketAvailability_MultiCheckSingleQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(time.Minute), 40),
		rawRow("c2", models.ResultStatusDown, currentHour.Add(time.Minute), 0),
	}}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1", "c2"}, time.Hour, bucketStart, 24, 0, 0,
	)
	r.NoError(err)

	c1, _ := out["c1"][currentHour].AvailabilityPct()
	r.InDelta(100.0, c1, 0.0001)
	c2, _ := out["c2"][currentHour].AvailabilityPct()
	r.InDelta(0.0, c2, 0.0001)

	r.NotNil(lister.gotFilter)
	// Not a row-count limit sized off "n buckets" or len(checkUIDs) (that
	// truncates dense windows — see TestBucketAvailability_DenseRowsFillAllBuckets)
	// but a generous retention-derived safety cap (see safetyRowCap): it must
	// exceed this tiny query's actual row count by a wide margin.
	wantLimit := safetyRowCap(2, 24, time.Hour, 0, 0)
	r.Equal(wantLimit, lister.gotFilter.Limit,
		"the query is bounded by the retention-derived safety cap")
	r.Greater(lister.gotFilter.Limit, len(lister.results),
		"the cap must be generous enough not to truncate this small query")
	r.ElementsMatch(
		[]string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		lister.gotFilter.PeriodTypes,
		"the union query spans raw+hour+day",
	)
	r.True(lister.gotFilter.SkipBlobs,
		"buckets are built from status/counts, so the metrics/output blobs must be "+
			"projected away (spec 2026-07-24-02)")
}

// TestBucketAvailability_NoChecks is a defensive guard for the empty-page case.
func TestBucketAvailability_NoChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	lister := &fakeLister{}
	out, err := BucketAvailability(context.Background(), lister, "org", nil, time.Hour, time.Now(), 24, 0, 0)
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter, "no query is issued when there are no checks")
}

// TestBucketAvailability_DenseRowsFillAllBuckets is the direct regression for
// the reported bug: a check whose window contains far more rows than buckets
// (dense today-only raw rows + one day rollup per older day) must have every
// bucket that has data filled, for all three long-range periods — not just the
// newest 1-3 days. Before the fix, Limit = n*len(checkUIDs) truncated the
// period_start-DESC-ordered query to the newest rows only, so older buckets
// silently read "no data" even though rows existed for them.
func TestBucketAvailability_DenseRowsFillAllBuckets(t *testing.T) {
	t.Parallel()

	for _, n := range []int{7, 30, 90} {
		t.Run(fmt.Sprintf("%dd", n), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			now := time.Now().UTC()
			todayStart := now.Truncate(24 * time.Hour)
			bucketStart := todayStart.Add(-time.Duration(n-1) * 24 * time.Hour)

			results := make([]*models.Result, 0, n-1+50)

			// One day-tier rollup row per day for every day except today.
			for i := 1; i < n; i++ {
				day := todayStart.Add(-time.Duration(i) * 24 * time.Hour)
				results = append(results, dayRow("c1", 100, 100, day))
			}

			// Dense raw rows for "today" — far more than one row, simulating
			// frequent per-region probing that hasn't rolled up yet. This is
			// what pushed the old Limit (n*len(checkUIDs)) past capacity and
			// squeezed out the older day rows.
			for i := range 50 {
				results = append(
					results,
					rawRow("c1", models.ResultStatusUp, todayStart.Add(time.Duration(i)*time.Minute), 40),
				)
			}

			lister := &fakeLister{results: results}

			out, err := BucketAvailability(
				context.Background(), lister, "org", []string{"c1"}, 24*time.Hour, bucketStart, n, 0, 0,
			)
			r.NoError(err)

			byBucket := out["c1"]
			r.Len(byBucket, n, "every bucket in the window must be filled, not just the newest few")

			for i := range n {
				bucket := bucketStart.Add(time.Duration(i) * 24 * time.Hour)
				_, ok := byBucket[bucket]
				r.True(ok, "bucket %d (%s) must have data", i, bucket)
			}
		})
	}
}

// TestBucketAvailability_MultiCheckDoesNotStarveOlderChecks is the status-page
// regression: a busy page batches several checks into ONE query
// (badges/service.go and statuspages/service.go share this exact call). Before
// the fix, a Limit shared across all checks in one DESC-ordered query meant a
// single dense/chatty check could crowd out another check's older buckets —
// or the whole other check — entirely.
func TestBucketAvailability_MultiCheckDoesNotStarveOlderChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const n = 30

	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	bucketStart := todayStart.Add(-time.Duration(n-1) * 24 * time.Hour)

	results := make([]*models.Result, 0, 100+n)

	// c1: dense — many raw rows, all "today" (the newest possible rows).
	for i := range 100 {
		results = append(
			results,
			rawRow("c1", models.ResultStatusUp, todayStart.Add(time.Duration(i)*time.Minute), 40),
		)
	}

	// c2: sparse but spans the entire window — one day-tier row per day,
	// including today. Even today's c2 row (PeriodStart = todayStart exactly)
	// sorts older than every c1 row above (all strictly after todayStart).
	for i := range n {
		day := todayStart.Add(-time.Duration(i) * 24 * time.Hour)
		results = append(results, dayRow("c2", 100, 100, day))
	}

	lister := &fakeLister{results: results}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1", "c2"}, 24*time.Hour, bucketStart, n, 0, 0,
	)
	r.NoError(err)

	r.Len(out["c2"], n, "c1's dense recent rows must not starve c2's older buckets out of the shared query")
	r.NotEmpty(out["c1"], "c1 itself must still be present")
}

// TestBucketAvailability_SafetyCapEngagesAndWarns is the pathological-scenario
// regression for the safety cap (see safetyRowCap): a lister returning far
// more rows than ANY reasonable retention configuration would ever produce for
// the requested window — simulating an aggregation job stalled/crashed
// indefinitely, so raw rows pile up without bound — must not be fetched
// unbounded. The query is capped, a warning is logged with org/check context,
// and the (partial) result is still returned rather than erroring — the same
// "generous cap + log + return partial" shape as the Slack client's
// pagination cap (see internal/integrations/slack/client.go's paginate and
// TestListChannelsStopsAtPageCap in client_test.go).
func TestBucketAvailability_SafetyCapEngagesAndWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Capture slog output for the duration of this test so the warning can be
	// asserted on, restoring the previous default logger afterwards.
	var logBuf bytes.Buffer

	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)

	// A single-bucket (n=1, 1h) window with default retention hints (0, 0 →
	// the documented 24h/30d fallback) yields a small cap. Feed it FAR more
	// raw rows than even a full RetentionRaw window at the platform's fastest
	// allowed period could produce — the pathological "aggregation job never
	// ran" case, not a realistic one.
	const pathologicalRowCount = 20_000

	results := make([]*models.Result, 0, pathologicalRowCount)
	for i := range pathologicalRowCount {
		results = append(
			results,
			rawRow("c1", models.ResultStatusUp, currentHour.Add(time.Duration(i)*time.Millisecond), 40),
		)
	}

	lister := &fakeLister{results: results}

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, currentHour, 1, 0, 0)
	r.NoError(err, "a capped, partial fetch must not error")

	r.NotNil(lister.gotFilter)

	wantLimit := safetyRowCap(1, 1, time.Hour, 0, 0)
	r.Less(wantLimit, pathologicalRowCount,
		"the cap must be smaller than the pathological row count for this test to be meaningful")
	r.Equal(wantLimit, lister.gotFilter.Limit, "the query is bounded by the safety cap")

	r.NotEmpty(out["c1"], "a bucket with partial data is still returned, not an error and not empty")

	logged := logBuf.String()
	r.Contains(logged, "hit its safety row cap", "a warning must be logged when the cap engages")
	r.Contains(logged, "organization_uid=org", "the warning must include org context")
	r.Contains(logged, "check_uids=", "the warning must include check context")
}
