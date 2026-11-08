package uptimebar

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// WindowStats is one check's whole-window fold plus its data coverage.
type WindowStats struct {
	// Stats is exactly what WindowAvailability returns for the check.
	Stats BucketStats
	// Coverage is the region-independent coverage tally over the check's
	// monitored part of the window (spec 2026-11-08-05).
	Coverage models.CoverageTally
}

// coverageTier is one tier of the per-bucket coverage read: the period types
// it folds, the bucket width it is grouped by, and the wall-clock length of
// one of its buckets.
type coverageTier struct {
	periodTypes []string
	bucket      time.Duration
	length      func(bucketStart time.Time) time.Duration
}

func fixedLength(d time.Duration) func(time.Time) time.Duration {
	return func(time.Time) time.Duration { return d }
}

func monthLength(bucketStart time.Time) time.Duration {
	return bucketStart.AddDate(0, 1, 0).Sub(bucketStart)
}

// rollupCoverageTiers reads every rollup tier on its own grid. They cannot
// share one query: a day and a month rollup both start at midnight, so a
// shared grid could not tell their lengths apart, and the length is what the
// bucket's expected period count is made of.
func rollupCoverageTiers() []coverageTier {
	return []coverageTier{
		{periodTypes: []string{models.PeriodTypeHour}, bucket: time.Hour, length: fixedLength(time.Hour)},
		{periodTypes: []string{models.PeriodTypeDay}, bucket: 24 * time.Hour, length: fixedLength(24 * time.Hour)},
		// A month rollup starts at a UTC midnight, so the 24 h grid keeps one
		// bucket per month row.
		{periodTypes: []string{models.PeriodTypeMonth}, bucket: 24 * time.Hour, length: monthLength},
	}
}

// rawCoverageTier bins raw results by the hour.
func rawCoverageTier() coverageTier {
	return coverageTier{
		periodTypes: []string{models.PeriodTypeRaw}, bucket: time.Hour, length: fixedLength(time.Hour),
	}
}

// WindowAvailabilityWithCoverage is WindowAvailability (same tiers, same
// bounds, same fold: Stats is identical) plus the region-independent data
// coverage of each check over [start, end), clamped to [createdAt, now)
// (spec 2026-11-08-05).
//
// Unmeasured time is wall-clock time where NO region produced a result. The
// window is read at the granularity each tier already has — raw and hour
// rollups by the hour, day rollups by the day, month rollups by the month —
// and every bucket that came back counts as measured in proportion to
// min(results, ⌈bucket length ÷ period⌉), with no region multiplier (see
// models.CoverageTally). A check whose region count changed mid-window thus
// reads fully measured as long as every period had a result from some region,
// and a gap inside a day or month rollup is only seen proportionally (the
// accepted rollup granularity: no per-period gap table).
//
// It costs four tier-aligned aggregates (raw, hour, day, month) instead of
// WindowAvailability's two, each answered by its partial index; the rows they
// return stay bounded by checks × buckets.
func WindowAvailabilityWithCoverage(
	ctx context.Context, db ResultBucketAggregator, orgUID string, checks []*models.Check,
	start, end, now time.Time, hints Hints,
) (map[string]WindowStats, error) {
	if len(checks) == 0 || !end.After(start) {
		return map[string]WindowStats{}, nil
	}

	fold := newCoverageFold(checks, start.UTC(), end.UTC(), now)
	if len(fold.checkUIDs) == 0 {
		return fold.out, nil
	}

	rollups := rollupCoverageTiers()
	for i := range rollups {
		buckets, err := fold.read(ctx, db, orgUID, &rollups[i], fold.start)
		if err != nil {
			return nil, err
		}

		fold.add(&rollups[i], buckets)
	}

	wallNow := time.Now().UTC()

	if rawStart := rawTierStart(fold.start, wallNow, hints.RetentionRawHours); rawStart.Before(fold.end) {
		raw := rawCoverageTier()

		buckets, err := fold.read(ctx, db, orgUID, &raw, rawStart)
		if err != nil {
			return nil, err
		}

		warnIfRawLagging(ctx, orgUID, buckets, wallNow, hints.RetentionRawHours)
		fold.add(&raw, buckets)
	}

	fold.closeHours()

	return fold.out, nil
}

