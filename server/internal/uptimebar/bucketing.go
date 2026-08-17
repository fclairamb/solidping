// Package uptimebar is the single shared source for per-bucket availability used
// by both the public status page and the badge uptime bar. Both surfaces render a
// strip of "ticks" (24 hourly buckets for 24h, N daily buckets for 7d/30d/90d).
// They MUST bucket from the same data so they never disagree for the same check +
// period.
//
// The core is BucketAvailability: TWO tier-aligned queries (rollups over the
// whole window, raw clamped to the raw-retention band) merged into one bucket
// map. Because the aggregation job deletes source rows after each rollup
// (raw → hour → day → month), the tiers cover non-overlapping age bands, so
// unioning them never double-counts. A bucket whose raw rows haven't been rolled
// up yet is still filled immediately from raw — this is what fixes status-page
// buckets reading "No data" while the badge showed data.
//
// Why two queries and not one `period_type IN ('raw','hour','day')`: `results`
// has exactly two useful indexes and both are PARTIAL — results_raw_idx
// (WHERE period_type = 'raw') and results_aggregated_idx (WHERE period_type <>
// 'raw'). A predicate straddling both halves is implied by neither, so Postgres
// can only answer it with a parallel sequential scan of the whole table
// (measured: 530 ms warm / 2.4 s cold, ~318 MB read, ~891 k rows discarded to
// return 17 k). Split by tier, each half is implied by exactly one partial index
// and the planner uses it (spec 2026-08-17-03).
//
// The month tier is deliberately NOT part of the per-bucket union: a month rollup
// spans many hour/day ticks and cannot be honestly attributed to any single one —
// truncating it to its period_start would dump a whole month's counts into one
// bucket. Ticks older than the day-tier horizon (RetentionDay, 2 months by
// default) therefore render as "no data", which is the truthful answer at that
// granularity. Whole-window folds don't have this problem — WindowAvailability
// (window.go) does include the month tier.
package uptimebar

