package sqlite

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// resultBucketWidths are every bucket width the callers use, plus the case that
// makes the ORIGIN matter: 7 h does not divide 24 h, so an epoch-aligned grid and
// time.Truncate's proleptic-origin grid put the same row in different buckets.
// The availability API accepts any whole multiple of its minimum bucket
// (resolveBucket), so this is a real configuration, not a hypothetical.
var resultBucketWidths = []time.Duration{time.Hour, 24 * time.Hour, 7 * time.Hour, 90 * time.Minute}

// newResultBucketService spins up an in-memory database with one org and returns
// both.
func newResultBucketService(t *testing.T) (*Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("bucket-org", "Bucket Org")
	r.NoError(s.CreateOrganization(ctx, org))

	return s, org
}

// TestResultBucketExprMatchesGoTruncate is the bucket-origin regression.
//
// uptimebar has always keyed its buckets on PeriodStart.Truncate(width), and
// time.Truncate rounds down relative to the ZERO time.Time (0001-01-01), not the
// Unix epoch. Binning in SQL against the epoch — the obvious thing, and what
// `unixepoch(period_start) / width * width` does — agrees for every width that
// divides 24 h and silently disagrees for every width that does not. This feeds a
// few hundred random timestamps through the production expression and through
// time.Truncate and requires the same answer.
func TestResultBucketExprMatchesGoTruncate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org := newResultBucketService(t)

	check := models.NewCheck(org.UID, "bucket-origin", "http")
	r.NoError(s.CreateCheck(ctx, check))

	// Deterministic pseudo-random timestamps spread over ~10 years, including
	// sub-second components (the expression truncates to whole seconds, which
	// cannot change the bucket for any width of a minute or more).
	source := rand.New(rand.NewPCG(0x5011d, 0x9143)) //nolint:gosec // fixture spread, not security
	base := time.Date(2021, time.March, 3, 4, 5, 6, 0, time.UTC)

	const sampleCount = 300

	stamps := make([]time.Time, 0, sampleCount)

	for i := range sampleCount {
		offset := time.Duration(source.Int64N(int64(10 * 365 * 24 * time.Hour)))

		stamp := base.Add(offset).Add(time.Duration(source.Int64N(int64(time.Second))))
		stamps = append(stamps, stamp)

		row := models.NewResult(org.UID, check.UID, models.ResultStatusUp, float32(i))
		row.PeriodStart = stamp
		r.NoError(s.CreateResult(ctx, row))
	}

	for _, width := range resultBucketWidths {
		seconds := int64(width / time.Second)

		var rows []struct {
			PeriodStart time.Time `bun:"period_start"`
			BucketStart int64     `bun:"bucket_start"`
		}

		r.NoError(s.db.NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("result.period_start AS period_start").
			ColumnExpr(resultBucketExpr+" AS bucket_start", seconds, seconds).
			Where("result.organization_uid = ?", org.UID).
			Scan(ctx, &rows))

		// At least the seeded rows: creating the check writes a `created`
		// lifecycle marker of its own, and it must bin correctly too.
		r.GreaterOrEqual(len(rows), sampleCount, "every seeded row must come back (width %s)", width)

		for i := range rows {
			want := rows[i].PeriodStart.UTC().Truncate(width)
			got := time.Unix(rows[i].BucketStart, 0).UTC()

			r.True(want.Equal(got),
				"width %s: %s binned to %s, time.Truncate says %s",
				width, rows[i].PeriodStart.UTC(), got, want)
		}
	}

	// Negative control: the epoch-aligned grid — what this expression is NOT —
	// must actually DISAGREE with time.Truncate for the non-dividing width, or
	// the assertions above would pass on either implementation.
	disagreements := 0

	for _, stamp := range stamps {
		epochAligned := time.Unix(stamp.UTC().Unix()/(7*3600)*(7*3600), 0).UTC()
		if !epochAligned.Equal(stamp.UTC().Truncate(7 * time.Hour)) {
			disagreements++
		}
	}

	r.Positive(disagreements,
		"an epoch-aligned 7h grid must differ from time.Truncate for some of these stamps, "+
			"otherwise the origin is untested")
}

