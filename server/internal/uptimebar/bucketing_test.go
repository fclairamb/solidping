package uptimebar

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeAggregator stands in for db.Service's AggregateResultBuckets, capturing
// EVERY filter it was called with (the availability engine issues one aggregate
// per tier side — see BucketAvailability).
//
// It STORES ROWS and folds them with the REAL Go accumulators
// (accumulateRaw/accumulateAgg), then packs the result into the wire shape. That
// is deliberate and it is the only honest way to fake this: a fake that
// hand-built the expected BucketStats would be a second implementation agreeing
// with the code by construction, and the package's scenario tests — warning
// counts as up, abandoned is excluded, maintenance is a subset, extrema fold
// one-sidedly — would stop exercising the counting rules they exist for. The
// real SQL's agreement with these same accumulators is pinned separately, per
// dialect, by the parity tests in internal/db/postgres and internal/db/sqlite.
//
// It honors the filter's tier list, region filter, time bounds and bucket width
// so a fixture row the query does not ask for is NOT returned — without that the
// raw clamp (which is what keeps the tiers disjoint) would be untestable. It also
// drops raw rows excluded from availability BEFORE grouping, exactly as the SQL's
// WHERE does, so a bucket whose only rows are lifecycle markers produces no group
// at all rather than an empty one.
type fakeAggregator struct {
	results    []*models.Result
	gotFilters []*models.ResultBucketFilter
}

// gotFilter returns the last captured filter, or nil when no query was issued.
func (f *fakeAggregator) gotFilter() *models.ResultBucketFilter {
	if len(f.gotFilters) == 0 {
		return nil
	}

	return f.gotFilters[len(f.gotFilters)-1]
}

