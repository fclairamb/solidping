// Package uptimebar is the single shared source for per-bucket availability used
// by both the public status page and the badge uptime bar. Both surfaces render a
// strip of "ticks" (24 hourly buckets for 24h, N daily buckets for 7d/30d/90d).
// They MUST bucket from the same data so they never disagree for the same check +
// period.
//
// The core is BucketAvailability: one query over a raw+hour+day union for the
// whole window. Because the aggregation job deletes source rows after each rollup
// (raw → hour → day → month), the three tiers cover non-overlapping age bands, so
// unioning them never double-counts. A bucket whose raw rows haven't been rolled
// up yet is still filled immediately from raw — this is what fixes status-page
// buckets reading "No data" while the badge showed data.
package uptimebar

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ResultsLister is the minimal db surface both services already satisfy
// (db.Service implements it). Keeping the dependency this small makes uptimebar a
// leaf package: it depends only on models, with no import cycle against the
// badges or statuspages handlers.
type ResultsLister interface {
	ListResults(ctx context.Context, filter *models.ListResultsFilter) (*models.ListResultsResponse, error)
}

// BucketStats accumulates availability and duration stats for one bucket across
// multiple result rows (potentially from different period types — raw + rollup).
type BucketStats struct {
	Up     int     // successful checks (raw: CountsAsUp rows; rollup: SuccessfulChecks)
	Total  int     // countable checks (raw: non-lifecycle rows; rollup: TotalChecks)
	DurCnt int     // number of checks contributing to DurSum
	DurSum float64 // sum of durations (weighted by check count for rollups)
}

// AvailabilityPct returns up/total*100 and ok=true when the bucket has any
// countable check. ok=false means the bucket is empty and the caller should
// render it as "no data".
func (b BucketStats) AvailabilityPct() (float64, bool) {
	if b.Total == 0 {
		return 0, false
	}

	return float64(b.Up) / float64(b.Total) * 100, true
}

// AvgDuration returns the average duration over contributing checks and ok=true
// when at least one check carried a duration.
func (b BucketStats) AvgDuration() (float64, bool) {
	if b.DurCnt == 0 {
		return 0, false
	}

	return b.DurSum / float64(b.DurCnt), true
}

// accumulateRaw merges a raw result row into the bucket. Lifecycle markers
// (created/running) are excluded from the denominator; up + warning count as
// success — the canonical models.RawAvailability / CountsAsUp rule, which also
// matches the aggregation job and the status page. This is the single point where
// the "warning counts as up" rule lives for the raw tier.
func (b *BucketStats) accumulateRaw(result *models.Result) {
	if result.Status == nil {
		return
	}

	status := models.ResultStatus(*result.Status)
	if status.IsLifecycleMarker() {
		return
	}

	b.Total++

	if status.CountsAsUp() {
		b.Up++
	}

	if result.Duration != nil {
		b.DurSum += float64(*result.Duration)
		b.DurCnt++
	}
}

// accumulateAgg merges an hour/day aggregated rollup row into the bucket. Rollup
// rows already encode the CountsAsUp rule in SuccessfulChecks (the aggregation
// job counts warning as up), so this path needs no per-status logic.
func (b *BucketStats) accumulateAgg(result *models.Result) {
	if result.TotalChecks != nil {
		b.Total += *result.TotalChecks
	}

	if result.SuccessfulChecks != nil {
		b.Up += *result.SuccessfulChecks
	}

	if result.DurationAvg != nil && result.TotalChecks != nil && *result.TotalChecks > 0 {
		b.DurSum += float64(*result.DurationAvg) * float64(*result.TotalChecks)
		b.DurCnt += *result.TotalChecks
	}
}

// BucketAvailability runs ONE raw+hour+day query over
// [bucketStart, bucketStart+n*bucketDuration) for all checks and returns
// per-check, per-bucket stats keyed by the bucket's truncated start time. Buckets
// with no rows are simply absent from the inner map — the caller renders them as
// "no data". checkUIDs may name several checks (status page) or exactly one
// (badge); the single batched query keeps a busy page to one round-trip.
func BucketAvailability(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	bucketDuration time.Duration, bucketStart time.Time, n int,
) (map[string]map[time.Time]BucketStats, error) {
	out := make(map[string]map[time.Time]BucketStats, len(checkUIDs))

	if len(checkUIDs) == 0 || n <= 0 {
		return out, nil
	}

	start := bucketStart.UTC()

	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &start,
		// No row-count Limit here (0 = no limit, same idiom as
		// models.ListResultsFilter elsewhere): the window is already bounded by
		// PeriodStartAfter, and a single bucket can be fed by many rows — up to
		// ~24 hourly rollups plus every not-yet-rolled-up raw row per probe per
		// region, and hourly rollups themselves can persist for RetentionHour
		// days (30 by default) before being folded into a day row. A row-count
		// Limit sized off "n buckets" (the previous bug) or even a generous
		// fixed cap silently drops the OLDEST rows first (ORDER BY period_start
		// DESC) once a check's real row count exceeds it, which is exactly the
		// "only the newest 1-3 days render" bug this replaces.
	}

	resp, err := db.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return out, nil
	}

	for _, result := range resp.Results {
		bucket := result.PeriodStart.UTC().Truncate(bucketDuration)

		byBucket := out[result.CheckUID]
		if byBucket == nil {
			byBucket = make(map[time.Time]BucketStats, n)
			out[result.CheckUID] = byBucket
		}

		acc := byBucket[bucket]

		if result.PeriodType == models.PeriodTypeRaw {
			acc.accumulateRaw(result)
		} else {
			acc.accumulateAgg(result)
		}

		byBucket[bucket] = acc
	}

	return out, nil
}