// resultBucketFixture seeds the deliberately nasty mix the parity test folds two
// ways, and returns the rows it wrote.
//
// What is in here and why: raw rows across every status including the two
// lifecycle markers and the reaper's `abandoned` (all three must leave both
// numerator and denominator alone), some with NULL duration (they must not enter
// DurCnt or drag the minimum to zero), some tagged maintenance (a strict subset,
// never subtracted here), two regions (so the Regions filter has something to
// bite on), plus hour and day rollups with NULL maintenance counters (a rollup
// written before the tagging shipped: "no evidence", not "zero") and one-sided
// extrema (only duration_min, or only duration_max — the switch in accumulateAgg
// that folds the one it has against itself).
func resultBucketFixture(
	t *testing.T, s *Service, orgUID, checkUID string, anchor time.Time,
) []*models.Result {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	regions := []string{"eu-1", "us-1"}
	rows := make([]*models.Result, 0, 64)

	raw := func(status models.ResultStatus, offset time.Duration, duration *float32,
		maintenance bool, region string,
	) {
		code := int(status)
		regionCopy := region
		row := &models.Result{
			UID:             models.NewResult(orgUID, checkUID, status, 0).UID,
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			PeriodType:      models.PeriodTypeRaw,
			PeriodStart:     anchor.Add(offset),
			Region:          &regionCopy,
			Status:          &code,
			Duration:        duration,
			Maintenance:     maintenance,
		}
		r.NoError(s.CreateResult(ctx, row))
		rows = append(rows, row)
	}

	dur := func(value float32) *float32 { return &value }

	// Spread across two hours so a 1 h width really splits them.
	for i, status := range []models.ResultStatus{
		models.ResultStatusUp,
		models.ResultStatusWarning,
		models.ResultStatusDown,
		models.ResultStatusTimeout,
		models.ResultStatusError,
		models.ResultStatusCreated,
		models.ResultStatusRunning,
		models.ResultStatusAbandoned,
	} {
		region := regions[i%len(regions)]

		raw(status, time.Duration(i)*time.Minute, dur(float32(90+i*370)), i%3 == 0, region)
		// Same status one hour later, with no duration at all.
		raw(status, time.Hour+time.Duration(i)*time.Minute, nil, i%4 == 0, region)
	}

	// A maintenance-tagged success and a maintenance-tagged failure, so MaintUp
	// is a strict subset of MaintTotal rather than equal to it.
	raw(models.ResultStatusUp, 42*time.Minute, dur(1000), true, regions[0])
	raw(models.ResultStatusDown, 43*time.Minute, dur(1001), true, regions[1])

	rollup := func(periodType string, offset time.Duration, total, success int,
		maintTotal, maintSuccess *int, durMin, durMax, durAvg *float32, region string,
	) {
		regionCopy := region
		totalCopy, successCopy := total, success
		row := &models.Result{
			UID:                         models.NewResult(orgUID, checkUID, models.ResultStatusUp, 0).UID,
			OrganizationUID:             orgUID,
			CheckUID:                    checkUID,
			PeriodType:                  periodType,
			PeriodStart:                 anchor.Add(offset),
			Region:                      &regionCopy,
			TotalChecks:                 &totalCopy,
			SuccessfulChecks:            &successCopy,
			MaintenanceChecks:           maintTotal,
			MaintenanceSuccessfulChecks: maintSuccess,
			DurationMin:                 durMin,
			DurationMax:                 durMax,
			DurationAvg:                 durAvg,
		}
		r.NoError(s.CreateResult(ctx, row))
		rows = append(rows, row)
	}

	count := func(value int) *int { return &value }

	// Hour rollups, one per region, in a bucket of their own.
	rollup(models.PeriodTypeHour, -3*time.Hour, 60, 59,
		count(10), count(9), dur(45), dur(2400), dur(210), regions[0])
	rollup(models.PeriodTypeHour, -3*time.Hour+time.Minute, 60, 60,
		nil, nil, dur(70), dur(320), dur(140), regions[1])
	// Only duration_min.
	rollup(models.PeriodTypeHour, -4*time.Hour, 60, 58,
		count(0), nil, dur(33), nil, dur(150), regions[0])
	// Only duration_max, and past the slow threshold so it is a peak.
	rollup(models.PeriodTypeHour, -5*time.Hour, 60, 60,
		nil, count(0), nil, dur(5100), nil, regions[1])
	// total_checks = 0 with a duration_avg present: contributes to neither
	// DurCnt nor DurSum (the `*TotalChecks > 0` half of accumulateAgg's guard).
	rollup(models.PeriodTypeHour, -6*time.Hour, 0, 0,
		nil, nil, nil, nil, dur(999), regions[0])
	// Day rollups, far enough back to land in their own daily bucket too.
	rollup(models.PeriodTypeDay, -50*time.Hour, 1440, 1439,
		count(30), count(30), dur(40), dur(3300), dur(190), regions[0])
	rollup(models.PeriodTypeDay, -74*time.Hour, 1440, 1400,
		nil, nil, dur(38), dur(900), dur(205), regions[1])

	return rows
}

