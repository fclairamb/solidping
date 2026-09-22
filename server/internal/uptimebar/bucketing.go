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

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
)

// ResultBucketAggregator is the minimal db surface both services already satisfy
// (db.Service implements it). Keeping the dependency this small makes uptimebar a
// leaf package: it depends only on models, with no import cycle against the
// badges or statuspages handlers.
//
// It used to be a ResultsLister — `ListResults`, i.e. every matching ROW. The
// fold below keeps eleven numbers per (check, bucket), and under the default
// 24 h raw retention the newest day of every check is always raw, so a 200-check
// public status page shipped 267 449 rows into Go (sorted to disk on the way) to
// produce 400 buckets. The counters are now computed in the database and the
// accumulators below are the reference semantics the dialects' parity tests
// check against (spec 2026-09-22-05).
type ResultBucketAggregator interface {
	AggregateResultBuckets(
		ctx context.Context, filter *models.ResultBucketFilter,
	) ([]models.ResultBucket, error)
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

	// DurMin / DurMax are the extreme response times (milliseconds) folded
	// into this bucket, valid only when DurExtremaCnt > 0. Raw rows fold their
	// own `duration`; rollup rows fold the `duration_min` / `duration_max`
	// the aggregation job already persists.
	//
	// Like MaintUp/MaintTotal these are ADDITIVE NEW fields: nothing existing
	// reads them, and AvailabilityPct / AvgDuration / Up / Total / DurCnt /
	// DurSum keep byte-identical semantics, because badges, status pages, the
	// availability API and SLOs all read those and must not move
	// (spec 2026-09-01-04).
	//
	// The narrow types are deliberate. BucketStats is passed BY VALUE on every
	// read path — it is a map value and an accumulator, and a pointer receiver
	// would make `byCheck[uid].AvailabilityPct()` illegal at every call site —
	// so the struct has to stay small enough for that to be cheap. float32 is
	// also exactly what the source columns are (models.Result.Duration /
	// DurationMin / DurationMax); widening them here would fabricate
	// precision. The counters are per-bucket and bounded by uptimebar's own
	// safety row caps, so int32 is orders of magnitude more headroom than any
	// window can produce. A future field should keep the total under
	// gocritic's by-value threshold rather than widening these back.
	DurMin        float32
	DurMax        float32
	DurExtremaCnt int32

	// SlowSamples counts RAW samples strictly above SlowSampleThresholdMillis.
	// It is exact: one raw row is one probe.
	SlowSamples int32
	// SlowPeaks counts ROLLUP rows whose duration_max exceeds the threshold —
	// a rolled-up period that contained at least one slow probe, which is NOT
	// the same unit as a sample (the period may have held one slow probe or a
	// thousand). It is kept as its own counter, never added to SlowSamples,
	// precisely so a reader has to phrase the two honestly.
	SlowPeaks int32
}

// SlowSampleThresholdMillis is the response time above which a probe counts as
// slow. A constant for now by design: the uptime report is its only reader and
// the spec that introduced it (2026-09-01-04) rules a configurable threshold
// out of scope. Milliseconds, matching models.Result.Duration.
//
// The value itself lives in models, because the per-bucket aggregate SQL has to
// encode it too and the dialect packages cannot import this one. This alias is
// what keeps every reader pointed at the accumulators that give it meaning.
const SlowSampleThresholdMillis = models.SlowSampleThresholdMillis

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

// DurationRange returns the fastest and slowest response times (milliseconds)
// folded into this bucket, and a false third value when no contributing row
// carried one. It never reports a
// confident "0 ms to 0 ms" for a bucket that measured nothing.
func (b BucketStats) DurationRange() (float64, float64, bool) {
	if b.DurExtremaCnt == 0 {
		return 0, 0, false
	}

	return float64(b.DurMin), float64(b.DurMax), true
}

