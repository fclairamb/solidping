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
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
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

// buildHeartbeatOutput assembles the per-ping Output map: the resolved
// message plus best-effort caller metadata. Keys the caller didn't send are
// omitted entirely rather than persisted as empty strings (e.g. no
// User-Agent header on the ping).
//
// callerData — the caller-supplied JSON body keys other than "message" — is
// nested under a "data" key rather than flattened into the top level. This is
// deliberate: flattening would let a caller overwrite the server-observed
// remoteAddr/userAgent/httpMethod simply by including those keys in its own
// JSON body. Nesting removes that forgery vector entirely. The "data" key is
// omitted altogether (not stored as an empty object) when callerData is
// empty.
func buildHeartbeatOutput(message, userAgent, remoteAddr, httpMethod string, callerData map[string]any) models.JSONMap {
	output := models.JSONMap{"message": message}

	if userAgent != "" {
		output["userAgent"] = userAgent
	}

	if remoteAddr != "" {
		output["remoteAddr"] = remoteAddr
	}

	if httpMethod != "" {
		output["httpMethod"] = httpMethod
	}

	if len(callerData) > 0 {
		output["data"] = models.JSONMap(callerData)
	}

	return output
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
	rt          *realtime.Publisher
}

// NewService creates a new heartbeat service. rt may be nil (realtime
// disabled) — hint publishing is a nil-safe no-op then.
func NewService(dbService db.Service, jobSvc jobsvc.Service, rt *realtime.Publisher) *Service {
	return &Service{
		db:          dbService,
		incidentSvc: incidents.NewService(dbService, jobSvc, clock.Real{}, rt),
		rt:          rt,
	}
}

// ReceiveHeartbeat processes an incoming heartbeat ping. durationMs is the
// caller-reported run duration (0 when absent or invalid), persisted as the
// result's Duration. userAgent, remoteAddr, and httpMethod are caller
// metadata (display-only forensics, not used for any security decision)
// persisted alongside the ping's message. callerData is the caller-supplied
// JSON body minus "message" and a valid "durationMs", persisted nested under
// Output["data"] — see buildHeartbeatOutput for why it is never flattened.
func (s *Service) ReceiveHeartbeat(
	ctx context.Context, orgSlug, identifier, token, statusStr, message string, durationMs float32,
	userAgent, remoteAddr, httpMethod string, callerData map[string]any,
) error {
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

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		PeriodType:      "raw",
		PeriodStart:     time.Now(),
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         make(models.JSONMap),
		Output:          buildHeartbeatOutput(outputMessage, userAgent, remoteAddr, httpMethod, callerData),
		CreatedAt:       time.Now(),
	}

	if err := s.db.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	// Skip incident processing for non-terminal statuses
	if checkerStatus == checkerdef.StatusRunning {
		// Live hint: the result is persisted even though incident processing
		// is skipped — ProcessCheckResult publishes for every other status.
		s.rt.Publish(ctx, org.UID, check.UID, realtime.KindResults)

		return nil
	}

	// Process incidents (may trigger recovery or creation)
	if err := s.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		// Log but don't fail the heartbeat
		_ = err
	}

	return nil
}
