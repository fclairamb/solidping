package models

import "time"

// IncidentClockUpdate captures the FirstFailureAt / FirstSuccessSinceFailureAt
// transition for a single check result. Each field is tri-state: a non-nil
// pointer sets the column, a true Clear* flag sets it to NULL, and the zero
// value (nil pointer + false flag) leaves the column untouched.
//
// It is consumed by UpdateCheckStatusAndClocks, which writes the check's
// status, streak and both incident clocks in one atomic UPDATE.
type IncidentClockUpdate struct {
	FirstFailureAt                  *time.Time
	ClearFirstFailureAt             bool
	FirstSuccessSinceFailureAt      *time.Time
	ClearFirstSuccessSinceFailureAt bool
}
