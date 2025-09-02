// Package maintenancewindows provides HTTP handlers for maintenance window management endpoints.
package maintenancewindows

import (
	"context"
	"database/sql"
	"errors"
	"time"

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
	case "none", "daily", "weekly", "monthly":
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
}

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

	responses := make([]MaintenanceWindowResponse, len(windows))
	for i, window := range windows {
		responses[i] = convertWindowToResponse(window)
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

	return convertWindowToResponse(window), nil
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

	return convertWindowToResponse(window), nil
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

	return convertWindowToResponse(updatedWindow), nil
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

	return s.db.DeleteMaintenanceWindow(ctx, org.UID, window.UID)
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

	return s.db.SetMaintenanceWindowChecks(ctx, windowUID, req.CheckUIDs, req.CheckGroupUIDs)
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

func convertWindowToResponse(window *models.MaintenanceWindow) MaintenanceWindowResponse {
	return MaintenanceWindowResponse{
		UID:           window.UID,
		Title:         window.Title,
		Description:   window.Description,
		StartAt:       window.StartAt,
		EndAt:         window.EndAt,
		Recurrence:    window.Recurrence,
		RecurrenceEnd: window.RecurrenceEnd,
		CreatedBy:     window.CreatedBy,
		CreatedAt:     window.CreatedAt,
		UpdatedAt:     window.UpdatedAt,
	}
}
