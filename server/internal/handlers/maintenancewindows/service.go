// Package maintenancewindows provides HTTP handlers for maintenance window management endpoints.
package maintenancewindows

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrMaintenanceWindowNotFound is returned when a maintenance window is not found.
	ErrMaintenanceWindowNotFound = errors.New("maintenance window not found")
	// ErrTitleRequired is returned when the title is missing.
	ErrTitleRequired = errors.New("title is required")
	// ErrInvalidTimeRange is returned when end_at is not after start_at.
	ErrInvalidTimeRange = errors.New("end must be after start")
	// ErrInvalidRecurrence is returned when the recurrence value is not valid.
	ErrInvalidRecurrence = errors.New("recurrence must be none, daily, weekly, or monthly")
)

// isValidRecurrence checks if a recurrence value is valid.
func isValidRecurrence(recurrence string) bool {
	switch recurrence {
	case models.RecurrenceNone, models.RecurrenceDaily, models.RecurrenceWeekly, models.RecurrenceMonthly:
		return true
	default:
		return false
	}
}

// Service provides business logic for maintenance window management.
type Service struct {
	db db.Service
}

// NewService creates a new maintenance windows service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// MaintenanceWindowResponse represents a maintenance window in API responses.
type MaintenanceWindowResponse struct {
	UID           string     `json:"uid"`
	Title         string     `json:"title"`
	Description   *string    `json:"description,omitempty"`
	StartAt       time.Time  `json:"startAt"`
	EndAt         time.Time  `json:"endAt"`
	Recurrence    string     `json:"recurrence"`
	RecurrenceEnd *time.Time `json:"recurrenceEnd,omitempty"`
	CreatedBy     *string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	// Status is the canonical lifecycle of the window at response time:
	// "active", "upcoming", or "past". Read-only, server-computed.
	Status string `json:"status"`
	// NextOccurrences lists up to the next few concrete activations (currently
	// active one first). Read-only, server-computed.
	NextOccurrences []models.Occurrence `json:"nextOccurrences"`
}

// nextOccurrencesCount is how many upcoming occurrences responses surface
// (matches the frontend preview).
const nextOccurrencesCount = 3

// CreateRequest represents a request to create a new maintenance window.
type CreateRequest struct {
	Title         string     `json:"title"`
	Description   *string    `json:"description"`
	StartAt       time.Time  `json:"startAt"`
	EndAt         time.Time  `json:"endAt"`
	Recurrence    string     `json:"recurrence"`
	RecurrenceEnd *time.Time `json:"recurrenceEnd"`
}

// UpdateRequest represents a request to update a maintenance window.
type UpdateRequest struct {
	Title         *string    `json:"title,omitempty"`
	Description   *string    `json:"description,omitempty"`
	StartAt       *time.Time `json:"startAt,omitempty"`
	EndAt         *time.Time `json:"endAt,omitempty"`
	Recurrence    *string    `json:"recurrence,omitempty"`
	RecurrenceEnd *time.Time `json:"recurrenceEnd,omitempty"`
}

// SetChecksRequest represents a request to set the checks for a maintenance window.
type SetChecksRequest struct {
	CheckUIDs      []string `json:"checkUids"`
	CheckGroupUIDs []string `json:"checkGroupUids"`
}

// MaintenanceWindowCheckResponse represents a check association in API responses.
type MaintenanceWindowCheckResponse struct {
	UID           string  `json:"uid"`
	CheckUID      *string `json:"checkUid,omitempty"`
	CheckGroupUID *string `json:"checkGroupUid,omitempty"`
}

