// Package incidents provides incident management functionality.
package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// Service errors.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrIncidentNotFound     = errors.New("incident not found")
)

// Service provides incident management functionality.
type Service struct {
	db      db.Service
	jobsSvc jobsvc.Service
}

// NewService creates a new incident service.
func NewService(dbService db.Service, jobsSvc jobsvc.Service) *Service {
	return &Service{
		db:      dbService,
		jobsSvc: jobsSvc,
	}
}

// ProcessCheckResult processes a check result and manages incidents.
// This is the main entry point called after each check execution.
func (s *Service) ProcessCheckResult(ctx context.Context, check *models.Check, result *models.Result) error {
	if result.Status == nil {
		return nil // Skip results without status
	}

	// Skip incident processing if the check is in an active maintenance window
	inMaintenance, mwErr := s.db.IsCheckInActiveMaintenance(ctx, check.UID)
	if mwErr != nil {
		slog.WarnContext(ctx, "Failed to check maintenance window status",
			"checkUID", check.UID, "error", mwErr)
	}

	if inMaintenance {
		slog.InfoContext(ctx, "Skipping incident processing: check is in maintenance window",
			"checkUID", check.UID)

		return nil
	}

	resultStatus := models.ResultStatus(*result.Status)

	// Determine if this is a success or failure
	isSuccess := resultStatus == models.ResultStatusUp
	isFailure := resultStatus == models.ResultStatusDown ||
		resultStatus == models.ResultStatusTimeout ||
		resultStatus == models.ResultStatusError

	if !isSuccess && !isFailure {
		return nil // Skip initial or unknown statuses
	}

	// Calculate new status
	var newStatus models.CheckStatus
	if isSuccess {
		newStatus = models.CheckStatusUp
	} else {
		newStatus = models.CheckStatusDown
	}

	// Update check status tracking
	now := time.Now()
	var statusChangedAt *time.Time
	var newStreak int

	if check.Status == newStatus {
		// Same status: increment streak
		newStreak = check.StatusStreak + 1
	} else {
		// Status changed: reset streak and update timestamp
		newStreak = 1
		statusChangedAt = &now
	}

	// Update check status in database
	if err := s.db.UpdateCheckStatus(ctx, check.UID, newStatus, newStreak, statusChangedAt); err != nil {
		return fmt.Errorf("failed to update check status: %w", err)
	}

	// Update local check object for incident logic
	check.Status = newStatus
	check.StatusStreak = newStreak
	if statusChangedAt != nil {
		check.StatusChangedAt = statusChangedAt
	}

	// Find active incident
	incident, err := s.db.FindActiveIncidentByCheckUID(ctx, check.UID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to find active incident: %w", err)
	}

	// Handle based on result
	if isFailure {
		return s.handleFailure(ctx, check, result, incident)
	}

	return s.handleSuccess(ctx, check, result, incident)
}

// handleFailure handles a failed check result.
func (s *Service) handleFailure(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil && check.StatusStreak < check.IncidentThreshold {
		// Not enough consecutive failures yet
		return nil
	}

	if incident == nil {
		// Threshold met - try to reopen a recently resolved incident before creating a new one
		return s.createOrReopenIncident(ctx, check, result)
	}

	// Update existing incident
	newFailureCount := incident.FailureCount + 1
	update := models.IncidentUpdate{
		FailureCount: &newFailureCount,
	}

	// Check if we should escalate (once per incident)
	if incident.EscalatedAt == nil && newFailureCount >= check.EscalationThreshold {
		now := time.Now()
		update.EscalatedAt = &now
		// Emit escalation event
		if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentEscalated, incident, models.JSONMap{
			"failure_count":        newFailureCount,
			"escalation_threshold": check.EscalationThreshold,
		}); err != nil {
			return fmt.Errorf("failed to emit escalation event: %w", err)
		}
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to update incident: %w", err)
	}

	return nil
}

// handleSuccess handles a successful check result.
func (s *Service) handleSuccess(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	if incident == nil {
		// No active incident - nothing to do
		return nil
	}

	// Use adaptive recovery threshold based on relapse count
	threshold := effectiveRecoveryThreshold(check, incident)
	if check.StatusStreak >= threshold {
		// Resolve the incident
		return s.resolveIncident(ctx, check, result, incident)
	}

	// Not enough consecutive successes yet - incident remains active
	return nil
}