// foldExtrema folds one observed duration pair into the bucket's min/max. The
// first observation seeds both, so a zero value can never win the minimum for a
// bucket that has not measured anything yet.
func (b *BucketStats) foldExtrema(minMillis, maxMillis float32) {
	if b.DurExtremaCnt == 0 {
		b.DurMin, b.DurMax = minMillis, maxMillis
		b.DurExtremaCnt = 1

		return
	}

	b.DurMin = min(b.DurMin, minMillis)
	b.DurMax = max(b.DurMax, maxMillis)
	b.DurExtremaCnt++
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
	b.SlowSamples += other.SlowSamples
	b.SlowPeaks += other.SlowPeaks

	if other.DurExtremaCnt > 0 {
		count := b.DurExtremaCnt

		b.foldExtrema(other.DurMin, other.DurMax)

		// foldExtrema counts one observation; carry the merged bucket's real
		// count so a merge of merges stays honest.
		b.DurExtremaCnt = count + other.DurExtremaCnt
	}
}

// accumulateRaw merges a raw result row into the bucket. Lifecycle markers
// (created/running) and reaped attempts (models.ResultStatusAbandoned) are
// excluded from the denominator (models.Result.ExcludedFromAvailability,
// specs 2026-08-18-03 and 2026-08-18-10);
// up + warning count as success — the canonical models.RawAvailability /
// CountsAsUp rule, which also matches the aggregation job and the status
// page. This is the single point where the "warning counts as up" rule lives
// for the raw tier.
//
// Duration accumulation (DurSum/DurCnt, and now the extrema and slow counters)
// deliberately includes FAILED samples — any non-lifecycle row carrying a
// duration, down and error and timeout alike. That is not an oversight, it is
// the rule the aggregation job already applies when it computes duration_avg /
// duration_min / duration_max (jobs/jobtypes/job_aggregation.go's
// processRawResult), so raw and rollup tiers agree; changing it here would move
// what status pages and badges display. Readers that surface response times
// must say so in their copy (spec 2026-09-01-04).
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
		duration := *result.Duration

		b.DurSum += float64(duration)
		b.DurCnt++
		b.foldExtrema(duration, duration)

		if float64(duration) > SlowSampleThresholdMillis {
			b.SlowSamples++
		}
	}
}

// StatsForResult folds ONE result row into a BucketStats, choosing the raw or
// the aggregated accumulator from its period_type.
//
// It exists for readers that key on individual rows rather than on time buckets
// — the public status page's response-time series, whose x-axis slots ARE the
// rows (spec 2026-08-26-10 phase 2). Routing them through here rather than
// letting them count statuses themselves is the whole point: lifecycle markers
// and abandoned attempts leave both numerator and denominator alone, warning
// counts as up, and a rollup's SuccessfulChecks already encodes that rule. A
// second implementation of any of those is how surfaces start disagreeing.
//
// A row that contributes nothing (a lifecycle marker, an abandoned attempt)
// returns the zero BucketStats, whose AvailabilityPct reports ok=false — "no
// data", explicitly not 100%.
func StatsForResult(result *models.Result) BucketStats {
	var stats BucketStats

	if result == nil {
		return stats
	}

	if result.PeriodType == models.PeriodTypeRaw {
		stats.accumulateRaw(result)
	} else {
		stats.accumulateAgg(result)
	}

	return stats
}

// StatsForBucket lifts one database-computed models.ResultBucket into the
// BucketStats the whole read path speaks. It is the aggregate's counterpart to
// StatsForResult, and the ONLY translation between the two shapes — a second one
// is how a counter starts getting dropped on one path and not the other.
//
// It deliberately does no arithmetic: every field of ResultBucket was computed by
// SQL that mirrors accumulateRaw / accumulateAgg field for field, and the
// per-dialect parity tests pin that by folding the same fixture both ways. The
// one piece of judgement here is the extrema pair, which is carried only when the
// bucket really measured something — a bucket with no durations must keep
// DurationRange()'s ok=false rather than report a confident 0 ms.
func StatsForBucket(bucket *models.ResultBucket) BucketStats {
	stats := BucketStats{
		Up:          bucket.Up,
		Total:       bucket.Total,
		DurCnt:      bucket.DurCnt,
		DurSum:      bucket.DurSum,
		MaintUp:     bucket.MaintUp,
		MaintTotal:  bucket.MaintTotal,
		SlowSamples: bucket.SlowSamples,
		SlowPeaks:   bucket.SlowPeaks,
	}

	if bucket.DurExtremaCnt > 0 && bucket.DurMin != nil && bucket.DurMax != nil {
		stats.DurMin = *bucket.DurMin
		stats.DurMax = *bucket.DurMax
		stats.DurExtremaCnt = bucket.DurExtremaCnt
	}

	return stats
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

	// Extremes come from the columns the aggregation job persists. A row
	// carrying only one of the two (nothing writes that today, but a
	// hand-repaired row could) folds the one it has against itself rather than
	// against a confident zero.
	switch {
	case result.DurationMin != nil && result.DurationMax != nil:
		b.foldExtrema(*result.DurationMin, *result.DurationMax)
	case result.DurationMin != nil:
		b.foldExtrema(*result.DurationMin, *result.DurationMin)
	case result.DurationMax != nil:
		b.foldExtrema(*result.DurationMax, *result.DurationMax)
	}

	// A rollup cannot say HOW MANY of its probes were slow — only whether its
	// slowest one was. Counting the row (a "peak") rather than pretending to
	// count samples is what keeps the report's number honest across tiers.
	if result.DurationMax != nil && float64(*result.DurationMax) > SlowSampleThresholdMillis {
		b.SlowPeaks++
	}
}

