package heartbeat

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrNotHeartbeatCheck is returned when the check is not a heartbeat type.
	ErrNotHeartbeatCheck = errors.New("check is not a heartbeat type")
	// ErrInvalidToken is returned when the token does not match.
	ErrInvalidToken = errors.New("invalid token")
	// ErrMissingToken is returned when no token is provided.
	ErrMissingToken = errors.New("missing token")
	// ErrInvalidStatus is returned when an unsupported status is provided.
	ErrInvalidStatus = errors.New("invalid status")
)

// defaultOutputMessage returns the default output message for a heartbeat status string.
func defaultOutputMessage(status string) string {
	switch status {
	case "running":
		return "Run started"
	case "up":
		return "Heartbeat received"
	case "down":
		return "Heartbeat reported failure"
	case "error":
		return "Heartbeat reported error"
	default:
		return ""
	}
}

// parseHeartbeatStatus maps a status string to a checkerdef.Status.
func parseHeartbeatStatus(status string) (checkerdef.Status, bool) {
	switch status {
	case "running":
		return checkerdef.StatusRunning, true
	case "up":
		return checkerdef.StatusUp, true
	case "down":
		return checkerdef.StatusDown, true
	case "error":
		return checkerdef.StatusError, true
	default:
		return 0, false
	}
}

// Service provides business logic for heartbeat ingestion.
type Service struct {
	db          db.Service
	incidentSvc *incidents.Service
}

// NewService creates a new heartbeat service.
func NewService(dbService db.Service, jobSvc jobsvc.Service) *Service {
	return &Service{
		db:          dbService,
		incidentSvc: incidents.NewService(dbService, jobSvc),
	}
}

// ReceiveHeartbeat processes an incoming heartbeat ping.
func (s *Service) ReceiveHeartbeat(ctx context.Context, orgSlug, identifier, token, statusStr, message string) error {
	// Look up organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	// Look up check by UID or slug
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	// Verify it's a heartbeat check
	if checkerdef.CheckType(check.Type) != checkerdef.CheckTypeHeartbeat {
		return ErrNotHeartbeatCheck
	}

	// Validate token
	if token == "" {
		return ErrMissingToken
	}

	expectedToken, _ := check.Config["token"].(string)
	if expectedToken == "" || token != expectedToken {
		return ErrInvalidToken
	}

	// Resolve status (default to "up" for backward compatibility)
	if statusStr == "" {
		statusStr = "up"
	}

	statusStr = strings.ToLower(statusStr)

	checkerStatus, ok := parseHeartbeatStatus(statusStr)
	if !ok {
		return ErrInvalidStatus
	}

	// Resolve output message
	outputMessage := message
	if outputMessage == "" {
		outputMessage = defaultOutputMessage(statusStr)
	}

	// Save result
	resultUID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	status := int(checkerStatus)
	durationMs := float32(0)
	lastForStatus := true

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		PeriodType:      "raw",
		PeriodStart:     time.Now(),
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         make(models.JSONMap),
		Output:          models.JSONMap{"message": outputMessage},
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	if err := s.db.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	// Skip incident processing for non-terminal statuses
	if checkerStatus == checkerdef.StatusRunning {
		return nil
	}

	// Process incidents (may trigger recovery or creation)
	if err := s.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		// Log but don't fail the heartbeat
		_ = err
	}

	return nil
}