// createIncident creates a new incident.
func (s *Service) createIncident(ctx context.Context, check *models.Check, result *models.Result) error {
	// Generate title
	title := s.generateIncidentTitle(check)

	incident := models.NewIncident(check.OrganizationUID, check.UID, result.PeriodStart, title)

	if err := s.db.CreateIncident(ctx, incident); err != nil {
		return fmt.Errorf("failed to create incident: %w", err)
	}

	// Emit incident created event
	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentCreated, incident, models.JSONMap{
		"check_uid":  check.UID,
		"check_slug": check.Slug,
		"started_at": result.PeriodStart,
		"result_uid": result.UID,
	}); err != nil {
		return fmt.Errorf("failed to emit incident created event: %w", err)
	}

	return nil
}

// resolveIncident resolves an active incident.
func (s *Service) resolveIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	resolvedState := models.IncidentStateResolved
	resolvedAt := result.PeriodStart

	update := models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to resolve incident: %w", err)
	}

	// Calculate duration
	durationSeconds := int64(resolvedAt.Sub(incident.StartedAt).Seconds())

	// Emit incident resolved event
	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentResolved, incident, models.JSONMap{
		"check_uid":        check.UID,
		"check_slug":       check.Slug,
		"resolved_at":      resolvedAt,
		"duration_seconds": durationSeconds,
		"total_failures":   incident.FailureCount,
	}); err != nil {
		return fmt.Errorf("failed to emit incident resolved event: %w", err)
	}

	return nil
}

// createOrReopenIncident tries to reopen a recently resolved incident, or creates a new one.
func (s *Service) createOrReopenIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) error {
	reopened, err := s.tryReopenIncident(ctx, check, result)
	if err != nil {
		return err
	}
	if reopened {
		return nil
	}

	return s.createIncident(ctx, check, result)
}

const defaultCooldownMultiplier = 5

// calculateCooldown returns the cooldown duration for incident reopening based on check period.
func calculateCooldown(check *models.Check) time.Duration {
	multiplier := defaultCooldownMultiplier
	if check.ReopenCooldownMultiplier != nil {
		multiplier = *check.ReopenCooldownMultiplier
	}

	if multiplier == 0 {
		return 0 // Reopening disabled
	}

	period := time.Duration(check.Period)
	cooldown := time.Duration(multiplier) * period

	const minCooldown = 2 * time.Minute
	const maxCooldown = 30 * time.Minute

	if cooldown < minCooldown {
		cooldown = minCooldown
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return cooldown
}

const defaultMaxAdaptiveIncrease = 5

// effectiveRecoveryThreshold returns the adaptive recovery threshold for an incident.
// It increases by 1 per relapse, capped by the check's MaxAdaptiveIncrease setting.
func effectiveRecoveryThreshold(check *models.Check, incident *models.Incident) int {
	maxIncrease := defaultMaxAdaptiveIncrease
	if check.MaxAdaptiveIncrease != nil {
		maxIncrease = *check.MaxAdaptiveIncrease
	}

	increase := incident.RelapseCount
	if increase > maxIncrease {
		increase = maxIncrease
	}

	return check.RecoveryThreshold + increase
}

// tryReopenIncident looks for a recently resolved incident and reopens it if appropriate.
// Returns true if an incident was reopened.
func (s *Service) tryReopenIncident(
	ctx context.Context, check *models.Check, result *models.Result,
) (bool, error) {
	cooldown := calculateCooldown(check)
	if cooldown == 0 {
		return false, nil // Reopening disabled for this check
	}
	since := time.Now().Add(-cooldown)

	incident, err := s.db.FindRecentlyResolvedIncidentByCheckUID(ctx, check.UID, since)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to find recently resolved incident: %w", err)
	}

	// Guard: don't reopen manually resolved incidents (has acknowledged_by)
	if incident.AcknowledgedBy != nil {
		return false, nil
	}

	// Guard: don't reopen if check was modified after incident was resolved
	if incident.ResolvedAt != nil && check.UpdatedAt.After(*incident.ResolvedAt) {
		return false, nil
	}

	return true, s.reopenIncident(ctx, check, result, incident)
}