// coverageFold accumulates WindowAvailabilityWithCoverage's reads.
type coverageFold struct {
	start, end, now time.Time
	byUID           map[string]*models.Check
	checkUIDs       []string
	out             map[string]WindowStats
	// hourly sums raw and hour-rollup counts per hour before the per-bucket
	// cap, so an hour read half from each tier is never credited twice.
	hourly map[string]map[time.Time]int
}

func newCoverageFold(checks []*models.Check, start, end, now time.Time) *coverageFold {
	fold := &coverageFold{
		start:     start,
		end:       end,
		now:       now,
		byUID:     make(map[string]*models.Check, len(checks)),
		checkUIDs: make([]string, 0, len(checks)),
		out:       make(map[string]WindowStats, len(checks)),
		hourly:    make(map[string]map[time.Time]int, len(checks)),
	}

	for _, check := range checks {
		if check == nil {
			continue
		}

		if _, seen := fold.byUID[check.UID]; seen {
			continue
		}

		fold.checkUIDs = append(fold.checkUIDs, check.UID)
		fold.byUID[check.UID] = check

		// The denominator covers the whole monitored range, buckets or not:
		// a bucket nobody measured is simply absent from the reads.
		var acc WindowStats

		acc.Coverage.AddExpected(check.ExpectedPeriodsBetween(start, end, now))
		fold.out[check.UID] = acc
	}

	return fold
}

// read runs one tier's aggregate from `from` to the window end.
func (f *coverageFold) read(
	ctx context.Context, db ResultBucketAggregator, orgUID string, tier *coverageTier, from time.Time,
) ([]models.ResultBucket, error) {
	end := f.end

	return aggregateTier(ctx, db, &models.ResultBucketFilter{
		OrganizationUID:   orgUID,
		CheckUIDs:         f.checkUIDs,
		PeriodTypes:       tier.periodTypes,
		PeriodStartAfter:  from,
		PeriodStartBefore: &end,
		BucketDuration:    tier.bucket,
	}, callsiteWindowAvailability)
}

// add folds one tier's buckets into the stats and the coverage tallies.
func (f *coverageFold) add(tier *coverageTier, buckets []models.ResultBucket) {
	for i := range buckets {
		bucket := &buckets[i]
		bucketStart := bucket.BucketStart.UTC()

		acc := f.out[bucket.CheckUID]
		acc.Stats.Add(StatsForBucket(bucket))

		switch check := f.byUID[bucket.CheckUID]; {
		case tier.bucket == time.Hour:
			byHour := f.hourly[bucket.CheckUID]
			if byHour == nil {
				byHour = make(map[time.Time]int)
				f.hourly[bucket.CheckUID] = byHour
			}

			byHour[bucketStart] += bucket.Total
		case check != nil:
			acc.Coverage.AddBucket(bucket.Total, f.bucketPeriods(
				check, bucketStart, bucketStart.Add(tier.length(bucketStart))))
		}

		f.out[bucket.CheckUID] = acc
	}
}

// closeHours credits the hourly buckets once raw and hour rollups are summed.
func (f *coverageFold) closeHours() {
	for uid, byHour := range f.hourly {
		check := f.byUID[uid]
		if check == nil {
			continue
		}

		acc := f.out[uid]

		for hourStart, total := range byHour {
			acc.Coverage.AddBucket(total, f.bucketPeriods(check, hourStart, hourStart.Add(time.Hour)))
		}

		f.out[uid] = acc
	}
}

// bucketPeriods is the check's period count over [bucketStart, bucketEnd)
// clamped to the window; ExpectedPeriodsBetween then clamps it to the check's
// lifetime.
func (f *coverageFold) bucketPeriods(check *models.Check, bucketStart, bucketEnd time.Time) float64 {
	if bucketStart.Before(f.start) {
		bucketStart = f.start
	}

	if bucketEnd.After(f.end) {
		bucketEnd = f.end
	}

	return check.ExpectedPeriodsBetween(bucketStart, bucketEnd, f.now)
}