// defaultRetentionRawHours is the raw retention the raw CLAMP falls back to when
// the caller has no real retention config to hand (e.g. the MCP handler passes
// cfg=nil, or a test exercising the bucketing logic in isolation). It must match
// the live default (24 h): the clamp is what keeps the raw and rollup tiers
// disjoint, so too wide double-counts and too narrow drops raw no rollup covers
// yet. Never used on the normal production call path, which always passes the
// org's actual configured retention (see systemconfig.ResolveAggregationRetention).
const defaultRetentionRawHours = 24

// Hints size uptimebar's raw clamp. The zero value is valid everywhere and means
// "use the documented default".
//
// Callers resolve these ONCE per request (see each service's uptimebarHints):
// RetentionRaw/RetentionHour must come from systemconfig.ResolveAggregationRetention
// so the reader agrees with the aggregation job about how much raw exists.
//
// This used to carry a measured probe rate as well, to size two per-tier safety
// row caps. Both caps are gone: they existed to bound how many ROWS the Go fold
// had to hold, and the fold now happens in the database, where the output is
// bounded by checks x buckets by construction. Sizing them also cost an extra
// ListOrgCheckRates query per request, and when one did engage it returned
// SILENTLY PARTIAL data — a wrong availability percentage with a log line
// (spec 2026-09-22-05).
type Hints struct {
	// RetentionRawHours is Aggregation.RetentionRaw — hours of raw kept before
	// it is rolled up and deleted. 0 = documented default (24).
	RetentionRawHours int
	// RetentionHourDays is Aggregation.RetentionHour — days of hourly rollups
	// kept. 0 = documented default. Carried for callers that resolve both
	// retention values together; the bucketing engine itself only needs the raw
	// one now that the rollup row cap is gone.
	RetentionHourDays int
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

// warnIfRawLagging logs once when a returned raw bucket holds a row older than
// RetentionRaw — i.e. it only survived the query because of rawClampMargin.
// Raw that old should have been rolled up and deleted already, so its presence
// means the aggregation job is behind. It is logged and the data returned
// anyway, never dropped.
//
// It reads each bucket's OldestPeriodStart, which the aggregate computes as
// MIN(period_start) per group. That is the whole reason the field exists: the row
// path had to walk every returned row to answer the same question.
func warnIfRawLagging(
	ctx context.Context, orgUID string, buckets []models.ResultBucket, now time.Time, retentionRawHours int,
) {
	threshold := now.Add(-time.Duration(effectiveRetentionRawHours(retentionRawHours)) * time.Hour)

	for i := range buckets {
		bucket := &buckets[i]

		if bucket.OldestPeriodStart.IsZero() || !bucket.OldestPeriodStart.Before(threshold) {
			continue
		}

		slog.WarnContext(ctx, "uptimebar found raw results older than the configured raw retention; "+
			"aggregation is lagging",
			"organization_uid", orgUID,
			"check_uid", bucket.CheckUID,
			"oldest_seen", bucket.OldestPeriodStart.UTC(),
			"retention_raw_hours", effectiveRetentionRawHours(retentionRawHours),
		)

		return
	}
}

// Bounded db_query_duration_seconds callsite labels for the uptimebar entry
// points (see sloghook.WithCallsite) — the highest-value queries to label
// after the 2026-08-16 status-page slowdown (spec 2026-08-17-04).
const (
	callsiteBucketAvailability = "uptimebar.bucket_availability"
	callsiteWindowAvailability = "uptimebar.window_availability"
)

// aggregateTier runs ONE tier-aligned aggregate. callsite is a bounded
// db_query_duration_seconds label (see sloghook.WithCallsite) identifying which
// uptimebar entry point issued the query.
//
// There is no row cap and no partial-result warning any more: the statement
// returns one row per (check, bucket), so its size is bounded by the caller's own
// request rather than by how much raw the aggregation job has yet to compact.
func aggregateTier(
	ctx context.Context, db ResultBucketAggregator, filter *models.ResultBucketFilter, callsite string,
) ([]models.ResultBucket, error) {
	return db.AggregateResultBuckets(sloghook.WithCallsite(ctx, callsite), filter)
}

// BucketAvailability runs two tier-aligned aggregates over
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
// hints bound the raw clamp (see Hints). The zero Hints is valid and falls back
// to the documented default.
func BucketAvailability(
	ctx context.Context, db ResultBucketAggregator, orgUID string, checkUIDs []string,
	bucketDuration time.Duration, bucketStart time.Time, n int,
	hints Hints,
) (map[string]map[time.Time]BucketStats, error) {
	return BucketAvailabilityInRegions(
		ctx, db, orgUID, checkUIDs, nil, bucketDuration, bucketStart, n, hints)
}

// BucketAvailabilityInRegions is BucketAvailability restricted to a set of
// probe regions. `regions` nil or empty means "every region", which is the
// historical behavior: the engine then SUMS up/total across regions rather
// than averaging their percentages, so a check probed from three regions is
// weighted by how many probes each region actually contributed (the
// statuspages mergeBuckets rule). Naming one region instead buckets that
// region alone.
//
// The filter is pushed into the query (ResultBucketFilter.Regions) rather than
// applied afterwards on purpose: hour and day rollups keep the region they were
// rolled up from (job_aggregation.go), so a region-scoped read is a real
// per-region rollup read at every tier — filtering afterwards would work only
// for the raw tier and silently lose the rollups. Region is a WHERE clause and
// never a GROUP BY key, so a multi-region read still yields ONE bucket per
// (check, time) with every region's probes summed into it.
func BucketAvailabilityInRegions(
	ctx context.Context, db ResultBucketAggregator, orgUID string, checkUIDs, regions []string,
	bucketDuration time.Duration, bucketStart time.Time, n int,
	hints Hints,
) (map[string]map[time.Time]BucketStats, error) {
	out := make(map[string]map[time.Time]BucketStats, len(checkUIDs))

	if len(checkUIDs) == 0 || n <= 0 || bucketDuration <= 0 {
		return out, nil
	}

	start := bucketStart.UTC()
	now := time.Now().UTC()

	// Rollup tiers: the full window, answered by results_aggregated_idx.
	rollupBuckets, err := aggregateTier(ctx, db, &models.ResultBucketFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		Regions:          regions,
		PeriodTypes:      []string{models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: start,
		BucketDuration:   bucketDuration,
	}, callsiteBucketAvailability)
	if err != nil {
		return nil, err
	}

	// Raw tier: clamped to the raw-retention band, answered by results_raw_idx.
	rawBuckets, err := aggregateTier(ctx, db, &models.ResultBucketFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		Regions:          regions,
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: rawTierStart(start, now, hints.RetentionRawHours),
		BucketDuration:   bucketDuration,
	}, callsiteBucketAvailability)
	if err != nil {
		return nil, err
	}

	warnIfRawLagging(ctx, orgUID, rawBuckets, now, hints.RetentionRawHours)

	for _, buckets := range [][]models.ResultBucket{rollupBuckets, rawBuckets} {
		for i := range buckets {
			bucket := &buckets[i]

			byBucket := out[bucket.CheckUID]
			if byBucket == nil {
				byBucket = make(map[time.Time]BucketStats, n)
				out[bucket.CheckUID] = byBucket
			}

			acc := byBucket[bucket.BucketStart]
			acc.Add(StatsForBucket(bucket))
			byBucket[bucket.BucketStart] = acc
		}
	}

	return out, nil
}