// ListMaintenanceWindows retrieves all maintenance windows for an organization.
func (s *Service) ListMaintenanceWindows(
	ctx context.Context, orgSlug, status string, limit int,
) ([]MaintenanceWindowResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	filter := models.ListMaintenanceWindowsFilter{
		Status: status,
		Limit:  limit,
	}

	windows, err := s.db.ListMaintenanceWindows(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	responses := make([]MaintenanceWindowResponse, len(windows))
	for i, window := range windows {
		responses[i] = convertWindowToResponse(window, now)
	}

	return responses, nil
}

// CreateMaintenanceWindow creates a new maintenance window.
func (s *Service) CreateMaintenanceWindow(
	ctx context.Context, orgSlug string, req *CreateRequest,
) (MaintenanceWindowResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return MaintenanceWindowResponse{}, ErrOrganizationNotFound
	}

	if err := validateCreateRequest(req); err != nil {
		return MaintenanceWindowResponse{}, err
	}

	window := models.NewMaintenanceWindow(org.UID, req.Title, req.StartAt, req.EndAt)
	window.Description = req.Description
	window.RecurrenceEnd = req.RecurrenceEnd

	if req.Recurrence != "" {
		window.Recurrence = req.Recurrence
	}

	if err := s.db.CreateMaintenanceWindow(ctx, window); err != nil {
		return MaintenanceWindowResponse{}, err
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeMaintenanceWindowCreated,
		auditTarget(window), models.JSONMap{
			"start_at":   window.StartAt.UTC().Format(time.RFC3339),
			"end_at":     window.EndAt.UTC().Format(time.RFC3339),
			"recurrence": window.Recurrence,
		})

	return convertWindowToResponse(window, time.Now().UTC()), nil
}

// auditTarget names a maintenance window for the audit trail.
func auditTarget(window *models.MaintenanceWindow) audit.Target {
	return audit.Target{Type: "maintenance_window", UID: window.UID, Name: window.Title}
}

// auditSnapshot is the scalar shape the audit trail diffs an update against.
func auditSnapshot(window *models.MaintenanceWindow) map[string]any {
	description := ""
	if window.Description != nil {
		description = *window.Description
	}

	recurrenceEnd := ""
	if window.RecurrenceEnd != nil {
		recurrenceEnd = window.RecurrenceEnd.UTC().Format(time.RFC3339)
	}

	return map[string]any{
		"title":          window.Title,
		"description":    description,
		"start_at":       window.StartAt.UTC().Format(time.RFC3339),
		"end_at":         window.EndAt.UTC().Format(time.RFC3339),
		"recurrence":     window.Recurrence,
		"recurrence_end": recurrenceEnd,
	}
}

// GetMaintenanceWindow retrieves a single maintenance window by UID.
func (s *Service) GetMaintenanceWindow(
	ctx context.Context, orgSlug, uid string,
) (MaintenanceWindowResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return MaintenanceWindowResponse{}, ErrOrganizationNotFound
	}

	window, err := s.db.GetMaintenanceWindow(ctx, org.UID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MaintenanceWindowResponse{}, ErrMaintenanceWindowNotFound
		}

		return MaintenanceWindowResponse{}, err
	}

	return convertWindowToResponse(window, time.Now().UTC()), nil
}

// UpdateMaintenanceWindow updates an existing maintenance window.
func (s *Service) UpdateMaintenanceWindow(
	ctx context.Context, orgSlug, uid string, req UpdateRequest,
) (MaintenanceWindowResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return MaintenanceWindowResponse{}, ErrOrganizationNotFound
	}

	window, err := s.db.GetMaintenanceWindow(ctx, org.UID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MaintenanceWindowResponse{}, ErrMaintenanceWindowNotFound
		}

		return MaintenanceWindowResponse{}, err
	}

	// Validate recurrence if provided
	if req.Recurrence != nil && !isValidRecurrence(*req.Recurrence) {
		return MaintenanceWindowResponse{}, ErrInvalidRecurrence
	}

	// Validate time range if both are provided, or if one is provided check against existing
	effectiveStart := window.StartAt
	if req.StartAt != nil {
		effectiveStart = *req.StartAt
	}

	effectiveEnd := window.EndAt
	if req.EndAt != nil {
		effectiveEnd = *req.EndAt
	}

	if !effectiveEnd.After(effectiveStart) {
		return MaintenanceWindowResponse{}, ErrInvalidTimeRange
	}

	update := models.MaintenanceWindowUpdate{
		Title:         req.Title,
		Description:   req.Description,
		StartAt:       req.StartAt,
		EndAt:         req.EndAt,
		Recurrence:    req.Recurrence,
		RecurrenceEnd: req.RecurrenceEnd,
	}

	if errUpdate := s.db.UpdateMaintenanceWindow(ctx, window.UID, update); errUpdate != nil {
		return MaintenanceWindowResponse{}, errUpdate
	}

	// Fetch updated window
	updatedWindow, errFetch := s.db.GetMaintenanceWindow(ctx, org.UID, window.UID)
	if errFetch != nil {
		return MaintenanceWindowResponse{}, errFetch
	}

	if changed, safe := audit.Changes(auditSnapshot(window), auditSnapshot(updatedWindow)); len(changed) > 0 {
		audit.Record(ctx, s.db, org.UID, models.EventTypeMaintenanceWindowUpdated,
			auditTarget(updatedWindow), audit.ChangePayload(changed, safe, nil, nil))
	}

	return convertWindowToResponse(updatedWindow, time.Now().UTC()), nil
}

