package models

import (
	"time"

	"github.com/google/uuid"
)

// ResultStatus represents the status of a check result.
type ResultStatus int

const (
	// ResultStatusCreated indicates the check was just created and hasn't been executed yet.
	ResultStatusCreated ResultStatus = 1
	// ResultStatusRunning indicates the check process has started but not yet completed.
	ResultStatusRunning ResultStatus = 2
	// ResultStatusUp indicates the check passed successfully.
	ResultStatusUp ResultStatus = 3
	// ResultStatusDown indicates the check failed.
	ResultStatusDown ResultStatus = 4
	// ResultStatusTimeout indicates the check timed out.
	ResultStatusTimeout ResultStatus = 5
	// ResultStatusError indicates the check encountered an error.
	ResultStatusError ResultStatus = 6
	// ResultStatusDegraded is the aggregated rollup status: a window contained
	// warning(s) but no dominating failure. Stored only on aggregated rows
	// (period_type = hour/day/month) by the aggregation job, never on raw rows.
	ResultStatusDegraded ResultStatus = 7
	// ResultStatusWarning indicates the target is up but there is something to
	// report. Stored on raw rows; counts as up for availability.
	ResultStatusWarning ResultStatus = 8
)

// PeriodType values for the Result.PeriodType column.
const (
	PeriodTypeRaw   = "raw"
	PeriodTypeHour  = "hour"
	PeriodTypeDay   = "day"
	PeriodTypeMonth = "month"
)

// StatusToString converts a ResultStatus integer to its string representation.
func StatusToString(status int) string {
	switch status {
	case int(ResultStatusCreated):
		return "CREATED"
	case int(ResultStatusRunning):
		return "RUNNING"
	case int(ResultStatusUp):
		return "UP"
	case int(ResultStatusDown):
		return "DOWN"
	case int(ResultStatusTimeout):
		return "TIMEOUT"
	case int(ResultStatusError):
		return "ERROR"
	case int(ResultStatusDegraded):
		return "DEGRADED"
	case int(ResultStatusWarning):
		return "WARNING"
	default:
		return "UNKNOWN"
	}
}

// IsLifecycleMarker reports whether the status is a non-measurement lifecycle state
// (created/running) that must be excluded from availability denominators.
func (s ResultStatus) IsLifecycleMarker() bool {
	return s == ResultStatusCreated || s == ResultStatusRunning
}

// CountsAsUp reports whether a raw result counts toward availability success.
// Warning is "up with something to report" (the target is reachable), so it counts.
func (s ResultStatus) CountsAsUp() bool {
	return s == ResultStatusUp || s == ResultStatusWarning
}

// RawAvailability computes (successCount, countableTotal) over raw results, skipping
// lifecycle markers and reaped/abandoned attempts. Callers derive
// pct = 100*success/total when total > 0.
func RawAvailability(results []*Result) (int, int) {
	var success, total int
	for _, r := range results {
		if r.Status == nil || r.ExcludedFromAvailability() {
			continue
		}
		s := ResultStatus(*r.Status)
		total++
		if s.CountsAsUp() {
			success++
		}
	}
	return success, total
}

