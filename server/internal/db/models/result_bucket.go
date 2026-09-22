package models

import (
	"errors"
	"fmt"
	"time"
)

// Errors returned by ResultBucketFilter.Validate.
var (
	// ErrResultBucketsNoOrganization is returned when the filter names no
	// organization — every index on `results` leads with organization_uid.
	ErrResultBucketsNoOrganization = errors.New("result buckets: organization uid is required")
	// ErrResultBucketsMixedTier is returned when the requested period types
	// straddle the raw/rollup split (or name nothing at all).
	//
	// Same guard, same reason as ErrRecentResultsMixedTier: both useful indexes
	// on `results` are PARTIAL on `period_type = 'raw'` / `!= 'raw'`, so a
	// predicate straddling the split is implied by neither and can only be
	// answered by a sequential scan of the largest table in the system. One call
	// per side, never one call for both.
	ErrResultBucketsMixedTier = errors.New(
		"result buckets: the period types must sit entirely on one side of the raw/rollup index split")
	// ErrResultBucketsNoBucketDuration is returned for a non-positive bucket
	// width, which has no bucket grid to group by.
	ErrResultBucketsNoBucketDuration = errors.New("result buckets: a positive bucket duration is required")
	// ErrResultBucketsNoPeriodStart is returned when the filter carries no
	// lower bound. An unbounded aggregate over `results` is exactly the scan
	// this query shape exists to avoid.
	ErrResultBucketsNoPeriodStart = errors.New("result buckets: a period_start lower bound is required")
)

// ProlepticEpochOffsetSeconds is the number of seconds from 0001-01-01
// 00:00:00 UTC to the Unix epoch.
//
// It is load-bearing, not trivia: Go's time.Truncate rounds down relative to
// the ZERO time.Time (0001-01-01 00:00:00 UTC), not to the Unix epoch, and
// uptimebar has always keyed its buckets on PeriodStart.Truncate(width). For a
// width that divides 24 h the two origins agree; for one that does not (7 h,
// 90 min — both legal on the availability API) they put the same row in
// different buckets. Any SQL that bins period_start must therefore bin against
// THIS origin, so the database and the Go reference fold agree for every width.
//
// Postgres expresses it directly (date_bin's origin argument); SQLite has to
// shift epoch seconds by this constant, floor, and shift back.
const ProlepticEpochOffsetSeconds = 62135596800

// SlowSampleThresholdMillis is the response time above which a probe counts as
// slow (strictly above). Milliseconds, matching Result.Duration.
//
// It lives in models rather than in uptimebar — whose accumulators are its only
// semantic owner — because the per-bucket aggregate SQL has to encode it too,
// and the dialect packages cannot import uptimebar (it imports models).
// uptimebar.SlowSampleThresholdMillis aliases this, so nothing that reads the
// threshold had to move.
const SlowSampleThresholdMillis = 1000.0

// allResultStatuses returns every status a persisted result row may carry. It
// backs the two status-set helpers below, so a status added to the ResultStatus
// block has to be listed here once and then reaches every SQL predicate derived
// from it (TestResultStatusSetsCoverEveryStatus pins that it stays complete).
func allResultStatuses() []ResultStatus {
	return []ResultStatus{
		ResultStatusCreated,
		ResultStatusRunning,
		ResultStatusUp,
		ResultStatusDown,
		ResultStatusTimeout,
		ResultStatusError,
		ResultStatusDegraded,
		ResultStatusWarning,
		ResultStatusAbandoned,
	}
}

// resultStatusesWhere materializes the status codes matching a predicate, as the
// ints the columns actually store.
func resultStatusesWhere(match func(ResultStatus) bool) []int {
	statuses := allResultStatuses()
	codes := make([]int, 0, len(statuses))

	for _, status := range statuses {
		if match(status) {
			codes = append(codes, int(status))
		}
	}

	return codes
}

// CountsAsUpStatuses returns the status codes ResultStatus.CountsAsUp accepts,
// for an `status IN (?)` predicate.
//
// It is DERIVED from the predicate rather than transcribed next to it. A SQL
// aggregate that hard-coded `IN (3, 8)` would silently disagree with the Go
// accumulators the day a status is added or reclassified — and the disagreement
// would be an availability percentage, not a compile error.
func CountsAsUpStatuses() []int {
	return resultStatusesWhere(ResultStatus.CountsAsUp)
}

// ExcludedFromAvailabilityStatuses returns the status codes
// ResultStatus.ExcludedFromAvailability drops from both numerator and
// denominator — the created/running lifecycle markers plus the reaper's
// `abandoned`. Derived, for the same reason as CountsAsUpStatuses.
func ExcludedFromAvailabilityStatuses() []int {
	return resultStatusesWhere(ResultStatus.ExcludedFromAvailability)
}

