package heartbeat

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
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

// tagMaintenance sets results.maintenance before the insert (spec
// 2026-08-20-01): rollup buckets cannot be sliced after the fact, so the tag
// has to exist before aggregation runs.
//
// Best-effort — a failed maintenance lookup records the beat as production
// traffic rather than losing it.
func (s *Service) tagMaintenance(ctx context.Context, checkUID string, result *models.Result) {
	inMaintenance, err := s.incidentSvc.IsCheckInActiveMaintenance(ctx, checkUID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to resolve maintenance window for result tagging",
			"error", err, "check_uid", checkUID)

		return
	}

	result.Maintenance = inMaintenance
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
	// pushTimestampWindow bounds how far an SP2 beat's `ts` may sit from
	// server time. Zero means "use the documented default", so a call site
	// that forgets SetPushTimestampWindow gets the safe value rather than a
	// zero-width window that would reject every clock-carrying device.
	pushTimestampWindow time.Duration
}

// SetPushTimestampWindow configures the SP2 freshness window used by the
// embedded TCP/UDP transports. Non-positive values keep the documented
// default (config.DefaultHeartbeatTimestampWindow).
func (s *Service) SetPushTimestampWindow(window time.Duration) {
	if window > 0 {
		s.pushTimestampWindow = window
	}
}

// timestampWindow resolves the configured SP2 freshness window.
func (s *Service) timestampWindow() time.Duration {
	if s.pushTimestampWindow <= 0 {
		return config.DefaultHeartbeatTimestampWindow
	}

	return s.pushTimestampWindow
}

// NewService creates a new heartbeat service. rt may be nil (realtime
// disabled) — hint publishing is a nil-safe no-op then.
//
// publicationHook is the status-page publication side (spec 2026-08-19-08) and
// may be nil. It is a REQUIRED PARAMETER rather than a setter on purpose: a
// heartbeat ping opens and resolves incidents exactly like a probe result
// does, so an instance without the hook silently never auto-publishes anything
// — a gap that is invisible until someone notices their heartbeat outage never
// reached the status page. Making it part of the signature means a new call
// site has to make a deliberate choice instead of inheriting the bug.
//
// defaultCheckTimeout is the operator-configured `scheduling.check_timeout_ms`
// (config.SchedulingConfig.CheckTimeout), a required parameter for the same
// reason: a heartbeat ping runs the very same rollup gate as a probe result,
// so an unplumbed timeout would give this ingest path a different
// confirmation-hold cap than the worker for the identical check. Non-positive
// values keep incidents.DefaultCheckTimeoutFallback.
func NewService(
	dbService db.Service, jobSvc jobsvc.Service, realtimePublisher *realtime.Publisher,
	publicationHook incidents.PublicationHook, defaultCheckTimeout time.Duration,
) *Service {
	incidentSvc := incidents.NewService(dbService, jobSvc, clock.Real{}, realtimePublisher)
	incidentSvc.SetDefaultCheckTimeout(defaultCheckTimeout)

	if publicationHook != nil {
		incidentSvc.SetPublicationHook(publicationHook)
	}

	return &Service{
		db:          dbService,
		incidentSvc: incidentSvc,
		rt:          realtimePublisher,
	}
}

// Request is one inbound heartbeat, whatever transport carried it.
//
// It exists so the HTTP ingest and the embedded TCP/UDP push transports reach
// the SAME recording code by construction rather than by copy: maintenance
// tagging, the result insert, the realtime hint and incident/recovery
// processing (with the operator-configured check timeout, spec 2026-09-01-02)
// happen in exactly one place, recordBeat.
type Request struct {
	OrgSlug    string
	Identifier string
	// Token is the plaintext token to compare. Only the HTTP ingest supplies
	// it; the push transports verify their own credential (SP1 token or SP2
	// MAC) before calling recordBeat.
	Token string
	// Status is the caller-reported status word ("", "running", "up", "down",
	// "error"). Empty means "up", for backward compatibility.
	Status string
	// Message overrides the default output message.
	Message string
	// DurationMs is the caller-reported run duration (0 when absent).
	DurationMs float32
	// UserAgent, RemoteAddr, HTTPMethod and Transport are caller metadata:
	// display-only forensics, never used for any security decision. In
	// particular the source address of a UDP datagram is spoofable and is
	// NEVER identity.
	UserAgent  string
	RemoteAddr string
	HTTPMethod string
	Transport  string
	// CallerData is caller-supplied data persisted nested under
	// Output["data"] — see buildHeartbeatOutput for why it is never flattened.
	CallerData map[string]any
	// Annotation is the raw beat annotation ("" when absent). Parsing is
	// best-effort and can never fail the beat.
	Annotation string
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
	return s.Receive(ctx, &Request{
		OrgSlug: orgSlug, Identifier: identifier, Token: token,
		Status: statusStr, Message: message, DurationMs: durationMs,
		UserAgent: userAgent, RemoteAddr: remoteAddr, HTTPMethod: httpMethod,
		CallerData: callerData,
	})
}