// Result represents a check execution result.
type Result struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	CheckUID        string     `bun:"check_uid,notnull"`
	PeriodType      string     `bun:"period_type,notnull,default:'raw'"`
	PeriodStart     time.Time  `bun:"period_start,notnull"`
	PeriodEnd       *time.Time `bun:"period_end"`
	Region          *string    `bun:"region"`

	// Raw result fields (period_type = 'raw')
	WorkerUID *string  `bun:"worker_uid"`
	Status    *int     `bun:"status"`
	Duration  *float32 `bun:"duration"`
	Metrics   JSONMap  `bun:"metrics,type:jsonb,nullzero"`
	Output    JSONMap  `bun:"output,type:jsonb,nullzero"`
	// Abandoned marks a row the abandoned-result reaper finalized from a stale
	// created marker (spec 2026-08-18-03): Status is set to
	// ResultStatusError like a genuine failure — the timeline keeps honest
	// evidence an attempt happened — but Abandoned=true excludes it from
	// availability math everywhere that math is done. See
	// ExcludedFromAvailability, the one predicate the hour rollup
	// (job_aggregation.go), the uptimebar union (uptimebar/bucketing.go), and
	// RawAvailability below all route through.
	Abandoned bool `bun:"abandoned,notnull"`

	// Aggregated fields (period_type = 'hour', 'day', 'month', 'year').
	// availability_pct is intentionally absent: it is derived at read time from
	// successful_checks / total_checks rather than stored (spec 2026-07-24-02).
	TotalChecks      *int     `bun:"total_checks"`
	SuccessfulChecks *int     `bun:"successful_checks"`
	DurationMin      *float32 `bun:"duration_min"`
	DurationMax      *float32 `bun:"duration_max"`
	DurationP95      *float32 `bun:"duration_p95"`
	DurationAvg      *float32 `bun:"duration_avg"`

	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// ExcludedFromAvailability reports whether a raw result must be dropped from
// both availability's numerator and denominator: a lifecycle marker (still in
// flight — created/running) OR a row the abandoned-result reaper finalized
// (terminal, but evidence of OUR infrastructure failing, not the monitored
// service — spec 2026-08-18-03). RawAvailability, the hour rollup's
// processRawResult (job_aggregation.go), and uptimebar's accumulateRaw
// (uptimebar/bucketing.go) all route through this one predicate so the three
// surfaces can never drift apart on what counts.
func (r *Result) ExcludedFromAvailability() bool {
	if r.Abandoned {
		return true
	}

	if r.Status == nil {
		return false
	}

	return ResultStatus(*r.Status).IsLifecycleMarker()
}

// Constants behind AbandonedResultThreshold — see its doc comment.
const (
	// AbandonedResultLeaseGrace mirrors the check_jobs lease formula
	// (scheduled_at + period + 30s, see checkjobsvc.Service.ClaimJobs): the
	// worst-case slack a legitimately in-flight attempt could still need on
	// top of the check's own period.
	AbandonedResultLeaseGrace = 30 * time.Second

	// AbandonedResultMultiplier pads (period + lease grace) generously so a
	// single routine worker restart mid-cycle is never mistaken for
	// abandonment. Only a check that has had no chance to produce ANY result
	// across several of its own periods is reaped.
	AbandonedResultMultiplier = 5
)

// AbandonedResultThreshold returns how old a lifecycle-marker raw row must be,
// relative to its check's period, before the abandoned-result reaper may
// finalize it: AbandonedResultMultiplier * (period + AbandonedResultLeaseGrace).
// This is "the check's period plus the worker lease timeout, with a generous
// multiplier" from spec 2026-08-18-03's Proposal.
func AbandonedResultThreshold(checkPeriod time.Duration) time.Duration {
	return AbandonedResultMultiplier * (checkPeriod + AbandonedResultLeaseGrace)
}

// NewResult creates a new raw result with generated UID.
func NewResult(orgUID, checkUID string, status ResultStatus, duration float32) *Result {
	now := time.Now()
	statusInt := int(status)

	return &Result{
		UID:             uuid.Must(uuid.NewV7()).String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		PeriodType:      "raw",
		PeriodStart:     now,
		Status:          &statusInt,
		Duration:        &duration,
		Metrics:         make(JSONMap),
		Output:          make(JSONMap),
		CreatedAt:       now,
	}
}