// ResultBucketFilter describes a per-(check, bucket) aggregate over `results`:
// which rows to fold, and how wide a bucket is.
//
// It is deliberately NOT a ListResultsFilter. This query never returns rows, it
// returns counters — one per (check_uid, bucket_start) — so it has no cursor, no
// ordering, no row limit and no blob switch. Those all exist on ListResults
// because ListResults ships rows; a consumer that only wants eleven numbers per
// bucket was paying for all of it (spec 2026-09-22-05: 267 449 rows shipped to
// Go, sorted to disk, to produce 400 buckets).
type ResultBucketFilter struct {
	// OrganizationUID scopes the aggregate. Required.
	OrganizationUID string
	// CheckUIDs are the checks to fold. An empty list yields no buckets.
	CheckUIDs []string
	// PeriodTypes are the aggregation tiers to fold. They MUST all sit on ONE
	// side of the raw/rollup split (see PeriodTypesTierSide) — Validate rejects
	// anything else, and the implementations restate that side as an explicit
	// predicate so both engines can use the matching partial index.
	PeriodTypes []string
	// Regions optionally narrows to a set of probe regions. It is a WHERE
	// clause and NEVER a GROUP BY key: a region-scoped read wants one bucket
	// per (check, bucket) holding that region's probes, not one bucket per
	// region. Empty means every region, which sums across them — the historical
	// behavior uptimebar's callers depend on.
	Regions []string
	// PeriodStartAfter is the inclusive period_start lower bound. Required.
	PeriodStartAfter time.Time
	// PeriodStartBefore is an optional EXCLUSIVE period_start upper bound,
	// mirroring ListResultsFilter.PeriodEndBefore (which also filters on
	// period_start despite its name). uptimebar's whole-window fold bounds both
	// edges of its window; the per-bucket strip bounds only the lower one.
	PeriodStartBefore *time.Time
	// BucketDuration is the bucket width. Must be > 0. Rows are binned against
	// ProlepticEpochOffsetSeconds' origin so the grid matches
	// time.Truncate(BucketDuration) exactly.
	BucketDuration time.Duration
}

// TierSide reports which side of the raw/rollup index split this filter reads.
func (f *ResultBucketFilter) TierSide() PeriodTierSide {
	return PeriodTypesTierSide(f.PeriodTypes)
}

// Validate rejects a filter no dialect may execute. The mixed-tier rule is the
// important one — see ErrResultBucketsMixedTier.
func (f *ResultBucketFilter) Validate() error {
	if f.OrganizationUID == "" {
		return ErrResultBucketsNoOrganization
	}

	if f.BucketDuration <= 0 {
		return fmt.Errorf("%w: %s", ErrResultBucketsNoBucketDuration, f.BucketDuration)
	}

	if f.PeriodStartAfter.IsZero() {
		return ErrResultBucketsNoPeriodStart
	}

	if f.TierSide() == PeriodTierMixed {
		return fmt.Errorf("%w: %v", ErrResultBucketsMixedTier, f.PeriodTypes)
	}

	return nil
}

// ResultBucket is one (check, bucket) group of the aggregate: exactly the
// counters uptimebar.BucketStats folds, computed by the database.
//
// Field-for-field it mirrors what uptimebar's accumulateRaw / accumulateAgg
// produce for the same rows, and that equivalence is pinned by a parity test per
// dialect which folds the same fixture both ways. The Go accumulators remain the
// reference semantics; this struct is the wire they arrive on.
type ResultBucket struct {
	// CheckUID and BucketStart are the group key. BucketStart is UTC.
	CheckUID    string
	BucketStart time.Time

	// Total / Up are the countable probes and the successful ones. Raw:
	// non-excluded rows, and rows whose status CountsAsUp. Rollup:
	// SUM(total_checks) / SUM(successful_checks).
	Total int
	Up    int
	// MaintTotal / MaintUp are strict SUBSETS of Total / Up — the share
	// recorded while a maintenance window covered the check.
	MaintTotal int
	MaintUp    int

	// DurCnt / DurSum are the response-time accumulator: on the raw tier one
	// count per row carrying a duration, on the rollup tier duration_avg
	// weighted by total_checks.
	DurCnt int
	DurSum float64

	// DurMin / DurMax are the extremes folded into the bucket, nil when
	// DurExtremaCnt is 0 — never a confident zero for a bucket that measured
	// nothing. DurExtremaCnt counts the CONTRIBUTIONS (what
	// BucketStats.foldExtrema counts), not the distinct values.
	DurMin        *float32
	DurMax        *float32
	DurExtremaCnt int32

	// SlowSamples counts raw probes strictly above SlowSampleThresholdMillis
	// (exact: one row is one probe). SlowPeaks counts ROLLUP rows whose
	// duration_max exceeded it — a different unit, never added to the other.
	// Each tier populates one and leaves the other at 0.
	SlowSamples int32
	SlowPeaks   int32

	// Rows is how many source rows were folded into this bucket. It is
	// diagnostics only — nothing derives a user-visible number from it — and it
	// is the one figure the aggregate can report that the counters cannot (a
	// rollup bucket's Total counts probes, not rows).
	Rows int
	// OldestPeriodStart is the oldest source row's period_start in this bucket.
	// uptimebar reads MIN over the raw buckets to detect aggregation lag, which
	// used to require scanning every returned row.
	OldestPeriodStart time.Time
}