// filterFor returns the captured filter whose PeriodTypes are exactly the given
// tiers, or nil when no such query was issued.
func (f *fakeAggregator) filterFor(periodTypes ...string) *models.ResultBucketFilter {
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

// fakeBucketKey is the aggregate's GROUP BY key.
type fakeBucketKey struct {
	checkUID    string
	bucketStart time.Time
}

// fakeBucketGroup is one group under construction: the folded stats plus the two
// things the aggregate reports about the source rows themselves.
type fakeBucketGroup struct {
	stats  BucketStats
	rows   int
	oldest time.Time
}

func (f *fakeAggregator) AggregateResultBuckets(
	_ context.Context, filter *models.ResultBucketFilter,
) ([]models.ResultBucket, error) {
	f.gotFilters = append(f.gotFilters, filter)

	// The real implementations validate before executing; a fake that didn't
	// would let a mixed-tier or zero-width filter pass unnoticed in tests.
	if err := filter.Validate(); err != nil {
		return nil, err
	}

	groups := make(map[fakeBucketKey]*fakeBucketGroup, len(f.results))
	order := make([]fakeBucketKey, 0, len(f.results))

	for _, row := range f.results {
		if !f.rowMatches(filter, row) {
			continue
		}

		key := fakeBucketKey{
			checkUID:    row.CheckUID,
			bucketStart: row.PeriodStart.UTC().Truncate(filter.BucketDuration),
		}

		group := groups[key]
		if group == nil {
			group = &fakeBucketGroup{oldest: row.PeriodStart.UTC()}
			groups[key] = group
			order = append(order, key)
		}

		if row.PeriodType == models.PeriodTypeRaw {
			group.stats.accumulateRaw(row)
		} else {
			group.stats.accumulateAgg(row)
		}

		group.rows++

		if row.PeriodStart.UTC().Before(group.oldest) {
			group.oldest = row.PeriodStart.UTC()
		}
	}

	buckets := make([]models.ResultBucket, 0, len(order))

	for _, key := range order {
		buckets = append(buckets, fakeBucket(key, groups[key]))
	}

	return buckets, nil
}

// rowMatches is the aggregate's WHERE clause.
func (f *fakeAggregator) rowMatches(filter *models.ResultBucketFilter, row *models.Result) bool {
	if !slices.Contains(filter.PeriodTypes, row.PeriodType) {
		return false
	}

	// Region fidelity matters for the same reason tier fidelity does: without
	// it a region-filtered read would fold every region's rows and the filter
	// would be untestable (see TestBucketAvailabilityInRegions).
	if len(filter.Regions) > 0 {
		if row.Region == nil || !slices.Contains(filter.Regions, *row.Region) {
			return false
		}
	}

	if row.PeriodStart.Before(filter.PeriodStartAfter) {
		return false
	}

	if filter.PeriodStartBefore != nil && !row.PeriodStart.Before(*filter.PeriodStartBefore) {
		return false
	}

	// `status NOT IN (created, running, abandoned)` on the raw tier: the SQL
	// drops these before grouping, so they cannot conjure an empty bucket.
	if row.PeriodType == models.PeriodTypeRaw && (row.Status == nil || row.ExcludedFromAvailability()) {
		return false
	}

	return true
}

// fakeBucket packs a folded group into the wire shape, carrying the extrema pair
// only when the group measured something.
func fakeBucket(key fakeBucketKey, group *fakeBucketGroup) models.ResultBucket {
	bucket := models.ResultBucket{
		CheckUID:          key.checkUID,
		BucketStart:       key.bucketStart,
		Total:             group.stats.Total,
		Up:                group.stats.Up,
		MaintTotal:        group.stats.MaintTotal,
		MaintUp:           group.stats.MaintUp,
		DurCnt:            group.stats.DurCnt,
		DurSum:            group.stats.DurSum,
		DurExtremaCnt:     group.stats.DurExtremaCnt,
		SlowSamples:       group.stats.SlowSamples,
		SlowPeaks:         group.stats.SlowPeaks,
		Rows:              group.rows,
		OldestPeriodStart: group.oldest,
	}

	if group.stats.DurExtremaCnt > 0 {
		durMin, durMax := group.stats.DurMin, group.stats.DurMax
		bucket.DurMin, bucket.DurMax = &durMin, &durMax
	}

	return bucket
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

// rawRowAbandoned builds a raw row the abandoned-result reaper finalized: the
// dedicated terminal models.ResultStatusAbandoned, which must behave like a
// lifecycle marker for availability purposes despite being terminal (specs
// 2026-08-18-03 / 2026-08-18-10; an `abandoned` boolean was drafted during the
// cycle and consolidated away before release).
func rawRowAbandoned(checkUID string, start time.Time) *models.Result {
	return rawRow(checkUID, models.ResultStatusAbandoned, start, 0)
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

	lister := &fakeAggregator{results: []*models.Result{
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

	lister := &fakeAggregator{results: []*models.Result{
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

	lister := &fakeAggregator{results: []*models.Result{
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

// TestBucketAvailability_AbandonedExcludedGenuineErrorCounts is the required
// positive-control test for spec 2026-08-18-03: a row the abandoned-result
// reaper finalized (models.ResultStatusAbandoned) must not move the bucket's
// availability at all — not numerator, not denominator — while a genuine
// ResultStatusError result in the SAME window still
// counts against it. Without the positive control, a fixture that never
// reached the calculation would trivially pass a "percentage unchanged"
// assertion.
func TestBucketAvailability_AbandonedExcludedGenuineErrorCounts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeAggregator{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(1*time.Minute), 40),
		rawRowAbandoned("c1", currentHour.Add(2*time.Minute)),
	}}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
	r.NoError(err)

	stats := out["c1"][currentHour]
	r.Equal(1, stats.Total, "the abandoned row must not enter the denominator")
	r.Equal(1, stats.Up, "the abandoned row must not enter the numerator either")
	pct, ok := stats.AvailabilityPct()
	r.True(ok)
	r.InDelta(100.0, pct, 0.01, "one up + one abandoned reads as 100%%, not 50%%")

	// Positive control: add a GENUINE ResultStatusError in the same window and
	// confirm it DOES move the percentage — proving the exclusion is specific
	// to ResultStatusAbandoned, not a blanket "errors don't count" bug.
	lister.results = append(lister.results, rawRow("c1", models.ResultStatusError, currentHour.Add(3*time.Minute), 0))
	lister.gotFilters = nil

	out, err = BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
	r.NoError(err)

	stats = out["c1"][currentHour]
	r.Equal(2, stats.Total, "the genuine error must enter the denominator")
	r.Equal(1, stats.Up, "the genuine error must not count as success")
	pct, ok = stats.AvailabilityPct()
	r.True(ok)
	r.InDelta(50.0, pct, 0.01, "the genuine error must drag availability down")
}

// TestBucketAvailability_EmptyBucketAbsent asserts a bucket with zero rows is
// absent from the inner map, so the caller knows to render noData.
func TestBucketAvailability_EmptyBucketAbsent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeAggregator{results: []*models.Result{
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
// bucketed independently from one batched aggregate per tier side.
func TestBucketAvailability_MultiCheckSingleQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	lister := &fakeAggregator{results: []*models.Result{
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

	for _, filter := range lister.gotFilters {
		r.Equal("org", filter.OrganizationUID)
		r.Equal([]string{"c1", "c2"}, filter.CheckUIDs)
		r.Equal(time.Hour, filter.BucketDuration,
			"the bucket width must be pushed into the query — that is what moves the fold "+
				"into the database")
		r.NoError(filter.Validate(),
			"every issued filter must be one the dialects will accept (single-sided tier, "+
				"positive width, bounded below)")
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

	lister := &fakeAggregator{}

	_, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, 24*time.Hour, bucketStart, n, hints(),
	)
	r.NoError(err)

	rollup := lister.filterFor(models.PeriodTypeHour, models.PeriodTypeDay)
	r.NotNil(rollup)
	r.True(rollup.PeriodStartAfter.Equal(bucketStart),
		"the rollup tiers cover the caller's full window")

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw)
	r.True(raw.PeriodStartAfter.After(bucketStart),
		"the raw tier must be clamped well inside a 30-day window")

	// 24h retention + the 2h margin, measured from now.
	r.WithinDuration(now.Add(-26*time.Hour), raw.PeriodStartAfter, time.Minute)
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

	lister := &fakeAggregator{results: []*models.Result{
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
//
// running concurrently with another test doing the same would corrupt both
// captured buffers (see the sibling tests below, same reasoning).
//
//nolint:paralleltest // swaps the process-wide slog default (global state);
func TestBucketAvailability_WarnsWhenAggregationLags(t *testing.T) {
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

	lister := &fakeAggregator{results: []*models.Result{
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
//
// TestBucketAvailability_WarnsWhenAggregationLags.
//
//nolint:paralleltest // swaps the process-wide slog default; see
func TestBucketAvailability_NoWarningWhenAggregationHealthy(t *testing.T) {
	r := require.New(t)

	var logBuf bytes.Buffer

	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))

	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-47 * time.Hour)

	lister := &fakeAggregator{results: []*models.Result{
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

	lister := &fakeAggregator{}
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

			lister := &fakeAggregator{results: results}

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

	lister := &fakeAggregator{results: results}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1", "c2"}, 24*time.Hour, bucketStart, n, noHints(),
	)
	r.NoError(err)

	r.Len(out["c2"], n, "c1's dense recent rows must not starve c2's older buckets out of the shared query")
	r.NotEmpty(out["c1"], "c1 itself must still be present")
}

// TestBucketAvailability_DenseRawIsNeverTruncated replaces the old
// TestBucketAvailability_SafetyCapEngagesAndWarns, and inverts its claim.
//
// The row path carried two per-tier safety row caps because the Go fold had to
// hold every matching row. When one engaged it logged "hit its safety row cap;
// returning partial data" and returned a WRONG availability percentage —
// silently, to a public status page. Aggregating in the database removes the
// reason for the cap: the statement returns one row per (check, bucket), so a
// pathological pile-up of raw (an aggregation job stalled for days) costs the
// database more work but can never truncate the answer.
//
// So: 20 000 raw rows in ONE bucket must all be counted, and nothing must warn
// about partial data.
//
//nolint:paralleltest // swaps the process-wide slog default (global state)
func TestBucketAvailability_DenseRawIsNeverTruncated(t *testing.T) {
	r := require.New(t)

	var logBuf bytes.Buffer

	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)

	const pathologicalRowCount = 20_000

	results := make([]*models.Result, 0, pathologicalRowCount)
	for i := range pathologicalRowCount {
		results = append(
			results,
			rawRow("c1", models.ResultStatusUp, currentHour.Add(time.Duration(i)*time.Millisecond), 40),
		)
	}

	lister := &fakeAggregator{results: results}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, currentHour, 1, noHints(),
	)
	r.NoError(err)

	stats := out["c1"][currentHour]
	r.Equal(pathologicalRowCount, stats.Total,
		"every probe must reach the bucket — a truncated answer used to be possible and was silent")
	r.Equal(pathologicalRowCount, stats.Up)

	raw := lister.filterFor(models.PeriodTypeRaw)
	r.NotNil(raw)

	r.NotContains(logBuf.String(), "safety row cap",
		"there is no row cap to hit any more, so nothing may claim partial data")
}

// TestStatsForBucket_CarriesEveryCounter pins the ONE translation between the
// database's per-bucket shape and BucketStats. A counter added to BucketStats
// and forgotten here would read as zero on every surface in the product, with
// nothing failing to compile.
func TestStatsForBucket_CarriesEveryCounter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	durMin, durMax := float32(20), float32(6000)
	stats := StatsForBucket(&models.ResultBucket{
		CheckUID:      "c1",
		Total:         100,
		Up:            95,
		MaintTotal:    10,
		MaintUp:       8,
		DurCnt:        90,
		DurSum:        27_000,
		DurMin:        &durMin,
		DurMax:        &durMax,
		DurExtremaCnt: 90,
		SlowSamples:   4,
		SlowPeaks:     2,
	})

	r.Equal(BucketStats{
		Up: 95, Total: 100,
		DurCnt: 90, DurSum: 27_000,
		MaintUp: 8, MaintTotal: 10,
		DurMin: 20, DurMax: 6000, DurExtremaCnt: 90,
		SlowSamples: 4, SlowPeaks: 2,
	}, stats)

	// A bucket that measured no duration must keep DurationRange()'s ok=false
	// rather than report a confident "0 ms to 0 ms".
	countsOnly := StatsForBucket(&models.ResultBucket{Total: 60, Up: 60})
	_, _, ok := countsOnly.DurationRange()
	r.False(ok)
	r.Zero(countsOnly.DurExtremaCnt)

	// And a bucket whose extrema count is positive but whose pair is missing
	// (nothing writes that, but a NULL from a hand-repaired row could) is
	// treated as unmeasured too, never as a zero minimum.
	orphanCount := StatsForBucket(&models.ResultBucket{Total: 60, Up: 60, DurExtremaCnt: 3})
	_, _, ok = orphanCount.DurationRange()
	r.False(ok)
}
