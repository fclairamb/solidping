package uptimebar

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// WindowAvailability runs two tier-aligned aggregates over [start, end) for all
// checks and accumulates everything into a single BucketStats per check. Unlike
// BucketAvailability (which keys per time-bucket for a tick strip), this folds
// the whole window into one aggregate — exactly what a per-period availability
// number needs.
//
// It asks the database for buckets one window-span wide and folds whatever comes
// back, rather than for "one bucket". That is exact, not an approximation: the
// bucket grid partitions the matched rows, and every BucketStats field is an
// associative fold (sums, plus a min/max pair that BucketStats.Add merges while
// carrying the real contribution count), so folding buckets gives the same answer
// as folding rows. A span not aligned to the grid simply yields two buckets.
//
// The split is the same as BucketAvailability's and for the same reason (see the
// package comment): rollups (hour+day+month) over the full window, plus raw
// clamped to max(start, now-(RetentionRaw+rawClampMargin)). Asking for raw and
// rollups in one `period_type IN (...)` predicate matches neither partial index
// on `results` and forces a full sequential scan — measured at ~1.5 s to return
// 19 rows for a 1 h single-check window (spec 2026-08-17-03).
//
// Because the aggregation job deletes source rows after each rollup (raw → hour →
// day → month), the four tiers cover non-overlapping age bands, so unioning them
// never double-counts. The month tier is terminal — never rolled further, never
// deleted — so the union covers the check's entire history regardless of how the
// raw/hour/day retention is tuned (the live defaults keep only 24 h / 7 d / 2 mo;
// see jobtypes' defaultRetention* constants). Without month in the union, a 365d
// window on a default deployment silently saw only ~2 months of data.
//
// hints bound the raw clamp; the zero value falls back to the documented default
// (see Hints).
//
// The function holds no shared mutable state, so several windows may be computed
// concurrently against the same Service.
//
// Edge granularity: a rollup row is included iff its period_start falls inside
// [start, end) — the same rule for every tier — so a rollup straddling the
// window's left edge is excluded even though part of its span is wanted. For the
// month tier that means up to a month of the oldest edge may be missing from a
// duration window; calendar windows (mtd/ytd) start on month boundaries and are
// exact.
//
// Counting rules stay canonical: raw rows go through accumulateRaw (lifecycle
// markers excluded; up + warning count as success) and rollup rows through
// accumulateAgg (SuccessfulChecks already encodes the rule). A check absent from
// the returned map (or with BucketStats.Total == 0) had no data in the window —
// the caller renders that as "no data", not "100%".
func WindowAvailability(
	ctx context.Context, db ResultBucketAggregator, orgUID string, checkUIDs []string,
	start, end time.Time, hints Hints,
) (map[string]BucketStats, error) {
	return WindowAvailabilityInRegions(ctx, db, orgUID, checkUIDs, nil, start, end, hints)
}

// WindowAvailabilityInRegions is WindowAvailability restricted to a set of
// probe regions — see BucketAvailabilityInRegions for why the filter is pushed
// into the query and what nil/empty means (every region, summed rather than
// averaged).
func WindowAvailabilityInRegions(
	ctx context.Context, db ResultBucketAggregator, orgUID string, checkUIDs, regions []string,
	start, end time.Time, hints Hints,
) (map[string]BucketStats, error) {
	out := make(map[string]BucketStats, len(checkUIDs))

	if len(checkUIDs) == 0 || !end.After(start) {
		return out, nil
	}

	startUTC := start.UTC()
	endUTC := end.UTC()
	now := time.Now().UTC()
	windowSpan := endUTC.Sub(startUTC)

	// Rollup tiers over the whole window, answered by results_aggregated_idx.
	// PeriodStartBefore bounds the upper edge to [start, end).
	rollupBuckets, err := aggregateTier(ctx, db, &models.ResultBucketFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		Regions:         regions,
		PeriodTypes: []string{
			models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth,
		},
		PeriodStartAfter:  startUTC,
		PeriodStartBefore: &endUTC,
		BucketDuration:    windowSpan,
	}, callsiteWindowAvailability)
	if err != nil {
		return nil, err
	}

	// Raw tier, clamped to the raw-retention band, answered by results_raw_idx.
	rawStart := rawTierStart(startUTC, now, hints.RetentionRawHours)

	var rawBuckets []models.ResultBucket

	if rawStart.Before(endUTC) {
		rawBuckets, err = aggregateTier(ctx, db, &models.ResultBucketFilter{
			OrganizationUID:   orgUID,
			CheckUIDs:         checkUIDs,
			Regions:           regions,
			PeriodTypes:       []string{models.PeriodTypeRaw},
			PeriodStartAfter:  rawStart,
			PeriodStartBefore: &endUTC,
			BucketDuration:    windowSpan,
		}, callsiteWindowAvailability)
		if err != nil {
			return nil, err
		}

		warnIfRawLagging(ctx, orgUID, rawBuckets, now, hints.RetentionRawHours)
	}

	for _, buckets := range [][]models.ResultBucket{rollupBuckets, rawBuckets} {
		for i := range buckets {
			bucket := &buckets[i]

			acc := out[bucket.CheckUID]
			acc.Add(StatsForBucket(bucket))
			out[bucket.CheckUID] = acc
		}
	}

	return out, nil
}