// ListResultsFilter provides filtering options for listing results.
type ListResultsFilter struct {
	OrganizationUID string   // Required: organization scope
	CheckUIDs       []string // Optional: filter by multiple check UIDs
	CheckTypes      []string // Optional: filter by check types (requires join with checks table)
	Regions         []string // Optional: filter by multiple regions
	PeriodTypes     []string // Optional: filter by multiple period_types ('raw', 'hour', 'day', 'month')
	Statuses        []int    // Optional: filter by multiple status integers (inclusion)
	// ExcludeStatuses drops rows whose status is in this set (NULL status is
	// never excluded). Used by aggregation work-discovery to skip buckets whose
	// only rows are lifecycle markers (created/running), which would otherwise
	// re-aggregate into degenerate rollups forever (poison-pill loop, spec
	// 2026-07-11-16). Applied independently of Statuses.
	ExcludeStatuses []int
	// RequireCheckExists, when true, drops rows whose check_uid no longer has a
	// row in `checks` (`check_uid IN (SELECT uid FROM checks)`). Soft-deleted
	// checks still have a row and keep their history — only rows orphaned by a
	// hard delete are excluded. Used by aggregation work-discovery so a single
	// FK-orphan raw row can't become a deterministic poison pill that fails the
	// rollup INSERT and permanently halts the org's aggregation (spec
	// 2026-07-12-01 §2). A no-op on Postgres by FK construction; implemented on
	// both backends for parity and defense in depth.
	RequireCheckExists bool
	PeriodStartAfter   *time.Time // Optional: filter period_start >= this value
	// Optional: filter period_start < this value (filters by period_start, not period_end)
	PeriodEndBefore *time.Time

	// Cursor-based pagination
	CursorTimestamp *time.Time // Optional: results with period_start < this timestamp
	CursorUID       *string    // Optional: for same timestamp, results with UID < this

	// Limit
	Limit int // Optional: pagination limit

	// Include check info (for joining with checks table to get slug/name)
	IncludeCheckInfo bool // Optional: whether to join with checks table

	// SkipBlobs, when true, drops the two JSON blob columns (`metrics`,
	// `output`) from the SELECT projection: the scanned rows come back with
	// Metrics and Output nil regardless of what is stored. Only for consumers
	// that need status/counts/duration and never render a blob (uptime bars,
	// badges, status-page recent results) — it trims per-request I/O on the
	// largest table in the system (spec 2026-07-24-02 §5). Default (false)
	// keeps the full row, so every other reader is unaffected.
	SkipBlobs bool
}

// ListResultsResponse wraps results with pagination info.
type ListResultsResponse struct {
	Results    []*Result // The result records
	Total      int64     // Total count of results (expensive, may be 0)
	NextCursor string    // Encoded cursor for next page (empty if no more results)
	HasMore    bool      // Whether there are more results
}

// AggregateResultsFunc computes the single rollup row for one bucket from its
// source rows and returns the UIDs of the source rows to delete. It runs inside
// CompactResults' transaction and must be pure (no DB access). Returning a nil
// rollup or an empty sourceUIDs signals "nothing measurable to compact" — the
// sources are left in place and the transaction commits without writing.
type AggregateResultsFunc func(sources []*Result) (rollup *Result, sourceUIDs []string, err error)

// CompactResultsOutcome reports what CompactResults did inside its transaction.
type CompactResultsOutcome struct {
	// Fetched is the number of source rows read for the bucket.
	Fetched int
	// SourceCount is the number of measurable source rows the aggregate function
	// selected for deletion (its returned sourceUIDs). Zero for a marker-only
	// bucket.
	SourceCount int
	// Compacted is true when the rollup row was upserted and the source rows
	// deleted (the whole transaction committed). False when there was nothing to
	// compact (no fetched rows, or a marker-only bucket).
	Compacted bool
	// DeletedCount is the number of source rows actually deleted.
	DeletedCount int64
}

// ReapAbandonedResultsOutcome reports what one abandoned-result reaper sweep
// did (see db.Service.ReapAbandonedResults).
type ReapAbandonedResultsOutcome struct {
	// Candidates is the number of raw rows found sitting in ResultStatusCreated
	// (see abandonedResultLifecycleStatuses), before threshold filtering.
	Candidates int
	// Reaped is the number of those candidates that were past
	// AbandonedResultThreshold for their check and got finalized this sweep.
	Reaped int
}
