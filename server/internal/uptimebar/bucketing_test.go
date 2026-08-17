package uptimebar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// fakeLister returns a fixed result set, capturing EVERY filter it was called
// with (the availability engine issues one query per tier — see
// BucketAvailability). It mimics the real DB's "ORDER BY period_start DESC" +
// "LIMIT" behavior (see postgres.ListResults / sqlite.ListResults) so tests can
// catch a regression where a row-count Limit gets reintroduced, and honors the
// filter's PeriodTypes and time bounds so a fixture row the query doesn't ask
// for is NOT returned. Without that fidelity a test seeding month rows would
// pass even if the query dropped the month tier, and the raw clamp (which is
// what keeps the tiers disjoint) would be untestable.
type fakeLister struct {
	results    []*models.Result
	gotFilters []*models.ListResultsFilter
}

// gotFilter returns the last captured filter, or nil when no query was issued.
func (f *fakeLister) gotFilter() *models.ListResultsFilter {
	if len(f.gotFilters) == 0 {
		return nil
	}

	return f.gotFilters[len(f.gotFilters)-1]
}

// filterFor returns the captured filter whose PeriodTypes are exactly the given
// tiers, or nil when no such query was issued.
func (f *fakeLister) filterFor(periodTypes ...string) *models.ListResultsFilter {
	for _, filter := range f.gotFilters {
		if len(filter.PeriodTypes) != len(periodTypes) {
			continue
		}

		match := true

		for _, want := range periodTypes {
			if !slices.Contains(filter.PeriodTypes, want) {
				match = false

				break
			}
		}

		if match {
			return filter
		}
	}

	return nil
}

func (f *fakeLister) ListResults(
	_ context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	f.gotFilters = append(f.gotFilters, filter)

	matching := make([]*models.Result, 0, len(f.results))

	for _, row := range f.results {
		if len(filter.PeriodTypes) > 0 && !slices.Contains(filter.PeriodTypes, row.PeriodType) {
			continue
		}

		if filter.PeriodStartAfter != nil && row.PeriodStart.Before(*filter.PeriodStartAfter) {
			continue
		}

		if filter.PeriodEndBefore != nil && !row.PeriodStart.Before(*filter.PeriodEndBefore) {
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

// Hints shorthands: hints() mirrors the live default retention (24 h raw, 7 d
// hourly) with no measured probe rate; noHints() exercises the documented
// fallbacks.
func hints() Hints { return Hints{RetentionRawHours: 24, RetentionHourDays: 7} }

func noHints() Hints { return Hints{} }

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

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
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

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
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

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
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

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
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
		context.Background(), lister, "org", []string{"c1", "c2"}, time.Hour, bucketStart, 24, noHints(),
	)
	r.NoError(err)

	c1, _ := out["c1"][currentHour].AvailabilityPct()
	r.InDelta(100.0, c1, 0.0001)
	c2, _ := out["c2"][currentHour].AvailabilityPct()
	r.InDelta(0.0, c2, 0.0001)

	// Exactly two queries, one per index-aligned tier group: a predicate
	// straddling the two PARTIAL indexes on `results` matches neither and forces
	// a full sequential scan (spec 2026-08-17-03).
	r.Len(lister.gotFilters, 2, "one query per tier group, not a single straddling union")

	rollup := lister.filterFor(models.PeriodTypeHour, models.PeriodTypeDay)
	r.NotNil(rollup, "a rollup-only query (hour+day) must be issued")
	r.NotContains(rollup.PeriodTypes, models.PeriodTypeRaw,
		"the rollup query must not mention raw — that is what defeats results_aggregated_idx")

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw, "a raw-only query must be issued")

	// Not a row-count limit sized off "n buckets" or len(checkUIDs) (that
	// truncates dense windows — see TestBucketAvailability_DenseRowsFillAllBuckets)
	// but a per-tier retention-derived safety cap: it must exceed this tiny
	// query's actual row count by a wide margin.
	r.Equal(rollupRowCap(noHints(), 2, 24*time.Hour, false), rollup.Limit,
		"the rollup query is bounded by the rollup-tier safety cap")
	r.Equal(rawRowCap(noHints(), 2, 24*time.Hour), raw.Limit,
		"the raw query is bounded by the raw-tier safety cap")
	r.Greater(rollup.Limit, len(lister.results),
		"the cap must be generous enough not to truncate this small query")

	for _, filter := range lister.gotFilters {
		r.True(filter.SkipBlobs,
			"buckets are built from status/counts, so the metrics/output blobs must be "+
				"projected away on EVERY tier query (spec 2026-07-24-02)")
		r.Equal("org", filter.OrganizationUID)
		r.Equal([]string{"c1", "c2"}, filter.CheckUIDs)
	}
}

