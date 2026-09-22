package postgres

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Ports distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const (
	portResultBucketParity = 15532
	portResultBucketOrigin = 15533
)

// TestResultBucketExprMatchesGoTruncate_Postgres is the bucket-origin regression,
// the Postgres half of the sqlite package's test of the same name.
//
// uptimebar has always keyed its buckets on PeriodStart.Truncate(width), and
// time.Truncate rounds down relative to the ZERO time.Time (0001-01-01), not the
// Unix epoch. date_bin's origin argument is what makes the two grids coincide;
// binning against the epoch instead (or leaving the origin at date_bin's own
// default) agrees for every width that divides 24 h and silently disagrees for
// every width that does not — and the availability API accepts such widths.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestResultBucketExprMatchesGoTruncate_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResultBucketOrigin)

	// Deterministic pseudo-random timestamps spread over ~10 years, with
	// sub-second components: the bin must land on the same multiple of the width
	// as time.Truncate for each one.
	source := rand.New(rand.NewPCG(0x5011d, 0x9143))
	base := time.Date(2021, time.March, 3, 4, 5, 6, 0, time.UTC)

	const sampleCount = 300

	stamps := make([]time.Time, 0, sampleCount)
	for range sampleCount {
		offset := time.Duration(source.Int64N(int64(10 * 365 * 24 * time.Hour)))
		stamps = append(stamps, base.Add(offset).Add(time.Duration(source.Int64N(int64(time.Second)))))
	}

	for _, width := range []time.Duration{time.Hour, 24 * time.Hour, 7 * time.Hour, 90 * time.Minute} {
		for _, stamp := range stamps {
			var got time.Time

			// The PRODUCTION expression, with `result.period_start` bound as a
			// parameter so no fixture row is needed — the grid is a property of
			// the expression, not of the table.
			expr := strings.Replace(
				"SELECT "+resultBucketExpr, "result.period_start", "?::timestamptz", 1)

			r.NoError(s.db.NewRaw(expr, width.Seconds(), stamp).Scan(ctx, &got))

			want := stamp.UTC().Truncate(width)
			r.True(want.Equal(got.UTC()),
				"width %s: %s binned to %s, time.Truncate says %s",
				width, stamp, got.UTC(), want)
		}
	}

	// Negative control: the epoch-aligned grid — what date_bin's origin argument
	// exists to avoid — must really disagree for the non-dividing width.
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

// seedResultBucketFixture seeds the deliberately nasty mix the parity test folds
// two ways, and returns the rows it wrote. Mirrors the sqlite package's
// resultBucketFixture row for row.
//
// What is in here and why: raw rows across every status including the two
// lifecycle markers and the reaper's `abandoned` (all three must leave both
// numerator and denominator alone), some with NULL duration (they must not enter
// DurCnt or drag the minimum to zero), some tagged maintenance (a strict subset),
// two regions (so the Regions filter has something to bite on), plus hour and day
// rollups with NULL maintenance counters (a rollup written before the tagging
// shipped: "no evidence", not "zero"), one-sided extrema, and a total_checks = 0
// rollup carrying a duration_avg (the half of accumulateAgg's guard that is easy
// to drop in SQL).
func seedResultBucketFixture(
	ctx context.Context, t *testing.T, s *Service, orgUID, checkUID string, anchor time.Time,
) []*models.Result {
	t.Helper()

	r := require.New(t)

	regions := []string{"eu-1", "us-1"}
	rows := make([]*models.Result, 0, 64)

	dur := func(value float32) *float32 { return &value }
	count := func(value int) *int { return &value }

	raw := func(status models.ResultStatus, offset time.Duration, duration *float32,
		maintenance bool, region string,
	) {
		code := int(status)
		regionCopy := region
		row := models.NewResult(orgUID, checkUID, status, 0)
		row.PeriodStart = anchor.Add(offset)
		row.Region = &regionCopy
		row.Status = &code
		row.Duration = duration
		row.Maintenance = maintenance
		row.Metrics, row.Output = nil, nil
		r.NoError(s.CreateResult(ctx, row))
		rows = append(rows, row)
	}

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
		raw(status, time.Hour+time.Duration(i)*time.Minute, nil, i%4 == 0, region)
	}

	// A maintenance-tagged success and a maintenance-tagged failure, so MaintUp
	// is a strict subset of MaintTotal rather than equal to it. 1000 sits exactly
	// on the slow threshold (not slow), 1001 just above it.
	raw(models.ResultStatusUp, 42*time.Minute, dur(1000), true, regions[0])
	raw(models.ResultStatusDown, 43*time.Minute, dur(1001), true, regions[1])

	rollup := func(periodType string, offset time.Duration, total, success int,
		maintTotal, maintSuccess *int, durMin, durMax, durAvg *float32, region string,
	) {
		regionCopy := region
		totalCopy, successCopy := total, success
		row := models.NewResult(orgUID, checkUID, models.ResultStatusUp, 0)
		row.PeriodType = periodType
		row.PeriodStart = anchor.Add(offset)
		row.Region = &regionCopy
		row.Status = nil
		row.Duration = nil
		row.Metrics, row.Output = nil, nil
		row.TotalChecks, row.SuccessfulChecks = &totalCopy, &successCopy
		row.MaintenanceChecks, row.MaintenanceSuccessfulChecks = maintTotal, maintSuccess
		row.DurationMin, row.DurationMax, row.DurationAvg = durMin, durMax, durAvg
		r.NoError(s.CreateResult(ctx, row))
		rows = append(rows, row)
	}

	rollup(models.PeriodTypeHour, -3*time.Hour, 60, 59,
		count(10), count(9), dur(45), dur(2400), dur(210), regions[0])
	rollup(models.PeriodTypeHour, -3*time.Hour+time.Minute, 60, 60,
		nil, nil, dur(70), dur(320), dur(140), regions[1])
	rollup(models.PeriodTypeHour, -4*time.Hour, 60, 58,
		count(0), nil, dur(33), nil, dur(150), regions[0])
	rollup(models.PeriodTypeHour, -5*time.Hour, 60, 60,
		nil, count(0), nil, dur(5100), nil, regions[1])
	rollup(models.PeriodTypeHour, -6*time.Hour, 0, 0,
		nil, nil, nil, nil, dur(999), regions[0])
	rollup(models.PeriodTypeDay, -50*time.Hour, 1440, 1439,
		count(30), count(30), dur(40), dur(3300), dur(190), regions[0])
	rollup(models.PeriodTypeDay, -74*time.Hour, 1440, 1400,
		nil, nil, dur(38), dur(900), dur(205), regions[1])

	return rows
}