// reopenIncident reopens a previously resolved incident.
func (s *Service) reopenIncident(
	ctx context.Context, check *models.Check, result *models.Result, incident *models.Incident,
) error {
	now := time.Now()
	activeState := models.IncidentStateActive
	newRelapseCount := incident.RelapseCount + 1
	newFailureCount := incident.FailureCount + 1

	update := models.IncidentUpdate{
		State:               &activeState,
		ClearResolvedAt:     true,
		RelapseCount:        &newRelapseCount,
		LastReopenedAt:      &now,
		FailureCount:        &newFailureCount,
		ClearAcknowledgedAt: true,
		ClearAcknowledgedBy: true,
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return fmt.Errorf("failed to reopen incident: %w", err)
	}

	// Emit reopened event
	effThreshold := effectiveRecoveryThreshold(check, &models.Incident{
		RelapseCount: newRelapseCount,
	})

	// Pass check fields to the incident for notification payload
	incident.RelapseCount = newRelapseCount

	if err := s.emitEvent(ctx, check.OrganizationUID, models.EventTypeIncidentReopened, incident, models.JSONMap{
		"check_uid":                    check.UID,
		"check_slug":                   check.Slug,
		"relapse_count":                newRelapseCount,
		"result_uid":                   result.UID,
		"effective_recovery_threshold": effThreshold,
	}); err != nil {
		return fmt.Errorf("failed to emit incident reopened event: %w", err)
	}

	return nil
}

// generateIncidentTitle generates a title for an incident.
func (s *Service) generateIncidentTitle(check *models.Check) string {
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug + " is down"
	}
	if check.Name != nil && *check.Name != "" {
		return *check.Name + " is down"
	}
	return "Check is down"
}

// emitEvent creates an event for the incident lifecycle.
func (s *Service) emitEvent(
	ctx context.Context, orgUID string, eventType models.EventType, incident *models.Incident, payload models.JSONMap,
) error {
	event := models.NewEvent(orgUID, eventType, models.ActorTypeSystem)
	event.IncidentUID = &incident.UID
	event.Payload = payload

	if err := s.db.CreateEvent(ctx, event); err != nil {
		return err
	}

	// Queue notifications for incident lifecycle events
	switch eventType {
	case models.EventTypeIncidentCreated, models.EventTypeIncidentResolved, models.EventTypeIncidentEscalated,
		models.EventTypeIncidentReopened:
		checkUID, _ := payload["check_uid"].(string)
		if checkUID == "" {
			slog.WarnContext(ctx, "Missing check_uid in event payload", "eventType", eventType)
			return nil
		}
		s.queueNotifications(ctx, orgUID, checkUID, incident.UID, eventType)
	case models.EventTypeCheckCreated, models.EventTypeCheckUpdated,
		models.EventTypeCheckDeleted, models.EventTypeIncidentAcknowledged:
		// No notifications for these event types
	}

	return nil
}

// queueNotifications queues notification jobs for an incident event.
func (s *Service) queueNotifications(
	ctx context.Context, orgUID, checkUID, incidentUID string, eventType models.EventType,
) {
	// Get connections for this check
	connections, err := s.db.ListConnectionsForCheck(ctx, checkUID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get connections for check", "checkUid", checkUID, "error", err)
		return // Don't fail incident processing for notification errors
	}

	for _, conn := range connections {
		if !conn.Enabled {
			continue
		}

		config, err := json.Marshal(jobtypes.NotificationJobConfig{
			ConnectionUID: conn.UID,
			IncidentUID:   incidentUID,
			EventType:     string(eventType),
		})
		if err != nil {
			slog.WarnContext(ctx, "Failed to marshal notification config",
				"connectionUid", conn.UID,
				"incidentUid", incidentUID,
				"error", err,
			)
			continue
		}

		_, err = s.jobsSvc.CreateJob(ctx, orgUID, string(jobdef.JobTypeNotification), config, nil)
		if err != nil {
			slog.WarnContext(ctx, "Failed to create notification job",
				"connectionUid", conn.UID,
				"incidentUid", incidentUID,
				"error", err,
			)
		}
	}
}

// ListIncidentsOptions contains options for listing incidents.
type ListIncidentsOptions struct {
	CheckUIDs []string
	States    []string // "active", "resolved"
	Since     *time.Time
	Until     *time.Time
	Cursor    string
	Size      int
	WithCheck bool // Include check details in response
}

// CheckResponse represents check details embedded in incident responses.
type CheckResponse struct {
	Slug   *string        `json:"slug,omitempty"`
	Type   string         `json:"type"`
	Config models.JSONMap `json:"config,omitempty"`
}