// referenceBuckets folds rows with the GO accumulators — the reference
// implementation the aggregate must reproduce. It routes every row through
// uptimebar.StatsForResult, which is the same dispatch the read path used before
// this moved into SQL.
func referenceBuckets(
	rows []*models.Result, filter *models.ResultBucketFilter,
) map[string]map[time.Time]uptimebar.BucketStats {
	out := make(map[string]map[time.Time]uptimebar.BucketStats)

	for _, row := range rows {
		if !referenceMatches(row, filter) {
			continue
		}

		bucket := row.PeriodStart.UTC().Truncate(filter.BucketDuration)

		byBucket := out[row.CheckUID]
		if byBucket == nil {
			byBucket = make(map[time.Time]uptimebar.BucketStats)
			out[row.CheckUID] = byBucket
		}

		acc := byBucket[bucket]
		acc.Add(uptimebar.StatsForResult(row))
		byBucket[bucket] = acc
	}

	return out
}

// referenceMatches is the filter, applied in Go.
func referenceMatches(row *models.Result, filter *models.ResultBucketFilter) bool {
	if !containsString(filter.PeriodTypes, row.PeriodType) {
		return false
	}

	if len(filter.Regions) > 0 && (row.Region == nil || !containsString(filter.Regions, *row.Region)) {
		return false
	}

	if row.PeriodStart.Before(filter.PeriodStartAfter) {
		return false
	}

	if filter.PeriodStartBefore != nil && !row.PeriodStart.Before(*filter.PeriodStartBefore) {
		return false
	}

	// The raw tier's `status NOT IN (created, running, abandoned)`: the aggregate
	// drops these before GROUPing, so a bucket whose only rows are excluded
	// produces no group at all rather than an all-zero one. Both read as "no
	// data" downstream (AvailabilityPct reports ok=false either way), but the
	// comparison has to agree on which it is.
	//
	// This does NOT weaken the exclusion check: the fixture's created/running/
	// abandoned rows sit in buckets that also hold countable rows, so a SQL
	// predicate that failed to drop one would inflate that bucket's Total.
	if row.PeriodType == models.PeriodTypeRaw && (row.Status == nil || row.ExcludedFromAvailability()) {
		return false
	}

	return true
}

func containsString(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}

	return false
}

// aggregatedBuckets runs the production aggregate and folds its output the way
// uptimebar does, so the comparison is between two complete read paths rather
// than between two field lists.
func aggregatedBuckets(
	ctx context.Context, t *testing.T, s *Service, filter *models.ResultBucketFilter,
) map[string]map[time.Time]uptimebar.BucketStats {
	t.Helper()

	buckets, err := s.AggregateResultBuckets(ctx, filter)
	require.NoError(t, err)

	out := make(map[string]map[time.Time]uptimebar.BucketStats)

	for i := range buckets {
		bucket := &buckets[i]

		byBucket := out[bucket.CheckUID]
		if byBucket == nil {
			byBucket = make(map[time.Time]uptimebar.BucketStats)
			out[bucket.CheckUID] = byBucket
		}

		acc := byBucket[bucket.BucketStart]
		acc.Add(uptimebar.StatsForBucket(bucket))
		byBucket[bucket.BucketStart] = acc
	}

	return out
}

// requireSameBuckets compares two folds. DurSum is compared with a tolerance
// because float addition is not associative and the two paths sum in different
// orders; every other field is an integer and is compared exactly.
func requireSameBuckets(
	t *testing.T, want, got map[string]map[time.Time]uptimebar.BucketStats, label string,
) {
	t.Helper()

	r := require.New(t)

	r.Len(got, len(want), "%s: different set of checks", label)

	for checkUID, wantByBucket := range want {
		gotByBucket, ok := got[checkUID]
		r.True(ok, "%s: check %s missing from the aggregate", label, checkUID)
		r.Len(gotByBucket, len(wantByBucket), "%s: check %s has a different bucket count", label, checkUID)

		for bucket, wantStats := range wantByBucket {
			gotStats, ok := gotByBucket[bucket]
			r.True(ok, "%s: bucket %s missing for check %s", label, bucket, checkUID)

			r.InDelta(wantStats.DurSum, gotStats.DurSum, 0.001,
				"%s: bucket %s DurSum", label, bucket)

			wantStats.DurSum, gotStats.DurSum = 0, 0
			r.Equal(wantStats, gotStats, "%s: bucket %s", label, bucket)
		}
	}
}

