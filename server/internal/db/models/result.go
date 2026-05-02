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
	default:
		return "UNKNOWN"
	}
}

// LastStatusChange represents the last time a check's status changed.
type LastStatusChange struct {
	Time   time.Time `json:"time"`
	Status string    `json:"status"`
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
	WorkerUID     *string  `bun:"worker_uid"`
	Status        *int     `bun:"status"`
	Duration      *float32 `bun:"duration"`
	Metrics       JSONMap  `bun:"metrics,type:jsonb,nullzero"`
	Output        JSONMap  `bun:"output,type:jsonb,nullzero"`
	LastForStatus *bool    `bun:"last_for_status"`

	// Aggregated fields (period_type = 'hour', 'day', 'month', 'year')
	TotalChecks      *int     `bun:"total_checks"`
	SuccessfulChecks *int     `bun:"successful_checks"`
	AvailabilityPct  *float64 `bun:"availability_pct"`
	DurationMin      *float32 `bun:"duration_min"`
	DurationMax      *float32 `bun:"duration_max"`
	DurationP95      *float32 `bun:"duration_p95"`

	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
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
	OrganizationUID  string     // Required: organization scope
	CheckUIDs        []string   // Optional: filter by multiple check UIDs
	CheckTypes       []string   // Optional: filter by check types (requires join with checks table)
	Regions          []string   // Optional: filter by multiple regions
	PeriodTypes      []string   // Optional: filter by multiple period_types ('raw', 'hour', 'day', 'month')
	Statuses         []int      // Optional: filter by multiple status integers
	PeriodStartAfter *time.Time // Optional: filter period_start >= this value
	// Optional: filter period_start < this value (filters by period_start, not period_end)
	PeriodEndBefore *time.Time

	// Cursor-based pagination
	CursorTimestamp *time.Time // Optional: results with period_start < this timestamp
	CursorUID       *string    // Optional: for same timestamp, results with UID < this

	// Limit
	Limit int // Optional: pagination limit

	// Include check info (for joining with checks table to get slug/name)
	IncludeCheckInfo bool // Optional: whether to join with checks table
}

// ListResultsResponse wraps results with pagination info.
type ListResultsResponse struct {
	Results    []*Result // The result records
	Total      int64     // Total count of results (expensive, may be 0)
	NextCursor string    // Encoded cursor for next page (empty if no more results)
	HasMore    bool      // Whether there are more results
}