// IncidentResponse represents an incident in API responses.
type IncidentResponse struct {
	UID            string         `json:"uid"`
	CheckUID       string         `json:"checkUid"`
	CheckSlug      *string        `json:"checkSlug,omitempty"`
	CheckName      *string        `json:"checkName,omitempty"`
	State          string         `json:"state"`
	StartedAt      time.Time      `json:"startedAt"`
	ResolvedAt     *time.Time     `json:"resolvedAt,omitempty"`
	EscalatedAt    *time.Time     `json:"escalatedAt,omitempty"`
	AcknowledgedAt *time.Time     `json:"acknowledgedAt,omitempty"`
	FailureCount   int            `json:"failureCount"`
	RelapseCount   int            `json:"relapseCount"`
	LastReopenedAt *time.Time     `json:"lastReopenedAt,omitempty"`
	Title          *string        `json:"title,omitempty"`
	Description    *string        `json:"description,omitempty"`
	Check          *CheckResponse `json:"check,omitempty"`
}

// ListIncidentsResponse represents the response for listing incidents.
type ListIncidentsResponse struct {
	Data       []IncidentResponse `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// PaginationResponse represents pagination info.
type PaginationResponse struct {
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}

// stateToString converts an incident state to a string.
func stateToString(state models.IncidentState) string {
	switch state {
	case models.IncidentStateActive:
		return "active"
	case models.IncidentStateResolved:
		return "resolved"
	default:
		return "unknown"
	}
}

// incidentToResponse converts a model incident to an API response.
func incidentToResponse(inc *models.Incident) IncidentResponse {
	return IncidentResponse{
		UID:            inc.UID,
		CheckUID:       inc.CheckUID,
		State:          stateToString(inc.State),
		StartedAt:      inc.StartedAt,
		ResolvedAt:     inc.ResolvedAt,
		EscalatedAt:    inc.EscalatedAt,
		AcknowledgedAt: inc.AcknowledgedAt,
		FailureCount:   inc.FailureCount,
		RelapseCount:   inc.RelapseCount,
		LastReopenedAt: inc.LastReopenedAt,
		Title:          inc.Title,
		Description:    inc.Description,
	}
}

// checkToResponse converts a model check to an embedded check response.
func checkToResponse(check *models.Check) *CheckResponse {
	return &CheckResponse{
		Slug:   check.Slug,
		Type:   check.Type,
		Config: check.Config,
	}
}

// buildCheckMap builds a map of check UIDs to check models for embedding in responses.
func (s *Service) buildCheckMap(
	ctx context.Context, orgUID string, incidents []*models.Incident, withCheck bool,
) map[string]*models.Check {
	checkMap := make(map[string]*models.Check)
	if !withCheck || len(incidents) == 0 {
		return checkMap
	}

	for _, inc := range incidents {
		if _, exists := checkMap[inc.CheckUID]; exists {
			continue
		}
		check, err := s.db.GetCheck(ctx, orgUID, inc.CheckUID)
		if err == nil {
			checkMap[inc.CheckUID] = check
		}
	}

	return checkMap
}

// stringToState converts a string to an incident state.
func stringToState(s string) (models.IncidentState, bool) {
	switch s {
	case "active":
		return models.IncidentStateActive, true
	case "resolved":
		return models.IncidentStateResolved, true
	default:
		return 0, false
	}
}

// ListIncidents lists incidents for an organization.
func (s *Service) ListIncidents(
	ctx context.Context, orgSlug string, opts *ListIncidentsOptions,
) (*ListIncidentsResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Build filter
	filter := &models.ListIncidentsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       opts.CheckUIDs,
		Since:           opts.Since,
		Until:           opts.Until,
		Limit:           opts.Size + 1, // Fetch one extra to determine hasMore
	}

	// Convert state strings to state values
	for _, stateStr := range opts.States {
		if state, ok := stringToState(stateStr); ok {
			filter.States = append(filter.States, state)
		}
	}

	// TODO: Parse cursor

	incidents, err := s.db.ListIncidents(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list incidents: %w", err)
	}

	// Determine if there are more results
	hasMore := len(incidents) > opts.Size
	if hasMore {
		incidents = incidents[:opts.Size]
	}

	// Build check map if WithCheck is requested
	checkMap := s.buildCheckMap(ctx, org.UID, incidents, opts.WithCheck)

	// Build response
	response := &ListIncidentsResponse{
		Data: make([]IncidentResponse, 0, len(incidents)),
		Pagination: PaginationResponse{
			Size: opts.Size,
		},
	}

	for _, inc := range incidents {
		incResponse := incidentToResponse(inc)

		// Add check details if requested
		if check, exists := checkMap[inc.CheckUID]; exists {
			incResponse.Check = checkToResponse(check)
			incResponse.CheckSlug = check.Slug
			incResponse.CheckName = check.Name
		}

		response.Data = append(response.Data, incResponse)
	}

	// Set cursor if there are more results
	if hasMore && len(incidents) > 0 {
		lastIncident := incidents[len(incidents)-1]
		response.Pagination.Cursor = lastIncident.UID
	}

	return response, nil
}

// GetIncidentOptions contains options for getting a single incident.
type GetIncidentOptions struct {
	WithCheck bool // Include check details in response
}

// GetIncident gets a single incident by UID.
func (s *Service) GetIncident(
	ctx context.Context, orgSlug, incidentUID string, opts *GetIncidentOptions,
) (*IncidentResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	incident, err := s.db.GetIncident(ctx, org.UID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	response := incidentToResponse(incident)

	// Always fetch check to populate name and slug
	check, err := s.db.GetCheck(ctx, org.UID, incident.CheckUID)
	if err == nil {
		response.CheckSlug = check.Slug
		response.CheckName = check.Name
		if opts != nil && opts.WithCheck {
			response.Check = checkToResponse(check)
		}
	}

	return &response, nil
}

// GetIncidentByUID gets an incident by UID, requiring organization UID.
func (s *Service) GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}
	return incident, nil
}

// AcknowledgeIncidentRequest contains the data needed to acknowledge an incident.
type AcknowledgeIncidentRequest struct {
	IncidentUID    string
	AcknowledgedBy string // User UID or identifier
	SlackUserID    string // Slack user ID if acknowledged via Slack
	SlackUsername  string // Slack username for display
	Via            string // "slack", "web", etc.
}

// AcknowledgeIncident marks an incident as acknowledged.
func (s *Service) AcknowledgeIncident(
	ctx context.Context, orgUID string, req *AcknowledgeIncidentRequest,
) (*models.Incident, error) {
	// Get the incident
	incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("failed to get incident: %w", err)
	}

	// Check if already acknowledged
	if incident.AcknowledgedAt != nil {
		slog.InfoContext(ctx, "Incident already acknowledged",
			"incident_uid", incident.UID,
			"acknowledged_at", *incident.AcknowledgedAt,
		)
		return incident, nil // Return existing incident without error
	}

	// Update the incident
	now := time.Now()
	update := models.IncidentUpdate{
		AcknowledgedAt: &now,
	}
	if req.AcknowledgedBy != "" {
		update.AcknowledgedBy = &req.AcknowledgedBy
	}

	if err := s.db.UpdateIncident(ctx, incident.UID, &update); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	// Update local copy
	incident.AcknowledgedAt = &now
	incident.AcknowledgedBy = update.AcknowledgedBy

	// Create acknowledgment event
	event := models.NewEvent(orgUID, models.EventTypeIncidentAcknowledged, models.ActorTypeUser)
	event.IncidentUID = &incident.UID
	event.Payload = models.JSONMap{
		"via":            req.Via,
		"slack_user_id":  req.SlackUserID,
		"slack_username": req.SlackUsername,
	}
	if req.AcknowledgedBy != "" {
		event.ActorUID = &req.AcknowledgedBy
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to create acknowledgment event",
			"incident_uid", incident.UID,
			"error", err,
		)
		// Don't fail the acknowledgment for event creation errors
	}

	slog.InfoContext(ctx, "Incident acknowledged",
		"incident_uid", incident.UID,
		"via", req.Via,
		"slack_user_id", req.SlackUserID,
	)

	return incident, nil
}

// GetCheckByUID gets a check by UID within an organization.
func (s *Service) GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	return s.db.GetCheck(ctx, orgUID, checkUID)
}

// AcknowledgeIncidentFromSlack marks an incident as acknowledged via Slack.
// This method is used by the Slack integration to acknowledge incidents.
func (s *Service) AcknowledgeIncidentFromSlack(
	ctx context.Context, orgUID, incidentUID, slackUserID, slackUsername string,
) (*models.Incident, error) {
	return s.AcknowledgeIncident(ctx, orgUID, &AcknowledgeIncidentRequest{
		IncidentUID:   incidentUID,
		SlackUserID:   slackUserID,
		SlackUsername: slackUsername,
		Via:           "slack",
	})
}