import (
	"context"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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

// accumulateAgg merges an aggregated rollup row (hour/day, plus month on the
// WindowAvailability path) into the bucket. Rollup rows already encode the
// CountsAsUp rule in SuccessfulChecks (the aggregation job counts warning as
// up), so this path needs no per-status logic.
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

// Defaults used by safetyRowCap when the caller doesn't have a real retention
// config to hand (e.g. the MCP handler passes cfg=nil, or a test exercising the
// bucketing logic in isolation). These are a deliberately generous upper bound
// for the row cap, not the tightened live defaults (jobtypes' 24/7/2) — a wider
// cap only ever admits more rows, never truncates, which is the safe direction
// for a fallback. Never used on the normal production call path, which always
// passes the org's actual configured retention.
const (
	defaultRetentionRawHours = 24
	defaultRetentionHourDays = 30
)

// capMaxRegionsPerCheck generously bounds the number of distinct regions
// safetyRowCap assumes per check when sizing the query's row cap. Real
// deployments run a handful of regions (2-5 is typical for a multi-region
// check); this is padded well past that so the cap never bites under any
// realistic multi-region topology — it only engages when retention is
// misconfigured or the aggregation job has been unhealthy for a long stretch.
const capMaxRegionsPerCheck = 20

// capSafetyMargin pads safetyRowCap's computed bound to absorb rounding and
// tier-boundary edge cases (e.g. a bucket that straddles the raw/hour
// boundary while the aggregation job is mid-rollup).
const capSafetyMargin = 500

// rawClampMargin pads the raw tier's lower bound past RetentionRaw to absorb
// aggregation lag: a bucket whose rollup hasn't run yet must still be readable
// from raw. It is deliberately SMALL. The raw tier is the whole remaining cost
// once the tiers are split and it scales sharply with the bound — measured on a
// live instance, a 24 h bound costs 97 ms and a 48 h bound 622 ms — so a
// "generous" 24 h margin would give back most of what the split buys. If raw
// rows do show up older than RetentionRaw, that is aggregation lagging, and it
// is logged rather than absorbed silently (see BucketAvailability).
const rawClampMargin = 2 * time.Hour

// effectiveRetentionRawHours resolves the caller's RetentionRaw hint, falling
// back to the documented default when it is unset/invalid.
func effectiveRetentionRawHours(retentionRawHours int) int {
	if retentionRawHours < 1 {
		return defaultRetentionRawHours
	}

	return retentionRawHours
}

// rawTierStart clamps the raw-tier query's lower bound to
// max(windowStart, now-(RetentionRaw+rawClampMargin)).
//
// This cannot drop data a rollup does not already cover: the aggregation job
// compacts a bucket and deletes its source raw rows in ONE transaction
// (jobs/jobtypes/job_aggregation.go), so raw and rollups are disjoint by
// construction and raw older than RetentionRaw simply does not exist. The clamp
// is what turns the raw half from a full scan into a bounded
// results_raw_idx lookup, and it is also what keeps the two halves disjoint —
// widening it past a rollup boundary would silently DOUBLE-COUNT, since the
// accumulator adds raw and rollup rows into the same BucketStats.
func rawTierStart(windowStart, now time.Time, retentionRawHours int) time.Time {
	bound := now.Add(-(time.Duration(effectiveRetentionRawHours(retentionRawHours))*time.Hour + rawClampMargin))
	if bound.After(windowStart) {
		return bound
	}

	return windowStart
}

// warnIfRawLagging logs once when any returned raw row is older than
// RetentionRaw — i.e. it only survived the query because of rawClampMargin.
// Raw that old should have been rolled up and deleted already, so its presence
// means the aggregation job is behind. Same "log it, return the data anyway"
// shape as the row-cap warning below.
func warnIfRawLagging(ctx context.Context, orgUID string, rows []*models.Result, now time.Time, retentionRawHours int) {
	threshold := now.Add(-time.Duration(effectiveRetentionRawHours(retentionRawHours)) * time.Hour)

	for _, row := range rows {
		if row.PeriodStart.Before(threshold) {
			slog.WarnContext(ctx, "uptimebar found raw results older than the configured raw retention; "+
				"aggregation is lagging",
				"organization_uid", orgUID,
				"check_uid", row.CheckUID,
				"oldest_seen", row.PeriodStart.UTC(),
				"retention_raw_hours", effectiveRetentionRawHours(retentionRawHours),
			)

			return
		}
	}
}

// windowDayCount is the window's span in whole days, rounded up, never below 1.
func windowDayCount(windowSpan time.Duration) int {
	days := int(windowSpan / (24 * time.Hour))
	if windowSpan%(24*time.Hour) != 0 {
		days++
	}

	if days < 1 {
		days = 1
	}

	return days
}

// rawRowCap bounds the RAW tier's query. Per check-region the raw tier can only
// hold min(window, RetentionRaw+margin) hours of rows at the platform's fastest
// allowed check period (checkerdef.GlobalMinPeriod); that is padded by a
// generous per-check region count and multiplied by the number of checks in the
// batch. Sizing it to its own tier — rather than to the sum of all tiers, which
// produced a functionally unbounded LIMIT 884300 on the observed request — is
// what makes the cap an actual guard (spec 2026-08-17-03 §4).
func rawRowCap(checkCount int, windowSpan time.Duration, retentionRawHours int) int {
	if checkCount < 1 {
		checkCount = 1
	}

	rawHours := effectiveRetentionRawHours(retentionRawHours) + int(rawClampMargin/time.Hour)
	if windowHours := int(windowSpan / time.Hour); windowHours < rawHours {
		rawHours = windowHours
	}

	if rawHours < 1 {
		rawHours = 1
	}

	perRegion := rawHours * int(time.Hour/checkerdef.GlobalMinPeriod)

	return perRegion*capMaxRegionsPerCheck*checkCount + capSafetyMargin
}

// rollupRowCap bounds the ROLLUP tiers' query: one row per bucket per tier per
// region — hour rollups for min(window, RetentionHour) days, day rollups for at
// most the window's own span in days, and (when includeMonth, i.e. the
// WindowAvailability path) one month row per ~30 days of window. Both bounds are
// independent of RetentionDay/RetentionMonth: a tier can never return more rows
// than the requested window has buckets.
func rollupRowCap(checkCount int, windowSpan time.Duration, retentionHourDays int, includeMonth bool) int {
	if retentionHourDays < 1 {
		retentionHourDays = defaultRetentionHourDays
	}

	if checkCount < 1 {
		checkCount = 1
	}

	windowDays := windowDayCount(windowSpan)

	hourTierDays := retentionHourDays
	if windowDays < hourTierDays {
		hourTierDays = windowDays
	}

	perRegion := hourTierDays*24 + windowDays

	if includeMonth {
		perRegion += windowDays/28 + 1
	}

	return perRegion*capMaxRegionsPerCheck*checkCount + capSafetyMargin
}

// listTier runs one tier-aligned query and warns (without failing) when the
// safety cap engaged, returning the partial rows — the "generous cap + log +
// return partial" pattern also used by the Slack client's list pagination (see
// internal/integrations/slack/client.go's paginate).
func listTier(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	filter *models.ListResultsFilter, tier string,
) ([]*models.Result, error) {
	resp, err := db.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return nil, nil
	}

	if filter.Limit > 0 && len(resp.Results) >= filter.Limit {
		slog.WarnContext(ctx, "uptimebar availability query hit its safety row cap; "+
			"returning partial data",
			"organization_uid", orgUID,
			"check_uids", checkUIDs,
			"tier", tier,
			"limit", filter.Limit,
		)
	}

	return resp.Results, nil
}

