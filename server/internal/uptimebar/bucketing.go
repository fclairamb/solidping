// Package uptimebar is the single shared source for per-bucket availability used
// by both the public status page and the badge uptime bar. Both surfaces render a
// strip of "ticks" (24 hourly buckets for 24h, N daily buckets for 7d/30d/90d).
// They MUST bucket from the same data so they never disagree for the same check +
// period.
//
// The core is BucketAvailability: one query over a raw+hour+day union for the
// whole window. Because the aggregation job deletes source rows after each rollup
// (raw → hour → day → month), the tiers cover non-overlapping age bands, so
// unioning them never double-counts. A bucket whose raw rows haven't been rolled
// up yet is still filled immediately from raw — this is what fixes status-page
// buckets reading "No data" while the badge showed data.
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

// safetyRowCap computes a generous upper bound on the number of rows
// BucketAvailability's query should ever need to return for a healthy
// deployment. It is derived from the actual retention configuration — the
// same tier-boundary reasoning jobtypes.calculateAggregationBoundary /
// retentionFromConfig use — rather than a fixed magic number, so it doesn't
// reintroduce the "only the newest few days render" truncation bug for any
// reasonable configuration. It exists purely to bound the pathological case:
// retention misconfigured to an extreme value, or the aggregation job
// stalled/crashed so raw rows pile up indefinitely instead of rolling up.
//
// Per check-region, the query can only need, within the requested window:
//   - raw rows for min(window, RetentionRaw) hours at the platform's fastest
//     allowed check period (checkerdef.GlobalMinPeriod),
//   - hour rollups for min(window, RetentionHour) days (one row/hour), and
//   - day rollups for the window's own span in days (one row/day — this tier
//     can never exceed the window regardless of RetentionDay).
//
// That per-check-region figure is padded by a generous per-check region count
// (capMaxRegionsPerCheck) and multiplied by the number of checks in the
// query, since a single batched query (status page) can span many checks.
func safetyRowCap(
	checkCount, n int, bucketDuration time.Duration,
	retentionRawHours, retentionHourDays int,
) int {
	if retentionRawHours < 1 {
		retentionRawHours = defaultRetentionRawHours
	}

	if retentionHourDays < 1 {
		retentionHourDays = defaultRetentionHourDays
	}

	if checkCount < 1 {
		checkCount = 1
	}

	windowSpan := time.Duration(n) * bucketDuration

	windowDays := int(windowSpan / (24 * time.Hour))
	if windowSpan%(24*time.Hour) != 0 {
		windowDays++
	}

	if windowDays < 1 {
		windowDays = 1
	}

	// Raw tier: bounded by whichever is smaller — the requested window or
	// RetentionRaw — since rows older than RetentionRaw hours are rolled up
	// (and deleted) and rows outside the window aren't queried at all.
	rawTierHours := retentionRawHours
	if windowHours := int(windowSpan / time.Hour); windowHours < rawTierHours {
		rawTierHours = windowHours
	}

	if rawTierHours < 1 {
		rawTierHours = 1
	}

	rawRowsPerRegion := rawTierHours * int(time.Hour/checkerdef.GlobalMinPeriod)

	// Hour tier: one row per hour, bounded by whichever is smaller — the
	// window or RetentionHour.
	hourTierDays := retentionHourDays
	if windowDays < hourTierDays {
		hourTierDays = windowDays
	}

	hourRowsPerRegion := hourTierDays * 24

	// Day tier: one row per day; can never exceed the window itself.
	dayRowsPerRegion := windowDays

	perRegion := rawRowsPerRegion + hourRowsPerRegion + dayRowsPerRegion

	return perRegion*capMaxRegionsPerCheck*checkCount + capSafetyMargin
}

// BucketAvailability runs ONE raw+hour+day query over
// [bucketStart, bucketStart+n*bucketDuration) for all checks and returns
// per-check, per-bucket stats keyed by the bucket's truncated start time. Buckets
// with no rows are simply absent from the inner map — the caller renders them as
// "no data". checkUIDs may name several checks (status page) or exactly one
// (badge); the single batched query keeps a busy page to one round-trip.
//
// retentionRawHours/retentionHourDays are the org's configured
// Aggregation.RetentionRaw/RetentionHour (hours of raw kept / days of hourly
// rollups kept) — pass 0 to fall back to the documented config defaults (see
// safetyRowCap). They size a generous safety cap on the query's row count: the
// window is already bounded by PeriodStartAfter, but a single bucket can be
// fed by many rows (every not-yet-rolled-up raw row per probe per region, plus
// up to RetentionHour days of hourly rollups), so an UNBOUNDED query risks
// scanning without limit if retention is misconfigured or the aggregation job
// is unhealthy. The cap is sized so it never bites under realistic
// configurations (see safetyRowCap) — if it ever does engage, a warning is
// logged and the partial result is returned rather than erroring, matching
// the "generous cap + log + return partial" pattern used by the Slack client's
// list pagination (see internal/integrations/slack/client.go's paginate).
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

	limit := safetyRowCap(len(checkUIDs), n, bucketDuration, retentionRawHours, retentionHourDays)

	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &start,
		Limit:            limit,
		// Buckets are built from status/counts only, so the metrics/output blobs
		// are dead weight on a query bounded by safetyRowCap (spec 2026-07-24-02 §5).
		SkipBlobs: true,
	}

	resp, err := db.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return out, nil
	}

	if len(resp.Results) >= limit {
		slog.WarnContext(ctx, "uptimebar bucket-availability query hit its safety row cap; "+
			"returning partial data",
			"organization_uid", orgUID,
			"check_uids", checkUIDs,
			"limit", limit,
		)
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