// DeleteMaintenanceWindow deletes a maintenance window by UID (soft delete).
func (s *Service) DeleteMaintenanceWindow(ctx context.Context, orgSlug, uid string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	window, err := s.db.GetMaintenanceWindow(ctx, org.UID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMaintenanceWindowNotFound
		}

		return err
	}

	if err := s.db.DeleteMaintenanceWindow(ctx, org.UID, window.UID); err != nil {
		return err
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeMaintenanceWindowDeleted, auditTarget(window), nil)

	return nil
}

// ListChecks retrieves the check associations for a maintenance window.
func (s *Service) ListChecks(
	ctx context.Context, orgSlug, windowUID string,
) ([]MaintenanceWindowCheckResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Verify window exists
	_, err = s.db.GetMaintenanceWindow(ctx, org.UID, windowUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMaintenanceWindowNotFound
		}

		return nil, err
	}

	checks, err := s.db.ListMaintenanceWindowChecks(ctx, windowUID)
	if err != nil {
		return nil, err
	}

	responses := make([]MaintenanceWindowCheckResponse, len(checks))
	for i, check := range checks {
		responses[i] = MaintenanceWindowCheckResponse{
			UID:           check.UID,
			CheckUID:      check.CheckUID,
			CheckGroupUID: check.CheckGroupUID,
		}
	}

	return responses, nil
}

// SetChecks sets the check associations for a maintenance window.
func (s *Service) SetChecks(
	ctx context.Context, orgSlug, windowUID string, req SetChecksRequest,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	// Verify window exists
	_, err = s.db.GetMaintenanceWindow(ctx, org.UID, windowUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMaintenanceWindowNotFound
		}

		return err
	}

	if err := s.db.SetMaintenanceWindowChecks(ctx, windowUID, req.CheckUIDs, req.CheckGroupUIDs); err != nil {
		return err
	}

	// A window with zero attached checks suppresses NOTHING, so "who changed
	// the attachment set, and to how many" is exactly the fact an operator
	// needs after a page that should have been suppressed.
	audit.Record(ctx, s.db, org.UID, models.EventTypeMaintenanceWindowUpdated,
		audit.Target{Type: "maintenance_window", UID: windowUID},
		audit.ChangePayload(nil, nil, []string{"checks"}, models.JSONMap{
			"check_count":       len(req.CheckUIDs),
			"check_group_count": len(req.CheckGroupUIDs),
		}))

	return nil
}

func validateCreateRequest(req *CreateRequest) error {
	if req.Title == "" {
		return ErrTitleRequired
	}

	if !req.EndAt.After(req.StartAt) {
		return ErrInvalidTimeRange
	}

	if req.Recurrence != "" && !isValidRecurrence(req.Recurrence) {
		return ErrInvalidRecurrence
	}

	return nil
}

func convertWindowToResponse(window *models.MaintenanceWindow, now time.Time) MaintenanceWindowResponse {
	return MaintenanceWindowResponse{
		UID:             window.UID,
		Title:           window.Title,
		Description:     window.Description,
		StartAt:         window.StartAt,
		EndAt:           window.EndAt,
		Recurrence:      window.Recurrence,
		RecurrenceEnd:   window.RecurrenceEnd,
		CreatedBy:       window.CreatedBy,
		CreatedAt:       window.CreatedAt,
		UpdatedAt:       window.UpdatedAt,
		Status:          models.Status(window, now),
		NextOccurrences: models.NextOccurrences(window, now, nextOccurrencesCount),
	}
}