// TestBucketAvailability_RawTierIsClampedToRetention pins the raw half of the
// split: the raw query must NOT ask for the caller's full window (which is what
// turns results_raw_idx into a full scan), but for
// max(windowStart, now-(RetentionRaw+rawClampMargin)). The rollup query keeps
// the full window.
func TestBucketAvailability_RawTierIsClampedToRetention(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const n = 30

	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	bucketStart := todayStart.Add(-time.Duration(n-1) * 24 * time.Hour)

	lister := &fakeLister{}

	_, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, 24*time.Hour, bucketStart, n, hints(),
	)
	r.NoError(err)

	rollup := lister.filterFor(models.PeriodTypeHour, models.PeriodTypeDay)
	r.NotNil(rollup)
	r.NotNil(rollup.PeriodStartAfter)
	r.True(rollup.PeriodStartAfter.Equal(bucketStart),
		"the rollup tiers cover the caller's full window")

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw)
	r.NotNil(raw.PeriodStartAfter)
	r.True(raw.PeriodStartAfter.After(bucketStart),
		"the raw tier must be clamped well inside a 30-day window")

	// 24h retention + the 2h margin, measured from now.
	r.WithinDuration(now.Add(-26*time.Hour), *raw.PeriodStartAfter, time.Minute)
}

// TestBucketAvailability_ClampKeepsTiersDisjoint is the double-counting
// regression. The accumulator adds raw and rollup rows into the SAME
// BucketStats, so correctness rests entirely on the two tiers never covering the
// same period. Here a stale raw row sits in the very same daily bucket as a day
// rollup — exactly the shape the aggregation job makes impossible (it compacts
// and deletes in one transaction). If the raw clamp were widened past the rollup
// boundary, that bucket's Total would inflate from 100 to 101.
func TestBucketAvailability_ClampKeepsTiersDisjoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const n = 30

	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	bucketStart := todayStart.Add(-time.Duration(n-1) * 24 * time.Hour)
	oldDay := todayStart.Add(-10 * 24 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		dayRow("c1", 100, 100, oldDay),
		// A raw row 10 days old: rolled up and deleted in production, so it must
		// never reach the accumulator alongside the rollup that already counts it.
		rawRow("c1", models.ResultStatusUp, oldDay.Add(time.Hour), 40),
	}}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, 24*time.Hour, bucketStart, n, hints(),
	)
	r.NoError(err)

	bucket := out["c1"][oldDay]
	r.Equal(100, bucket.Total,
		"a bucket must be fed by exactly one tier — the clamp keeps raw out of a rolled-up period")
	r.Equal(100, bucket.Up)
}

// TestBucketAvailability_WarnsWhenAggregationLags asserts the observability half
// of the clamp: raw rows older than RetentionRaw only survive thanks to
// rawClampMargin, and their presence means the aggregation job is behind. That
// is logged (and the data still returned) rather than absorbed silently.
func TestBucketAvailability_WarnsWhenAggregationLags(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var logBuf bytes.Buffer

	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))

	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-47 * time.Hour)

	// 25h old: past the 24h retention, inside the 2h margin.
	stale := now.Add(-25 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, stale, 40),
	}}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 48, hints(),
	)
	r.NoError(err)
	r.NotEmpty(out["c1"], "the lagging raw row is still counted, not dropped")

	logged := logBuf.String()
	r.Contains(logged, "aggregation is lagging")
	r.Contains(logged, "organization_uid=org")
}

// TestBucketAvailability_NoWarningWhenAggregationHealthy is the positive control
// for the test above: with every raw row inside the retention band, the lagging
// warning must NOT fire (otherwise the assertion above would pass on any input).
func TestBucketAvailability_NoWarningWhenAggregationHealthy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var logBuf bytes.Buffer

	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))

	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-47 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, now.Add(-time.Hour), 40),
	}}

	_, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 48, hints(),
	)
	r.NoError(err)

	r.NotContains(logBuf.String(), "aggregation is lagging")
}

// TestBucketAvailability_NoChecks is a defensive guard for the empty-page case.
func TestBucketAvailability_NoChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	lister := &fakeLister{}
	out, err := BucketAvailability(context.Background(), lister, "org", nil, time.Hour, time.Now(), 24, noHints())
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter(), "no query is issued when there are no checks")
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
				context.Background(), lister, "org", []string{"c1"}, 24*time.Hour, bucketStart, n, noHints(),
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
		context.Background(), lister, "org", []string{"c1", "c2"}, 24*time.Hour, bucketStart, n, noHints(),
	)
	r.NoError(err)

	r.Len(out["c2"], n, "c1's dense recent rows must not starve c2's older buckets out of the shared query")
	r.NotEmpty(out["c1"], "c1 itself must still be present")
}

