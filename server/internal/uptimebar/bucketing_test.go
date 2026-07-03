package uptimebar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeLister returns a fixed result set, capturing the filter it was called with.
type fakeLister struct {
	results   []*models.Result
	gotFilter *models.ListResultsFilter
}

func (f *fakeLister) ListResults(
	_ context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	f.gotFilter = filter

	return &models.ListResultsResponse{Results: f.results}, nil
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

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24)
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

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24)
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

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24)
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

	out, err := BucketAvailability(context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24)
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
		context.Background(), lister, "org", []string{"c1", "c2"}, time.Hour, bucketStart, 24,
	)
	r.NoError(err)

	c1, _ := out["c1"][currentHour].AvailabilityPct()
	r.InDelta(100.0, c1, 0.0001)
	c2, _ := out["c2"][currentHour].AvailabilityPct()
	r.InDelta(0.0, c2, 0.0001)

	r.NotNil(lister.gotFilter)
	r.Equal(24*2, lister.gotFilter.Limit, "limit is windowBuckets × len(checkUIDs)")
	r.ElementsMatch(
		[]string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		lister.gotFilter.PeriodTypes,
		"the union query spans raw+hour+day",
	)
}

// TestBucketAvailability_NoChecks is a defensive guard for the empty-page case.
func TestBucketAvailability_NoChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	lister := &fakeLister{}
	out, err := BucketAvailability(context.Background(), lister, "org", nil, time.Hour, time.Now(), 24)
	r.NoError(err)
	r.Empty(out)
	r.Nil(lister.gotFilter, "no query is issued when there are no checks")
}