// referenceResultBuckets folds rows with the GO accumulators — the reference
// implementation the aggregate must reproduce — through uptimebar.StatsForResult,
// the very dispatch the read path used before the fold moved into SQL.
func referenceResultBuckets(
	rows []*models.Result, filter *models.ResultBucketFilter,
) map[string]map[time.Time]uptimebar.BucketStats {
	out := make(map[string]map[time.Time]uptimebar.BucketStats)

	for _, row := range rows {
		if !referenceResultBucketMatches(row, filter) {
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

// referenceResultBucketMatches is the aggregate's WHERE clause, in Go.
func referenceResultBucketMatches(row *models.Result, filter *models.ResultBucketFilter) bool {
	if !slices.Contains(filter.PeriodTypes, row.PeriodType) {
		return false
	}

	if len(filter.Regions) > 0 && (row.Region == nil || !slices.Contains(filter.Regions, *row.Region)) {
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
	// produces no group rather than an all-zero one. This does not weaken the
	// exclusion check — the fixture's excluded rows share buckets with countable
	// ones, so a predicate that failed to drop one inflates that bucket's Total.
	if row.PeriodType == models.PeriodTypeRaw && (row.Status == nil || row.ExcludedFromAvailability()) {
		return false
	}

	return true
}

// TestAggregateResultBuckets_ParityWithGoAccumulators_Postgres is the dialect
// half of the spec's correctness contract: the database's per-bucket counters
// must equal what uptimebar's accumulators produce for the same rows, at every
// width the callers use, with and without a region filter. The sqlite package
// asserts the same thing against the same fixture.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestAggregateResultBuckets_ParityWithGoAccumulators_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResultBucketParity)

	org := models.NewOrganization("bucket-parity-org", "Bucket Parity Org")
	r.NoError(s.CreateOrganization(ctx, org))

	first := models.NewCheck(org.UID, "bucket-parity", "http")
	r.NoError(s.CreateCheck(ctx, first))

	second := models.NewCheck(org.UID, "bucket-parity-2", "http")
	r.NoError(s.CreateCheck(ctx, second))

	anchor := time.Now().UTC().Truncate(time.Hour).Add(-8 * time.Hour)
	rows := seedResultBucketFixture(ctx, t, s, org.UID, first.UID, anchor)
	rows = append(rows,
		seedResultBucketFixture(ctx, t, s, org.UID, second.UID, anchor.Add(-30*time.Minute))...)

	checkUIDs := []string{first.UID, second.UID}
	since := anchor.Add(-200 * time.Hour)

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

				want := referenceResultBuckets(rows, filter)
				r.NotEmpty(want, "the fixture must produce buckets for %v at %s", periodTypes, width)

				buckets, err := s.AggregateResultBuckets(ctx, filter)
				r.NoError(err)

				requireSameResultBuckets(t, want, foldResultBuckets(buckets),
					width.String()+" "+periodTypes[0])
			}
		}
	}

	// And the mixed-tier guard holds at the dialect boundary, not only in models.
	mixed, err := s.AggregateResultBuckets(ctx, &models.ResultBucketFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour},
		PeriodStartAfter: since,
		BucketDuration:   time.Hour,
	})
	r.ErrorIs(err, models.ErrResultBucketsMixedTier)
	r.Nil(mixed)
}

// foldResultBuckets folds the aggregate's output the way uptimebar does, so the
// comparison is between two complete read paths rather than two field lists.
func foldResultBuckets(buckets []models.ResultBucket) map[string]map[time.Time]uptimebar.BucketStats {
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

// requireSameResultBuckets compares two folds. DurSum is compared with a
// tolerance because float addition is not associative and the two paths sum in
// different orders; every other field is an integer and is compared exactly.
func requireSameResultBuckets(
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

			r.InDelta(wantStats.DurSum, gotStats.DurSum, 0.001, "%s: bucket %s DurSum", label, bucket)

			wantStats.DurSum, gotStats.DurSum = 0, 0
			r.Equal(wantStats, gotStats, "%s: bucket %s", label, bucket)
		}
	}
}
