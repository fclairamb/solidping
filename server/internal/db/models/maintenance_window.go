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
	Recurrence      string     `bun:"recurrence,notnull,default:'none'"`
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
		Recurrence:      "none",
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

// IsActiveAt determines whether a maintenance window is active at the given time.
func IsActiveAt(window *MaintenanceWindow, target time.Time) bool {
	if window.Recurrence == "none" {
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

	duration := window.EndAt.Sub(window.StartAt)

	switch window.Recurrence {
	case "daily":
		return isActiveForRecurrence(window.StartAt, duration, target, addDays)
	case "weekly":
		return isActiveForRecurrence(window.StartAt, duration, target, addWeeks)
	case "monthly":
		return isActiveForRecurrence(window.StartAt, duration, target, addMonths)
	default:
		return false
	}
}

type timeAdder func(t time.Time, n int) time.Time

func addDays(t time.Time, n int) time.Time {
	return t.AddDate(0, 0, n)
}

func addWeeks(t time.Time, n int) time.Time {
	return t.AddDate(0, 0, n*7)
}

func addMonths(t time.Time, n int) time.Time {
	return t.AddDate(0, n, 0)
}

// isActiveForRecurrence checks if target falls within any occurrence of a recurring window.
func isActiveForRecurrence(start time.Time, duration time.Duration, target time.Time, adder timeAdder) bool {
	// Step forward to find the occurrence just before or at target
	current := start
	for {
		next := adder(current, 1)
		if next.After(target) {
			break
		}
		current = next
	}

	// Check if target is within this occurrence
	occurrenceEnd := current.Add(duration)

	return !target.Before(current) && target.Before(occurrenceEnd)
}
