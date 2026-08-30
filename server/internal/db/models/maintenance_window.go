package models

import (
	"time"

	"github.com/google/uuid"
)

// MaintenanceWindow represents a scheduled maintenance window for an organization.
type MaintenanceWindow struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Title           string     `bun:"title,notnull"`
	Description     *string    `bun:"description"`
	StartAt         time.Time  `bun:"start_at,notnull"`
	EndAt           time.Time  `bun:"end_at,notnull"`
	Recurrence      string     `bun:"recurrence,notnull"`
	RecurrenceEnd   *time.Time `bun:"recurrence_end"`
	CreatedBy       *string    `bun:"created_by"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewMaintenanceWindow creates a new maintenance window with generated UID.
func NewMaintenanceWindow(orgUID, title string, startAt, endAt time.Time) *MaintenanceWindow {
	now := time.Now()

	return &MaintenanceWindow{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Title:           title,
		StartAt:         startAt,
		EndAt:           endAt,
		Recurrence:      RecurrenceNone,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// MaintenanceWindowUpdate represents fields that can be updated on a maintenance window.
type MaintenanceWindowUpdate struct {
	Title         *string
	Description   *string
	StartAt       *time.Time
	EndAt         *time.Time
	Recurrence    *string
	RecurrenceEnd *time.Time
}

// ListMaintenanceWindowsFilter provides filtering options for listing maintenance windows.
type ListMaintenanceWindowsFilter struct {
	Status string // "active", "upcoming", "past", or "" for all
	Limit  int    // max results (0 = no limit)
}

// MaintenanceWindowCheck represents the association between a maintenance window and a check or check group.
type MaintenanceWindowCheck struct {
	UID                  string    `bun:"uid,pk,type:varchar(36)"`
	MaintenanceWindowUID string    `bun:"maintenance_window_uid,notnull"`
	CheckUID             *string   `bun:"check_uid"`
	CheckGroupUID        *string   `bun:"check_group_uid"`
	CreatedAt            time.Time `bun:"created_at,notnull"`
}

// Maintenance window recurrence cadences.
const (
	// RecurrenceNone marks a one-off (non-recurring) window.
	RecurrenceNone = "none"
	// RecurrenceDaily repeats the window every day at the anchor's time-of-day.
	RecurrenceDaily = "daily"
	// RecurrenceWeekly repeats the window every week on the anchor's weekday.
	RecurrenceWeekly = "weekly"
	// RecurrenceMonthly repeats the window every month on the anchor's day-of-month.
	RecurrenceMonthly = "monthly"
)

// Maintenance window lifecycle statuses (returned by Status).
const (
	// MaintenanceStatusActive means a window occurrence covers now.
	MaintenanceStatusActive = "active"
	// MaintenanceStatusUpcoming means the window has future occurrences but none cover now.
	MaintenanceStatusUpcoming = "upcoming"
	// MaintenanceStatusPast means the window has no remaining occurrences.
	MaintenanceStatusPast = "past"
)

// Occurrence is one concrete activation of a (possibly recurring) maintenance window.
type Occurrence struct {
	StartAt time.Time `json:"startAt"`
	EndAt   time.Time `json:"endAt"`
}

// maxNextOccurrences caps how many occurrences NextOccurrences will ever compute,
// guarding against pathological callers.
const maxNextOccurrences = 100

// IsActiveAt determines whether a maintenance window is active at the given time.
func IsActiveAt(window *MaintenanceWindow, target time.Time) bool {
	if window.Recurrence == RecurrenceNone {
		return !target.Before(window.StartAt) && target.Before(window.EndAt)
	}

	// Check recurrence end
	if window.RecurrenceEnd != nil && target.After(*window.RecurrenceEnd) {
		return false
	}

	// Don't match before the original start
	if target.Before(window.StartAt) {
		return false
	}

	adder := adderFor(window.Recurrence)
	if adder == nil {
		return false
	}

	duration := window.EndAt.Sub(window.StartAt)

	return isActiveForRecurrence(window.StartAt, duration, target, adder)
}

// Status returns "active", "upcoming", or "past" for the window at now.
//
// It is the canonical recurrence-status semantics shared with the frontend
// (computeMaintenanceStatus): active if IsActiveAt; else past for a non-recurring
// window after its start, or a recurring window after its RecurrenceEnd; else upcoming.
func Status(window *MaintenanceWindow, now time.Time) string {
	if IsActiveAt(window, now) {
		return MaintenanceStatusActive
	}

	if window.Recurrence == RecurrenceNone {
		if now.Before(window.StartAt) {
			return MaintenanceStatusUpcoming
		}

		return MaintenanceStatusPast
	}

	if window.RecurrenceEnd != nil && now.After(*window.RecurrenceEnd) {
		return MaintenanceStatusPast
	}

	return MaintenanceStatusUpcoming
}

// NextOccurrences returns up to n occurrences whose end is at/after from, in
// chronological order, honoring RecurrenceEnd. For recurrence "none" it returns the
// single window when not yet past, else an empty slice. The currently-active
// occurrence (if any) is included as the first entry.
func NextOccurrences(window *MaintenanceWindow, from time.Time, n int) []Occurrence {
	if n <= 0 {
		return nil
	}

	if n > maxNextOccurrences {
		n = maxNextOccurrences
	}

	duration := window.EndAt.Sub(window.StartAt)

	if window.Recurrence == RecurrenceNone {
		if !window.EndAt.After(from) {
			return nil
		}

		return []Occurrence{{StartAt: window.StartAt, EndAt: window.EndAt}}
	}

	adder := adderFor(window.Recurrence)
	if adder == nil {
		return nil
	}

	return collectOccurrences(window, from, n, duration, adder)
}

// collectOccurrences walks anchored+clamped occurrences from the window's start,
// skipping those whose end is before from, stopping at RecurrenceEnd.
func collectOccurrences(
	window *MaintenanceWindow, from time.Time, n int, duration time.Duration, adder timeAdder,
) []Occurrence {
	out := make([]Occurrence, 0, n)

	// Start scanning a couple of steps before the first occurrence that could be
	// active at from, so the currently-active occurrence is not skipped.
	startK := occurrenceIndex(window.StartAt, from, adder) - 2
	if startK < 0 {
		startK = 0
	}

	for k := startK; len(out) < n && k < startK+maxNextOccurrences; k++ {
		occStart := adder(window.StartAt, k)
		occEnd := occStart.Add(duration)

		if window.RecurrenceEnd != nil && occStart.After(*window.RecurrenceEnd) {
			break
		}

		// Skip occurrences entirely in the past relative to from.
		if !occEnd.After(from) {
			continue
		}

		out = append(out, Occurrence{StartAt: occStart, EndAt: occEnd})
	}

	return out
}

type timeAdder func(t time.Time, n int) time.Time

func adderFor(recurrence string) timeAdder {
	switch recurrence {
	case RecurrenceDaily:
		return addDays
	case RecurrenceWeekly:
		return addWeeks
	case RecurrenceMonthly:
		return addMonths
	default:
		return nil
	}
}

func addDays(t time.Time, n int) time.Time {
	return t.AddDate(0, 0, n)
}

func addWeeks(t time.Time, n int) time.Time {
	return t.AddDate(0, 0, n*7)
}

// addMonths shifts base by n months, clamping the day to the last day of the
// target month (Jan 31 +1mo -> Feb 28/29, not Mar 3).
func addMonths(base time.Time, n int) time.Time {
	year, month, day := base.Date()
	first := time.Date(
		year, month, 1,
		base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location(),
	)
	target := first.AddDate(0, n, 0) // first-of-month never overflows

	last := daysIn(target.Year(), target.Month())
	if day > last {
		day = last
	}

	return time.Date(
		target.Year(), target.Month(), day,
		base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location(),
	)
}

// daysIn returns the number of days in the given month (handles leap years).
func daysIn(year int, month time.Month) int {
	// Day 0 of the next month is the last day of this month.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// occurrenceIndex returns an estimate of the occurrence index k such that
// adder(start, k) is at or just before target. It overshoots/undershoots by at
// most a step or two near month boundaries, so callers must clamp around it.
func occurrenceIndex(start, target time.Time, adder timeAdder) int {
	if !target.After(start) {
		return 0
	}

	// Calibrate the per-step size from the first step off the anchor, then estimate.
	step := adder(start, 1).Sub(start)
	if step <= 0 {
		return 0
	}

	k := int(target.Sub(start) / step)
	if k < 0 {
		k = 0
	}

	return k
}

// isActiveForRecurrence checks if target falls within any occurrence of a recurring
// window. Every occurrence is anchored on the ORIGINAL start (adder(start, k)) so the
// month-end clamp does not accumulate drift across occurrences.
func isActiveForRecurrence(
	start time.Time, duration time.Duration, target time.Time, adder timeAdder,
) bool {
	// Estimate the occurrence index, then scan a small window around it to absorb
	// month-length variance, finding the latest occurrence whose start <= target.
	k := occurrenceIndex(start, target, adder)

	best := -1

	for i := k - 2; i <= k+2; i++ {
		if i < 0 {
			continue
		}

		occStart := adder(start, i)
		if !occStart.After(target) { // occStart <= target
			best = i
		}
	}

	if best < 0 {
		return false
	}

	occStart := adder(start, best)
	occEnd := occStart.Add(duration)

	return !target.Before(occStart) && target.Before(occEnd)
}