// BucketAvailability runs two tier-aligned queries over
// [bucketStart, bucketStart+n*bucketDuration) for all checks and returns
// per-check, per-bucket stats keyed by the bucket's truncated start time. Buckets
// with no rows are simply absent from the inner map — the caller renders them as
// "no data". checkUIDs may name several checks (status page) or exactly one
// (badge); both queries are batched across every check, so a busy page still
// costs two round-trips regardless of how many checks it renders.
//
// The two queries are (see the package comment for why they cannot be one):
//   - rollups (hour+day) over the full window, and
//   - raw clamped to max(windowStart, now-(RetentionRaw+rawClampMargin)).
//
// Both feed the SAME accumulator, so bucketing semantics are unchanged: the
// tiers stay disjoint (see rawTierStart) and nothing is double-counted.
//
// retentionRawHours/retentionHourDays are the org's configured
// Aggregation.RetentionRaw/RetentionHour (hours of raw kept / days of hourly
// rollups kept) — pass 0 to fall back to the documented config defaults. They
// bound the raw clamp and size each tier's own safety row cap (see rawRowCap /
// rollupRowCap): a single bucket can be fed by many rows, so an UNBOUNDED query
// risks scanning without limit if retention is misconfigured or the aggregation
// job is unhealthy. The caps are sized so they never bite under realistic
// configurations — if one does engage, a warning is logged and the partial
// result is returned rather than erroring.
func BucketAvailability(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	bucketDuration time.Duration, bucketStart time.Time, n int,
	retentionRawHours, retentionHourDays int,
) (map[string]map[time.Time]BucketStats, error) {
	out := make(map[string]map[time.Time]BucketStats, len(checkUIDs))

	if len(checkUIDs) == 0 || n <= 0 {
		return out, nil
	}

	start := bucketStart.UTC()
	now := time.Now().UTC()
	windowSpan := time.Duration(n) * bucketDuration

	// Rollup tiers: the full window, answered by results_aggregated_idx.
	rollupRows, err := listTier(ctx, db, orgUID, checkUIDs, &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &start,
		Limit:            rollupRowCap(len(checkUIDs), windowSpan, retentionHourDays, false),
		// Buckets are built from status/counts only, so the metrics/output blobs
		// are dead weight on these queries (spec 2026-07-24-02 §5).
		SkipBlobs: true,
	}, models.PeriodTypeHour+"+"+models.PeriodTypeDay)
	if err != nil {
		return nil, err
	}

	// Raw tier: clamped to the raw-retention band, answered by results_raw_idx.
	rawStart := rawTierStart(start, now, retentionRawHours)

	rawRows, err := listTier(ctx, db, orgUID, checkUIDs, &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &rawStart,
		Limit:            rawRowCap(len(checkUIDs), windowSpan, retentionRawHours),
		SkipBlobs:        true,
	}, models.PeriodTypeRaw)
	if err != nil {
		return nil, err
	}

	warnIfRawLagging(ctx, orgUID, rawRows, now, retentionRawHours)

	for _, rows := range [][]*models.Result{rollupRows, rawRows} {
		for _, result := range rows {
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
	}

	return out, nil
}