// Receive is the token-authenticated ingest path: resolve the check, compare
// the token, record the beat.
func (s *Service) Receive(ctx context.Context, req *Request) error {
	org, check, err := s.resolveCheck(ctx, req.OrgSlug, req.Identifier)
	if err != nil {
		return err
	}

	if req.Token == "" {
		return ErrMissingToken
	}

	expectedToken, _ := check.Config["token"].(string)
	if expectedToken == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(expectedToken)) != 1 {
		return ErrInvalidToken
	}

	return s.recordBeat(ctx, org, check, req)
}

// resolveCheck maps <org>/<identifier> to a live heartbeat check.
func (s *Service) resolveCheck(
	ctx context.Context, orgSlug, identifier string,
) (*models.Organization, *models.Check, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return nil, nil, ErrCheckNotFound
	}

	if checkerdef.CheckType(check.Type) != checkerdef.CheckTypeHeartbeat {
		return nil, nil, ErrNotHeartbeatCheck
	}

	return org, check, nil
}

// recordBeat is the ONE place a heartbeat becomes a result row. Every ingest
// transport converges here after it has authenticated the beat its own way, so
// maintenance tagging, the status-tracking insert, the realtime hint and
// incident/recovery processing cannot drift between transports.
func (s *Service) recordBeat(
	ctx context.Context, org *models.Organization, check *models.Check, req *Request,
) error {
	statusStr := req.Status
	if statusStr == "" {
		statusStr = "up"
	}

	statusStr = strings.ToLower(statusStr)

	checkerStatus, ok := parseHeartbeatStatus(statusStr)
	if !ok {
		return ErrInvalidStatus
	}

	outputMessage := req.Message
	if outputMessage == "" {
		outputMessage = defaultOutputMessage(statusStr)
	}

	// Annotation parsing is total and runs last, so a malformed annotation can
	// only ever add less information — never reject the beat.
	callerData, metrics := applyAnnotation(req.CallerData, req.Annotation)

	if req.Transport != "" {
		if callerData == nil {
			callerData = make(map[string]any, 1)
		}

		callerData["transport"] = req.Transport
	}

	resultUID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	status := int(checkerStatus)
	durationMs := req.DurationMs

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		PeriodType:      "raw",
		PeriodStart:     time.Now(),
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         metrics,
		Output: buildHeartbeatOutput(
			outputMessage, req.UserAgent, req.RemoteAddr, req.HTTPMethod, callerData),
		CreatedAt: time.Now(),
	}

	s.tagMaintenance(ctx, check.UID, result)

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

// applyAnnotation folds a beat annotation into the two places it belongs:
// numeric fields become first-class time series in the result's `metrics`
// jsonb (rolled up by the suffix conventions in jobtypes.job_aggregation), and
// the status word, the text fields and the raw fallback go under the result's
// `output` jsonb.
//
// It never returns an error. A firmware typo in a key name must not make a
// healthy device look dead.
func applyAnnotation(callerData map[string]any, annotation string) (map[string]any, models.JSONMap) {
	metrics := make(models.JSONMap)

	parsed := heartbeatpush.ParseAnnotation(annotation)
	if parsed.IsEmpty() {
		return callerData, metrics
	}

	for key, value := range parsed.Numeric {
		metrics[key] = value
	}

	stored := make(map[string]any, 3)
	if parsed.Status != "" {
		stored["status"] = parsed.Status
	}

	if len(parsed.Text) > 0 {
		fields := make(map[string]any, len(parsed.Text))
		for key, value := range parsed.Text {
			fields[key] = value
		}

		stored["fields"] = fields
	}

	if parsed.Raw != "" {
		stored["raw"] = parsed.Raw
	}

	if len(stored) == 0 {
		return callerData, metrics
	}

	if callerData == nil {
		callerData = make(map[string]any, 1)
	}

	callerData["annotation"] = stored

	return callerData, metrics
}