// TestAggregateResultBuckets_ParityWithGoAccumulators is the correctness contract
// for the whole spec: the database's per-bucket counters must equal what
// uptimebar's accumulators produce for the same rows, for every width the callers
// use and with or without a region filter.
//
// The fixture is deliberately nasty (see resultBucketFixture), and the reference
// side folds through uptimebar.StatsForResult — the very code the read path used
// before the fold moved into SQL — so this cannot pass by both sides sharing a
// bug.
func TestAggregateResultBuckets_ParityWithGoAccumulators(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org := newResultBucketService(t)

	check := models.NewCheck(org.UID, "bucket-parity", "http")
	r.NoError(s.CreateCheck(ctx, check))

	second := models.NewCheck(org.UID, "bucket-parity-2", "http")
	r.NoError(s.CreateCheck(ctx, second))

	anchor := time.Now().UTC().Truncate(time.Hour).Add(-8 * time.Hour)
	rows := resultBucketFixture(t, s, org.UID, check.UID, anchor)
	rows = append(rows, resultBucketFixture(t, s, org.UID, second.UID, anchor.Add(-30*time.Minute))...)

	since := anchor.Add(-200 * time.Hour)
	checkUIDs := []string{check.UID, second.UID}

	for _, width := range []time.Duration{time.Hour, 24 * time.Hour} {
		for _, regions := range [][]string{nil, {"eu-1"}} {
			for _, periodTypes := range [][]string{
				{models.PeriodTypeRaw},
				{models.PeriodTypeHour, models.PeriodTypeDay},
			} {
				filter := &models.ResultBucketFilter{
					OrganizationUID:  org.UID,
					CheckUIDs:        checkUIDs,
					Regions:          regions,
					PeriodTypes:      periodTypes,
					PeriodStartAfter: since,
					BucketDuration:   width,
				}

				want := referenceBuckets(rows, filter)
				r.NotEmpty(want, "the fixture must produce buckets for %v at %s", periodTypes, width)

				requireSameBuckets(t, want, aggregatedBuckets(ctx, t, s, filter),
					fmtLabel(width, regions, periodTypes))
			}
		}
	}
}

// fmtLabel names one parity case for assertion messages.
func fmtLabel(width time.Duration, regions, periodTypes []string) string {
	label := width.String() + " " + periodTypes[0]
	if len(regions) > 0 {
		label += " region=" + regions[0]
	}

	return label
}

// TestAggregateResultBuckets_RejectsMixedTier pins the guard at the dialect
// boundary, not only in models: a filter straddling the raw/rollup split can only
// be answered by a sequential scan of the largest table in the system, so it must
// never execute.
func TestAggregateResultBuckets_RejectsMixedTier(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org := newResultBucketService(t)

	check := models.NewCheck(org.UID, "bucket-mixed", "http")
	r.NoError(s.CreateCheck(ctx, check))

	filter := &models.ResultBucketFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour},
		PeriodStartAfter: time.Now().UTC().Add(-time.Hour),
		BucketDuration:   time.Hour,
	}

	buckets, err := s.AggregateResultBuckets(ctx, filter)
	r.ErrorIs(err, models.ErrResultBucketsMixedTier)
	r.Nil(buckets)

	// An empty tier list constrains nothing and is mixed for the same reason.
	filter.PeriodTypes = nil
	_, err = s.AggregateResultBuckets(ctx, filter)
	r.ErrorIs(err, models.ErrResultBucketsMixedTier)

	// A zero bucket width has no grid to group by.
	filter.PeriodTypes = []string{models.PeriodTypeRaw}
	filter.BucketDuration = 0
	_, err = s.AggregateResultBuckets(ctx, filter)
	r.ErrorIs(err, models.ErrResultBucketsNoBucketDuration)

	// And an unbounded aggregate is the scan this shape exists to avoid.
	filter.BucketDuration = time.Hour
	filter.PeriodStartAfter = time.Time{}
	_, err = s.AggregateResultBuckets(ctx, filter)
	r.ErrorIs(err, models.ErrResultBucketsNoPeriodStart)
}
