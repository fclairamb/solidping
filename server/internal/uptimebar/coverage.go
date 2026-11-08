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
var rollupCoverageTiers = []coverageTier{
	{periodTypes: []string{models.PeriodTypeHour}, bucket: time.Hour, length: fixedLength(time.Hour)},
	{periodTypes: []string{models.PeriodTypeDay}, bucket: 24 * time.Hour, length: fixedLength(24 * time.Hour)},
	// A month rollup starts at a UTC midnight, so the 24 h grid keeps one
	// bucket per month row.
	{periodTypes: []string{models.PeriodTypeMonth}, bucket: 24 * time.Hour, length: monthLength},
}

// rawCoverageTier bins raw results by the hour.
var rawCoverageTier = coverageTier{
	periodTypes: []string{models.PeriodTypeRaw}, bucket: time.Hour, length: fixedLength(time.Hour),
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
	out := make(map[string]WindowStats, len(checks))

	if len(checks) == 0 || !end.After(start) {
		return out, nil
	}

	startUTC := start.UTC()
	endUTC := end.UTC()
	byUID := make(map[string]*models.Check, len(checks))
	checkUIDs := make([]string, 0, len(checks))

	for _, check := range checks {
		if check == nil {
			continue
		}

		if _, seen := byUID[check.UID]; seen {
			continue
		}

		checkUIDs = append(checkUIDs, check.UID)
		byUID[check.UID] = check

		// The denominator covers the whole monitored range, buckets or not:
		// a bucket nobody measured is simply absent from the reads below.
		var acc WindowStats
		acc.Coverage.AddExpected(check.ExpectedPeriodsBetween(startUTC, endUTC, now))
		out[check.UID] = acc
	}

	if len(checkUIDs) == 0 {
		return out, nil
	}

	// Raw and hour rollups share the hourly grid: their counts are summed per
	// hour before the per-bucket cap, so an hour read half from each tier is
	// never credited twice.
	hourly := make(map[string]map[time.Time]int, len(checkUIDs))

	fold := func(tier coverageTier, buckets []models.ResultBucket) {
		for i := range buckets {
			bucket := &buckets[i]

			acc := out[bucket.CheckUID]
			acc.Stats.Add(StatsForBucket(bucket))

			if tier.bucket == time.Hour {
				byHour := hourly[bucket.CheckUID]
				if byHour == nil {
					byHour = make(map[time.Time]int)
					hourly[bucket.CheckUID] = byHour
				}

				byHour[bucket.BucketStart.UTC()] += bucket.Total
			} else if check := byUID[bucket.CheckUID]; check != nil {
				bucketStart := bucket.BucketStart.UTC()
				acc.Coverage.AddBucket(bucket.Total, bucketPeriods(
					check, bucketStart, bucketStart.Add(tier.length(bucketStart)), startUTC, endUTC, now))
			}

			out[bucket.CheckUID] = acc
		}
	}

	for _, tier := range rollupCoverageTiers {
		buckets, err := aggregateTier(ctx, db, &models.ResultBucketFilter{
			OrganizationUID:   orgUID,
			CheckUIDs:         checkUIDs,
			PeriodTypes:       tier.periodTypes,
			PeriodStartAfter:  startUTC,
			PeriodStartBefore: &endUTC,
			BucketDuration:    tier.bucket,
		}, callsiteWindowAvailability)
		if err != nil {
			return nil, err
		}

		fold(tier, buckets)
	}

	wallNow := time.Now().UTC()

	if rawStart := rawTierStart(startUTC, wallNow, hints.RetentionRawHours); rawStart.Before(endUTC) {
		buckets, err := aggregateTier(ctx, db, &models.ResultBucketFilter{
			OrganizationUID:   orgUID,
			CheckUIDs:         checkUIDs,
			PeriodTypes:       rawCoverageTier.periodTypes,
			PeriodStartAfter:  rawStart,
			PeriodStartBefore: &endUTC,
			BucketDuration:    rawCoverageTier.bucket,
		}, callsiteWindowAvailability)
		if err != nil {
			return nil, err
		}

		warnIfRawLagging(ctx, orgUID, buckets, wallNow, hints.RetentionRawHours)
		fold(rawCoverageTier, buckets)
	}

	for uid, byHour := range hourly {
		check := byUID[uid]
		if check == nil {
			continue
		}

		acc := out[uid]

		for hourStart, total := range byHour {
			acc.Coverage.AddBucket(total, bucketPeriods(
				check, hourStart, hourStart.Add(time.Hour), startUTC, endUTC, now))
		}

		out[uid] = acc
	}

	return out, nil
}

// bucketPeriods is the check's period count over [bucketStart, bucketEnd)
// clamped to the window [start, end) — ExpectedPeriodsBetween then clamps it
// to the check's lifetime.
func bucketPeriods(check *models.Check, bucketStart, bucketEnd, start, end, now time.Time) float64 {
	if bucketStart.Before(start) {
		bucketStart = start
	}

	if bucketEnd.After(end) {
		bucketEnd = end
	}

	return check.ExpectedPeriodsBetween(bucketStart, bucketEnd, now)
}