// TestBucketAvailability_SafetyCapEngagesAndWarns is the pathological-scenario
// regression for the raw tier's safety cap (see rawRowCap): a lister returning far
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

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, currentHour, 1, noHints(),
	)
	r.NoError(err, "a capped, partial fetch must not error")

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw)

	wantLimit := rawRowCap(noHints(), 1, time.Hour)
	r.Less(wantLimit, pathologicalRowCount,
		"the cap must be smaller than the pathological row count for this test to be meaningful")
	r.Equal(wantLimit, raw.Limit, "the raw query is bounded by the raw-tier safety cap")

	r.NotEmpty(out["c1"], "a bucket with partial data is still returned, not an error and not empty")

	logged := logBuf.String()
	r.Contains(logged, "hit its safety row cap", "a warning must be logged when the cap engages")
	r.Contains(logged, "organization_uid=org", "the warning must include org context")
	r.Contains(logged, "check_uids=", "the warning must include check context")
}

// TestRawRowsPerHour pins the probe-rate measurement the raw cap is sized from:
// (1 hour / period) rows per region, summed over the checks, with a
// no-region check still counting once and a zero period ignored.
func TestRawRowsPerHour(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rate := func(period time.Duration, regions ...string) models.CheckRate {
		return models.CheckRate{Enabled: true, Period: timeutils.Duration(period), Regions: regions}
	}

	r.Equal(0, RawRowsPerHour(nil))
	r.Equal(60, RawRowsPerHour([]models.CheckRate{rate(time.Minute)}),
		"a 60s single-region check writes 60 rows an hour")
	r.Equal(180, RawRowsPerHour([]models.CheckRate{rate(time.Minute, "eu", "us", "ap")}),
		"a multi-region check runs once per region per period")
	r.Equal(360, RawRowsPerHour([]models.CheckRate{rate(10 * time.Second)}),
		"the platform's fastest allowed period")
	r.Equal(60, RawRowsPerHour([]models.CheckRate{rate(time.Minute), rate(0, "eu")}),
		"a zero period cannot be divided by and is skipped")

	// A disabled check still counts: it stopped writing, but the rows it already
	// wrote stay queryable until retention expires them.
	disabled := rate(time.Minute)
	disabled.Enabled = false
	r.Equal(60, RawRowsPerHour([]models.CheckRate{disabled}))
}

// TestRawRowCap_IsARealGuard is the spec §4 regression. The old combined cap
// produced LIMIT 884300 on the measured request — "large enough to be
// functionally unbounded, so it never protected anything". Both tiers must now
// land in a range that is genuinely bounded for a realistic deployment while
// still leaving generous headroom over the rows such a deployment actually has.
func TestRawRowCap_IsARealGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The configuration the spec measured: 5 checks, 30-day window, default 24h
	// raw retention — but with the org's REAL probe rate (a 60s period from two
	// regions), which is what the production call path now supplies.
	const (
		checks         = 5
		rowsPerHour    = checks * 60 * 2 // 60s period × 2 regions
		observedOldCap = 884_300
	)

	window := 30 * 24 * time.Hour
	hints := Hints{RetentionRawHours: 24, RetentionHourDays: 7, RawRowsPerHour: rowsPerHour}

	rawCap := rawRowCap(hints, checks, window)

	// Rows this deployment can actually have in the clamped 26h window.
	actual := 26 * rowsPerHour

	r.Greater(rawCap, actual*2, "the cap must keep generous headroom over the real row count")
	r.Less(rawCap, 100_000, "the cap must be a real bound, not a formality")
	r.Less(rawCap, observedOldCap/8,
		"the cap must be materially tighter than the LIMIT %d the spec measured", observedOldCap)

	// The rollup tier is bounded by buckets, not by probe rate.
	r.Less(rollupRowCap(hints, checks, window, false), 100_000)

	// A measured rate can only ever TIGHTEN the cap: it is capped by the
	// unmeasured worst case, so a bogus rate cannot loosen the query past what
	// the platform can physically produce.
	absurd := Hints{RetentionRawHours: 24, RawRowsPerHour: 1_000_000_000}
	r.Equal(rawRowCap(Hints{RetentionRawHours: 24}, checks, window),
		rawRowCap(absurd, checks, window),
		"an implausible rate falls back to the worst-case bound, never above it")

	// And with no measurement at all the conservative worst case still applies.
	r.Greater(rawRowCap(Hints{RetentionRawHours: 24}, checks, window), rawCap)
}

// TestMeasureRawRowsPerHour_ReadFailureFallsBack asserts the cap degrades to the
// conservative bound (not to "no bound") when the probe rate can't be read: a
// failed hint must never fail or unbound a render.
func TestMeasureRawRowsPerHour_ReadFailureFallsBack(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(0, MeasureRawRowsPerHour(context.Background(), failingRateLister{}, "org"))
}

type failingRateLister struct{}

func (failingRateLister) ListOrgCheckRates(
	_ context.Context, _ string,
) ([]models.CheckRate, error) {
	return nil, errRateListerBoom
}

var errRateListerBoom = errors.New("boom")
