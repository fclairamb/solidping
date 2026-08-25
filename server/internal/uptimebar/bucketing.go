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
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
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

	// MaintUp / MaintTotal are strict SUBSETS of Up / Total: the share of this
	// bucket recorded while an active maintenance window covered the check
	// (spec 2026-08-20-01).
	//
	// They are carried alongside rather than pre-subtracted precisely so that
	// AvailabilityPct — what badges, status pages and the availability API all
	// read — is byte-identical to what it was before this feature existed.
	// Only ExcludingMaintenance() subtracts them, and only the SLO read path
	// calls it.
	MaintUp    int
	MaintTotal int
}

// ExcludingMaintenance returns the same bucket with maintenance-tagged probes
// removed from both numerator and denominator.
//
// Defensive clamping: the counters are subsets by construction, but a bucket
// merged from rows written before the tagging existed could in principle carry
// a larger subset than parent if anything ever went wrong upstream. Clamping
// here means the worst case is "maintenance exclusion did nothing", never a
// negative denominator that renders as a nonsense attainment.
func (b BucketStats) ExcludingMaintenance() BucketStats {
	out := b

	maintTotal := min(b.MaintTotal, b.Total)
	maintUp := min(b.MaintUp, b.Up)
	maintUp = min(maintUp, maintTotal)

	out.Total -= maintTotal
	out.Up -= maintUp
	out.MaintTotal = 0
	out.MaintUp = 0

	return out
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

// Add folds another bucket into this one. This is the ONLY place buckets are
// merged, so a new counter can never be added to BucketStats and silently
// forgotten by the group-merge path (that is exactly how MaintUp/MaintTotal
// would have gone missing on group SLOs).
func (b *BucketStats) Add(other BucketStats) {
	b.Up += other.Up
	b.Total += other.Total
	b.DurCnt += other.DurCnt
	b.DurSum += other.DurSum
	b.MaintUp += other.MaintUp
	b.MaintTotal += other.MaintTotal
}

// accumulateRaw merges a raw result row into the bucket. Lifecycle markers
// (created/running) and reaped attempts (models.ResultStatusAbandoned) are
// excluded from the denominator (models.Result.ExcludedFromAvailability,
// specs 2026-08-18-03 and 2026-08-18-10);
// up + warning count as success — the canonical models.RawAvailability /
// CountsAsUp rule, which also matches the aggregation job and the status
// page. This is the single point where the "warning counts as up" rule lives
// for the raw tier.
func (b *BucketStats) accumulateRaw(result *models.Result) {
	if result.Status == nil || result.ExcludedFromAvailability() {
		return
	}

	status := models.ResultStatus(*result.Status)

	b.Total++

	if status.CountsAsUp() {
		b.Up++
	}

	if result.Maintenance {
		b.MaintTotal++

		if status.CountsAsUp() {
			b.MaintUp++
		}
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

	// nil (a row rolled up before maintenance tagging shipped) contributes
	// nothing — "no evidence" rather than "zero, confidently".
	if result.MaintenanceChecks != nil {
		b.MaintTotal += *result.MaintenanceChecks
	}

	if result.MaintenanceSuccessfulChecks != nil {
		b.MaintUp += *result.MaintenanceSuccessfulChecks
	}

	if result.DurationAvg != nil && result.TotalChecks != nil && *result.TotalChecks > 0 {
		b.DurSum += float64(*result.DurationAvg) * float64(*result.TotalChecks)
		b.DurCnt += *result.TotalChecks
	}
}

// Defaults used by the row caps and the raw clamp when the caller doesn't have a
// real retention config to hand (e.g. the MCP handler passes cfg=nil, or a test
// exercising the bucketing logic in isolation). RetentionHour is deliberately a
// generous upper bound for the row cap, not the tightened live default
// (jobtypes' 7 days) — a wider cap only ever admits more rows, never truncates,
// which is the safe direction for a fallback. RetentionRaw, by contrast, must
// match the live default (24 h) because it also bounds the raw CLAMP, where too
// wide is as wrong as too narrow. Never used on the normal production call path,
// which always passes the org's actual configured retention (see
// systemconfig.ResolveAggregationRetention).
const (
	defaultRetentionRawHours = 24
	defaultRetentionHourDays = 30
)

// capMaxRegionsPerCheck generously bounds the number of distinct regions the row
// caps assume per check when the caller supplies no measured probe rate. Real
// deployments run a handful of regions (2-5 is typical for a multi-region
// check); this is padded well past that so the cap never bites under any
// realistic multi-region topology — it only engages when retention is
// misconfigured or the aggregation job has been unhealthy for a long stretch.
const capMaxRegionsPerCheck = 20

// capSafetyMargin pads a computed row cap to absorb rounding and tier-boundary
// edge cases (e.g. a bucket that straddles the raw/hour boundary while the
// aggregation job is mid-rollup).
const capSafetyMargin = 500

// capRateHeadroom multiplies the caller's MEASURED raw row rate when that rate
// is available. It absorbs everything the measurement cannot see: internal
// (system-created) checks, which ListOrgCheckRates excludes; rows still inside
// the retention window that belong to a check deleted since; and a check whose
// period was shortened after the older rows were written. Four times the org's
// entire configured probe rate is far past any of those, while still being one
// to two orders of magnitude below the unmeasured worst case — which is the
// whole point: the cap must be a real guard, not a formality (spec
// 2026-08-17-03 §4).
const capRateHeadroom = 4

// Hints size uptimebar's raw clamp and its two per-tier safety row caps. The
// zero value is valid everywhere and means "use the documented defaults and the
// conservative unmeasured worst case".
//
// Callers resolve these ONCE per request (see each service's uptimebarHints):
// RetentionRaw/RetentionHour must come from systemconfig.ResolveAggregationRetention
// so the reader agrees with the aggregation job about how much raw exists, and
// RawRowsPerHour is the org's measured probe rate, which is what lets the raw cap
// be sized from reality instead of from the platform's theoretical maximum.
type Hints struct {
	// RetentionRawHours is Aggregation.RetentionRaw — hours of raw kept before
	// it is rolled up and deleted. 0 = documented default (24).
	RetentionRawHours int
	// RetentionHourDays is Aggregation.RetentionHour — days of hourly rollups
	// kept. 0 = documented default.
	RetentionHourDays int
	// RawRowsPerHour is the number of raw rows the org's checks can produce per
	// hour, measured from their configured periods and region counts (see
	// RawRowsPerHour). 0 = unknown, fall back to the worst case. It is an
	// org-wide figure and therefore a valid upper bound for any subset of the
	// org's checks.
	RawRowsPerHour int
}

// RawRowsPerHour sums the raw rows per hour the given checks can produce:
// (3600s / period) × max(1, regions) each, since a multi-region check executes
// once per region per period. Mirrors entitlements.Usage's checks-per-minute
// formula. Disabled checks are counted too — they stop producing rows but the
// ones they already wrote stay queryable until retention expires them.
func RawRowsPerHour(rates []models.CheckRate) int {
	total := 0

	for i := range rates {
		period := time.Duration(rates[i].Period)
		if period <= 0 {
			continue
		}

		regions := len(rates[i].Regions)
		if regions < 1 {
			regions = 1
		}

		total += int(time.Hour/period) * regions
	}

	return total
}

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

// RawTierStart is the exported form of rawTierStart, for readers outside this
// package that must bound a raw-tier query by exactly the same clamp — today
// the status page's response-time fetch (spec 2026-08-22-05). It is exported
// rather than reimplemented so there is still ONE raw bound in the system: the
// clamp is what keeps raw and rollups disjoint, and a second copy that drifted
// would either drop raw rows no rollup covers yet or double-count the overlap.
//
// retentionRawHours must come from systemconfig (see each service's
// uptimebarHints), never from the koanf field alone.
func RawTierStart(windowStart, now time.Time, retentionRawHours int) time.Time {
	return rawTierStart(windowStart, now, retentionRawHours)
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

// rawTierHours is how many hours of raw the clamped query can span:
// min(window, RetentionRaw + rawClampMargin), never below 1.
func rawTierHours(windowSpan time.Duration, retentionRawHours int) int {
	hours := effectiveRetentionRawHours(retentionRawHours) + int(rawClampMargin/time.Hour)
	if windowHours := int(windowSpan / time.Hour); windowHours < hours {
		hours = windowHours
	}

	if hours < 1 {
		hours = 1
	}

	return hours
}

// rawRowCap bounds the RAW tier's query, which the clamp already bounds in TIME
// (rawTierHours). What remains to bound is the row RATE inside that window, and
// there are two ways to know it:
//
//   - Measured (hints.RawRowsPerHour > 0, the production path): the org's checks
//     can only produce that many raw rows an hour, so the whole window holds at
//     most hours × rate. Multiplied by capRateHeadroom for what the measurement
//     cannot see. This is a REAL bound — for a typical org (a handful of checks
//     at a 60 s period in one region) it lands in the tens of thousands, against
//     the LIMIT 884300 the spec measured, which was large enough to be
//     functionally unbounded and therefore protected nothing.
//   - Unmeasured (rate 0, e.g. the MCP handler or a unit test): fall back to the
//     platform's theoretical worst case — every check in the batch probing at
//     checkerdef.GlobalMinPeriod from capMaxRegionsPerCheck regions at once.
//
// The measured bound is capped by the unmeasured one, so a hint can only ever
// tighten the query, never loosen it past what the platform can physically
// produce. If a cap does engage, the query still returns its (partial) rows and
// logs a warning — see listTier.
func rawRowCap(hints Hints, checkCount int, windowSpan time.Duration) int {
	if checkCount < 1 {
		checkCount = 1
	}

	hours := rawTierHours(windowSpan, hints.RetentionRawHours)

	worstCase := hours*int(time.Hour/checkerdef.GlobalMinPeriod)*capMaxRegionsPerCheck*checkCount + capSafetyMargin

	if hints.RawRowsPerHour <= 0 {
		return worstCase
	}

	measured := hours*hints.RawRowsPerHour*capRateHeadroom + capSafetyMargin
	if measured < worstCase {
		return measured
	}

	return worstCase
}

// rollupRowCap bounds the ROLLUP tiers' query: one row per bucket per tier per
// region — hour rollups for min(window, RetentionHour) days, day rollups for at
// most the window's own span in days, and (when includeMonth, i.e. the
// WindowAvailability path) one month row per ~30 days of window. Both bounds are
// independent of RetentionDay/RetentionMonth: a tier can never return more rows
// than the requested window has buckets.
func rollupRowCap(hints Hints, checkCount int, windowSpan time.Duration, includeMonth bool) int {
	retentionHourDays := hints.RetentionHourDays
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

// Bounded db_query_duration_seconds callsite labels for the uptimebar entry
// points (see sloghook.WithCallsite) — the highest-value queries to label
// after the 2026-08-16 status-page slowdown (spec 2026-08-17-04).
const (
	callsiteBucketAvailability = "uptimebar.bucket_availability"
	callsiteWindowAvailability = "uptimebar.window_availability"
)

// listTier runs one tier-aligned query and warns (without failing) when the
// safety cap engaged, returning the partial rows — the "generous cap + log +
// return partial" pattern also used by the Slack client's list pagination (see
// internal/integrations/slack/client.go's paginate). callsite is a bounded
// db_query_duration_seconds label (see sloghook.WithCallsite) identifying
// which uptimebar entry point issued the query.
func listTier(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	filter *models.ListResultsFilter, tier, callsite string,
) ([]*models.Result, error) {
	resp, err := db.ListResults(sloghook.WithCallsite(ctx, callsite), filter)
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
// hints bound the raw clamp and size each tier's own safety row cap (see Hints,
// rawRowCap and rollupRowCap): a single bucket can be fed by many rows, so an
// UNBOUNDED query risks scanning without limit if retention is misconfigured or
// the aggregation job is unhealthy. The caps are sized so they never bite under
// realistic configurations — if one does engage, a warning is logged and the
// partial result is returned rather than erroring. The zero Hints is valid and
// falls back to the documented defaults.
func BucketAvailability(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	bucketDuration time.Duration, bucketStart time.Time, n int,
	hints Hints,
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
		Limit:            rollupRowCap(hints, len(checkUIDs), windowSpan, false),
		// Buckets are built from status/counts only, so the metrics/output blobs
		// are dead weight on these queries (spec 2026-07-24-02 §5).
		SkipBlobs: true,
	}, models.PeriodTypeHour+"+"+models.PeriodTypeDay, callsiteBucketAvailability)
	if err != nil {
		return nil, err
	}

	// Raw tier: clamped to the raw-retention band, answered by results_raw_idx.
	rawStart := rawTierStart(start, now, hints.RetentionRawHours)

	rawRows, err := listTier(ctx, db, orgUID, checkUIDs, &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &rawStart,
		Limit:            rawRowCap(hints, len(checkUIDs), windowSpan),
		SkipBlobs:        true,
	}, models.PeriodTypeRaw, callsiteBucketAvailability)
	if err != nil {
		return nil, err
	}

	warnIfRawLagging(ctx, orgUID, rawRows, now, hints.RetentionRawHours)

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

// CheckRateLister is the minimal db surface needed to measure an org's raw row
// rate (db.Service implements it).
type CheckRateLister interface {
	ListOrgCheckRates(ctx context.Context, orgUID string) ([]models.CheckRate, error)
}

// MeasureRawRowsPerHour returns the org's configured raw row rate for Hints, or
// 0 when it cannot be read — in which case the caps fall back to the
// conservative unmeasured worst case rather than to no bound at all. A read
// failure must never fail the render: this only sizes a safety cap.
func MeasureRawRowsPerHour(ctx context.Context, db CheckRateLister, orgUID string) int {
	rates, err := db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		slog.WarnContext(ctx, "uptimebar could not measure the org's probe rate; "+
			"falling back to the conservative row cap",
			"organization_uid", orgUID, "error", err)

		return 0
	}

	return RawRowsPerHour(rates)
}
