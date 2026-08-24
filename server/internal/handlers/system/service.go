// Package system provides handlers for system-wide configuration endpoints.
package system

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// Errors for system parameter operations.
var (
	ErrParameterNotFound       = errors.New("parameter not found")
	ErrInvalidParameter        = errors.New("invalid parameter value")
	ErrEmailInboxNotConfigured = errors.New("email inbox not configured")
	ErrEmailInboxDisabled      = errors.New("email inbox disabled")
	ErrEmailInboxNotAvailable  = errors.New("email inbox manager not initialized")
)

// JMAPInboxManager is the subset of *jmap.Manager that the system service
// depends on. Defined as an interface to keep the package decoupled from the
// jmap package and to make testing trivial.
type JMAPInboxManager interface {
	GetStatus() jmap.Status
	TriggerSync(ctx context.Context) error
	TestConnection(ctx context.Context, cfg *jmap.Config) (*jmap.Mailboxes, error)
}

// Service provides business logic for system parameter operations.
type Service struct {
	db        db.Service
	inbox     JMAPInboxManager
	formatter email.Formatter
}

// NewService creates a new system service.
func NewService(dbService db.Service) *Service {
	return &Service{
		db: dbService,
	}
}

// SetEmailInboxManager wires a JMAP inbox manager into the service. Called
// from app/server.go after the manager has been constructed.
func (s *Service) SetEmailInboxManager(m JMAPInboxManager) {
	s.inbox = m
}

// SetEmailFormatter wires the shared email.Formatter into the service, used
// to render the admin test email through test-email.html instead of a
// hand-rolled string. Called from app/server.go alongside the other service
// wiring.
func (s *Service) SetEmailFormatter(f email.Formatter) {
	s.formatter = f
}

// ParameterResponse represents a system parameter in API responses.
type ParameterResponse struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Secret    bool      `json:"secret"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListParametersResponse wraps the list of parameters.
type ListParametersResponse struct {
	Data []*ParameterResponse `json:"data"`
	// EnvOverrides lists the known parameter keys whose effective value is
	// currently forced by an SP_* environment variable. Env wins over the
	// database in systemconfig.Service.Initialize, so without this an operator
	// editing one of these keys in Server Settings would see their edit saved
	// and then apparently ignored. Only key NAMES appear here — never values —
	// so it is safe regardless of a key's secrecy.
	EnvOverrides []string `json:"envOverrides"`
}

// SetParameterRequest represents a request to set a parameter.
type SetParameterRequest struct {
	Value  any   `json:"value"`
	Secret *bool `json:"secret,omitempty"`
}

// ActivationFunnelRow lists per-org timestamps for each activation
// milestone. Missing milestones leave the corresponding field nil.
type ActivationFunnelRow struct {
	OrganizationUID string     `json:"organizationUid"`
	Slug            string     `json:"slug"`
	Name            string     `json:"name"`
	SignupAt        *time.Time `json:"signupAt,omitempty"`
	FirstCheckAt    *time.Time `json:"firstCheckAt,omitempty"`
	FirstResultAt   *time.Time `json:"firstResultAt,omitempty"`
	FirstNotifierAt *time.Time `json:"firstNotifierAt,omitempty"`
	FirstIncidentAt *time.Time `json:"firstIncidentAt,omitempty"`
	OrgCreatedAt    time.Time  `json:"orgCreatedAt"`
}

// ActivationFunnelResponse wraps the funnel rows.
type ActivationFunnelResponse struct {
	Data []*ActivationFunnelRow `json:"data"`
}

// ListActivationFunnel returns one row per organization with the timestamps
// for each activation milestone. Used by the super-admin /admin/activation
// view to spot stalled funnels at a glance.
func (s *Service) ListActivationFunnel(ctx context.Context) (*ActivationFunnelResponse, error) {
	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}

	rows := make([]*ActivationFunnelRow, 0, len(orgs))
	for _, org := range orgs {
		row := &ActivationFunnelRow{
			OrganizationUID: org.UID,
			Slug:            org.Slug,
			Name:            org.Name,
			OrgCreatedAt:    org.CreatedAt,
		}

		events, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: org.UID,
			EventTypes: []models.EventType{
				models.EventTypeOrgActivationSignupCompleted,
				models.EventTypeOrgActivationFirstCheckCreated,
				models.EventTypeOrgActivationFirstResultReceived,
				models.EventTypeOrgActivationFirstNotificationConfigured,
				models.EventTypeOrgActivationFirstIncidentPaged,
			},
		})
		if err != nil {
			slog.WarnContext(ctx, "activation funnel: list events failed",
				"orgUID", org.UID, "error", err)

			rows = append(rows, row)

			continue
		}

		for _, event := range events {
			applyActivationEvent(row, event)
		}

		rows = append(rows, row)
	}

	return &ActivationFunnelResponse{Data: rows}, nil
}

// applyActivationEvent stamps one activation milestone onto a funnel row.
// Split out of ListActivationFunnel because the switch must enumerate every
// event type in the product (exhaustive lint), so it grows with families that
// have nothing to do with activation.
func applyActivationEvent(row *ActivationFunnelRow, event *models.Event) {
	occurredAt := event.CreatedAt

	switch event.EventType {
	case models.EventTypeOrgActivationSignupCompleted:
		row.SignupAt = &occurredAt
	case models.EventTypeOrgActivationFirstCheckCreated:
		row.FirstCheckAt = &occurredAt
	case models.EventTypeOrgActivationFirstResultReceived:
		row.FirstResultAt = &occurredAt
	case models.EventTypeOrgActivationFirstNotificationConfigured:
		row.FirstNotifierAt = &occurredAt
	case models.EventTypeOrgActivationFirstIncidentPaged:
		row.FirstIncidentAt = &occurredAt
	case models.EventTypeCheckCreated, models.EventTypeCheckUpdated,
		models.EventTypeCheckDeleted,
		models.EventTypeIncidentCreated, models.EventTypeIncidentResolved,
		models.EventTypeIncidentEscalated, models.EventTypeIncidentReopened,
		models.EventTypeIncidentAcknowledged, models.EventTypeIncidentUnacknowledged,
		models.EventTypeIncidentSnoozed, models.EventTypeIncidentUnsnoozed,
		models.EventTypeIncidentEscalationFailed, models.EventTypeIncidentComment,
		models.EventTypeIncidentRolledUp,
		models.EventTypeStatusUpdateCreated, models.EventTypeStatusUpdateUpdated,
		models.EventTypeStatusUpdateDeleted,
		models.EventTypeStatusPageIncidentPublished,
		models.EventTypeStatusPageIncidentUpdated,
		models.EventTypeStatusPageIncidentResolved,
		models.EventTypeStatusSubscriberDisabled,
		models.EventTypeStatusPageCustomDomainDemoted,
		models.EventTypeAuthLoginSucceeded, models.EventTypeAuthLoginFailed,
		models.EventTypeAuthLogout,
		models.EventTypeAuthTokenCreated, models.EventTypeAuthTokenRevoked,
		models.EventTypeAuthTokenMisuse,
		models.EventTypeMemberInvited, models.EventTypeMemberJoined,
		models.EventTypeMemberRemoved, models.EventTypeMemberRoleChanged,
		models.EventTypeIntegrationCreated, models.EventTypeIntegrationUpdated,
		models.EventTypeIntegrationDeleted,
		models.EventTypeEscalationPolicyCreated, models.EventTypeEscalationPolicyUpdated,
		models.EventTypeEscalationPolicyDeleted,
		models.EventTypeOnCallScheduleCreated, models.EventTypeOnCallScheduleUpdated,
		models.EventTypeOnCallScheduleDeleted,
		models.EventTypeStatusPageCreated, models.EventTypeStatusPageUpdated,
		models.EventTypeStatusPageDeleted,
		models.EventTypeMaintenanceWindowCreated, models.EventTypeMaintenanceWindowUpdated,
		models.EventTypeMaintenanceWindowDeleted,
		models.EventTypeConfigApplied, models.EventTypeOrgSettingsUpdated:
		// Filter only requested activation events; these branches
		// are unreachable but exhaustive lint requires them.
	}
}

// ListParameters returns all system parameters with secrets masked.
func (s *Service) ListParameters(ctx context.Context) (*ListParametersResponse, error) {
	params, err := s.db.ListSystemParameters(ctx)
	if err != nil {
		return nil, err
	}

	responses := make([]*ParameterResponse, 0, len(params))
	for _, p := range params {
		responses = append(responses, s.toResponse(p))
	}

	return &ListParametersResponse{
		Data:         responses,
		EnvOverrides: systemconfig.EnvOverriddenKeys(),
	}, nil
}

// GetParameter returns a single system parameter with secret masked.
func (s *Service) GetParameter(ctx context.Context, key string) (*ParameterResponse, error) {
	param, err := s.db.GetSystemParameter(ctx, key)
	if err != nil {
		return nil, err
	}

	if param == nil {
		return nil, ErrParameterNotFound
	}

	return s.toResponse(param), nil
}

// SetParameter creates or updates a system parameter. The auth.password.* keys
// are validated against the exact bounds enforced at config load (see
// config.ValidatePasswordParameter), and the live aggregation-retention keys
// against the floor the aggregation job requires (integer >= 1), so a value
// that would abort the next startup or be silently ignored by the job is
// rejected here with a validation error instead of being persisted.
func (s *Service) SetParameter(ctx context.Context, key string, value any, secret bool) (*ParameterResponse, error) {
	if config.IsPasswordParameterKey(key) {
		if err := config.ValidatePasswordParameter(key, value); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidParameter, err)
		}
	}

	if systemconfig.IsAggregationRetentionKey(key) {
		if err := systemconfig.ValidateAggregationRetentionParameter(key, value); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidParameter, err)
		}
	}

	// The platform watchdog is an alerting path of last resort: a value it
	// cannot decode would only surface as a failing hourly job nobody reads.
	// Reject it here, while the operator is still looking at the request.
	if key == watchdog.ParamPlatformWatchdog {
		if err := watchdog.ValidateParameter(value); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidParameter, err)
		}
	}

	if err := s.db.SetSystemParameter(ctx, key, value, secret); err != nil {
		return nil, err
	}

	// Fetch the updated parameter to return
	param, err := s.db.GetSystemParameter(ctx, key)
	if err != nil {
		return nil, err
	}

	return s.toResponse(param), nil
}

// DeleteParameter soft-deletes a system parameter.
func (s *Service) DeleteParameter(ctx context.Context, key string) error {
	err := s.db.DeleteSystemParameter(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrParameterNotFound
		}

		return err
	}

	return nil
}

// toResponse converts a Parameter model to a response, masking secrets.
func (s *Service) toResponse(param *models.Parameter) *ParameterResponse {
	isSecret := param.Secret != nil && *param.Secret
	value := s.extractValue(param.Value)

	// Mask secret values
	if isSecret {
		value = "******"
	}

	return &ParameterResponse{
		Key:       param.Key,
		Value:     value,
		Secret:    isSecret,
		UpdatedAt: param.UpdatedAt,
	}
}

// extractValue extracts the value from the JSONMap.
func (s *Service) extractValue(value models.JSONMap) any {
	if val, ok := value["value"]; ok {
		return val
	}

	return value
}

// newEmailSender constructs the outbound SMTP sender for TestEmail from the
// just-loaded config — a test seam (mirrors patterns elsewhere in the
// codebase, e.g. jobtypes.newTwilioClient) so the rendering path can be
// exercised with a capturing fake instead of a live SMTP server.
//
//nolint:gochecknoglobals // test seam, not mutable application state.
var newEmailSender = func(cfg *config.EmailConfig, logger *slog.Logger) email.Sender {
	return email.NewSender(cfg, logger)
}

// TestEmailRequest represents a request to send a test email.
type TestEmailRequest struct {
	Recipient string `json:"recipient"`
}

// TestEmailResponse represents the result of sending a test email.
type TestEmailResponse struct {
	Sent    bool   `json:"sent"`
	Message string `json:"message"`
}

// TestEmail sends a test email using the currently saved SMTP parameters.
func (s *Service) TestEmail(ctx context.Context, recipient string) (*TestEmailResponse, error) {
	// Build email config from current DB parameters
	emailCfg, err := s.buildEmailConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load email config: %w", err)
	}

	if !emailCfg.Enabled {
		return &TestEmailResponse{Sent: false, Message: "Email sending is disabled. Enable it in the settings first."}, nil
	}

	if emailCfg.From == "" {
		return &TestEmailResponse{Sent: false, Message: "From address is not configured."}, nil
	}

	if s.formatter == nil {
		return &TestEmailResponse{Sent: false, Message: "Email formatter not configured."}, nil
	}

	// Create a temporary sender with current settings
	sender := newEmailSender(emailCfg, slog.Default())

	subject, htmlBody, textBody, err := s.formatter.Format("test-email.html", map[string]any{
		"Subject": "SolidPing Test Email",
		"Heading": "SolidPing Test Email",
		"Body": "This is a test email from SolidPing. " +
			"If you received this, your email configuration is working correctly.",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to format test email: %w", err)
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: []string{recipient}},
		Subject:          subject,
		Text:             textBody,
		HTML:             htmlBody,
		SupportReplyable: email.SupportReplyable("test-email.html"),
	}

	result, err := sender.Send(ctx, msg)
	if err != nil {
		//nolint:nilerr // Intentionally return nil error with failure details in response
		return &TestEmailResponse{
			Sent:    false,
			Message: "Failed to send: " + err.Error(),
		}, nil
	}

	return &TestEmailResponse{Sent: result.Sent, Message: result.Message}, nil
}

// buildEmailConfig reads email parameters from the database and builds an EmailConfig.
func (s *Service) buildEmailConfig(ctx context.Context) (*config.EmailConfig, error) {
	params, err := s.db.ListSystemParameters(ctx)
	if err != nil {
		return nil, err
	}

	// Build a map for quick lookup
	paramMap := make(map[string]any)
	for _, p := range params {
		if val, ok := p.Value["value"]; ok {
			paramMap[p.Key] = val
		}
	}

	cfg := &config.EmailConfig{
		Port:     587,
		AuthType: "login",
		Protocol: "starttls",
	}

	if v, ok := paramMap["email.enabled"].(bool); ok {
		cfg.Enabled = v
	}

	if v, ok := paramMap["email.host"].(string); ok {
		cfg.Host = v
	}

	if v, ok := paramMap["email.port"].(float64); ok {
		cfg.Port = int(v)
	}

	if v, ok := paramMap["email.username"].(string); ok {
		cfg.Username = v
	}

	if v, ok := paramMap["email.password"].(string); ok {
		cfg.Password = v
	}

	if v, ok := paramMap["email.from"].(string); ok {
		cfg.From = v
	}

	if v, ok := paramMap["email.from_name"].(string); ok {
		cfg.FromName = v
	}

	if v, ok := paramMap["email.auth_type"].(string); ok {
		cfg.AuthType = v
	}

	if v, ok := paramMap["email.protocol"].(string); ok {
		cfg.Protocol = v
	}

	// The support mailbox travels with the rest of the SMTP settings so the
	// "send a test email" button exercises the real Reply-To an operator has
	// configured, rather than a silently different one (spec 2026-08-22-02).
	if v, ok := paramMap["email.reply_to"].(string); ok {
		cfg.ReplyTo = v
	}

	if v, ok := paramMap["email.insecure_skip_verify"].(bool); ok {
		cfg.InsecureSkipVerify = v
	}

	return cfg, nil
}

// EmailInboxStatus returns the JMAP inbox manager's current status, or an
// error if the manager has not been wired into the service.
func (s *Service) EmailInboxStatus() (jmap.Status, error) {
	if s.inbox == nil {
		return jmap.Status{}, ErrEmailInboxNotAvailable
	}

	return s.inbox.GetStatus(), nil
}

// EmailInboxTest validates a JMAP configuration end-to-end (session
// discovery, mailbox resolution). When cfg is nil, the stored email_inbox
// system parameter is used. Returns the resolved mailboxes on success.
func (s *Service) EmailInboxTest(ctx context.Context, cfg *jmap.Config) (*jmap.Mailboxes, error) {
	if s.inbox == nil {
		return nil, ErrEmailInboxNotAvailable
	}

	if cfg == nil {
		stored, err := s.loadEmailInboxConfig(ctx)
		if err != nil {
			return nil, err
		}

		cfg = stored
	}

	return s.inbox.TestConnection(ctx, cfg)
}

// EmailInboxSync fires an immediate sync. Returns ErrEmailInboxNotConfigured
// if the system parameter is missing, or ErrEmailInboxDisabled if disabled.
func (s *Service) EmailInboxSync(ctx context.Context) error {
	if s.inbox == nil {
		return ErrEmailInboxNotAvailable
	}

	cfg, err := s.loadEmailInboxConfig(ctx)
	if err != nil {
		return err
	}

	if !cfg.Enabled {
		return ErrEmailInboxDisabled
	}

	return s.inbox.TriggerSync(ctx)
}

// EmailInboxConfig returns the stored email_inbox config with the password
// elided, suitable for prefilling the admin form. Returns a zero-valued
// response when the inbox has never been configured.
func (s *Service) EmailInboxConfig(ctx context.Context) (*EmailInboxConfigResponse, error) {
	cfg, err := s.loadEmailInboxConfig(ctx)
	if err != nil {
		if errors.Is(err, ErrEmailInboxNotConfigured) {
			return &EmailInboxConfigResponse{}, nil
		}

		return nil, err
	}

	return &EmailInboxConfigResponse{
		Enabled:                cfg.Enabled,
		SessionURL:             cfg.SessionURL,
		Username:               cfg.Username,
		AddressDomain:          cfg.AddressDomain,
		MailboxName:            cfg.MailboxName,
		ProcessedMailboxName:   cfg.ProcessedMailboxName,
		PollIntervalSeconds:    cfg.PollIntervalSeconds,
		ProcessedRetentionDays: cfg.ProcessedRetentionDays,
		FailedRetentionDays:    cfg.FailedRetentionDays,
		RewriteBaseURL:         cfg.RewriteBaseURL,
		PasswordSet:            cfg.Password != "",
	}, nil
}

// EmailInboxPublicAddressDomain returns the address domain configured on the
// shared inbox so authenticated users can render the per-check email
// address. Returns an empty string (not an error) when the inbox isn't
// configured — UIs use that to show the "configure first" placeholder.
func (s *Service) EmailInboxPublicAddressDomain(ctx context.Context) (string, error) {
	cfg, err := s.loadEmailInboxConfig(ctx)
	if err != nil {
		if errors.Is(err, ErrEmailInboxNotConfigured) {
			return "", nil
		}

		return "", err
	}

	return cfg.AddressDomain, nil
}

// loadEmailInboxConfig reads the stored configuration. Returns
// ErrEmailInboxNotConfigured if the parameter does not exist.
func (s *Service) loadEmailInboxConfig(ctx context.Context) (*jmap.Config, error) {
	param, err := s.db.GetSystemParameter(ctx, jmap.SystemParameterKey)
	if err != nil {
		return nil, fmt.Errorf("load email_inbox: %w", err)
	}

	if param == nil {
		return nil, ErrEmailInboxNotConfigured
	}

	cfg, err := jmap.JSONMapToConfig(param.Value)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// LaneLoadStats aggregates one lane's offered load for a worker.
type LaneLoadStats struct {
	// Jobs is the number of enabled check jobs in this lane the worker is
	// eligible to claim (region match).
	Jobs int `json:"jobs"`
	// CostEwmaSumMs is the sum of the jobs' cost EWMAs: the total wall-clock
	// milliseconds one full pass over the lane costs this worker.
	CostEwmaSumMs float64 `json:"costEwmaSumMs"`
	// DelayEwmaSumMs is the sum of the jobs' delay EWMAs — accumulated
	// start-lateness telemetry. A growing sum means the lane runs behind.
	DelayEwmaSumMs float64 `json:"delayEwmaSumMs"`
	// DutySumPct is the sum of per-job duty cycles (100 × cost EWMA / period):
	// how many runner slots this lane's steady-state demand occupies,
	// expressed in percent (100 ≈ one full-time slot). Comparing it against
	// the worker's pool capacity answers "is this worker overloaded?".
	DutySumPct float64 `json:"dutySumPct"`
}

// WorkerLaneLoad is the per-worker lane-load report.
type WorkerLaneLoad struct {
	WorkerUID    string        `json:"workerUid"`
	Name         string        `json:"name"`
	Region       *string       `json:"region,omitempty"`
	LastActiveAt *time.Time    `json:"lastActiveAt,omitempty"`
	Fast         LaneLoadStats `json:"fast"`
	Slow         LaneLoadStats `json:"slow"`
}

// LaneLoad computes, server-side, each worker's offered check load per lane:
// job counts, summed cost and delay EWMAs, and the summed duty cycle. "Offered"
// means every enabled job the worker is eligible to claim (job region unset, or
// a prefix of the worker's region) — workers sharing a region therefore see the
// same jobs, which is the honest capacity view for "is this worker overloaded".
func (s *Service) LaneLoad(ctx context.Context) ([]WorkerLaneLoad, error) {
	var workers []*models.Worker
	if err := s.db.DB().NewSelect().
		Model(&workers).
		Where("deleted_at IS NULL").
		Order("last_active_at DESC").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}

	// One bounded scan over the enabled jobs (one row per check × region);
	// aggregation happens in Go so the duty division and the region prefix
	// match stay dialect-neutral.
	var jobs []struct {
		Lane        uint8              `bun:"lane"`
		Region      *string            `bun:"region"`
		Period      timeutils.Duration `bun:"period"`
		CostEwmaMs  float64            `bun:"cost_ewma_ms"`
		DelayEwmaMs float64            `bun:"delay_ewma_ms"`
	}
	if err := s.db.DB().NewSelect().
		TableExpr("check_jobs AS cj").
		ColumnExpr("cj.lane, cj.region, cj.period, cj.cost_ewma_ms, cj.delay_ewma_ms").
		Join("JOIN checks AS c ON c.uid = cj.check_uid").
		Where("c.enabled = ?", true).
		Where("c.deleted_at IS NULL").
		Scan(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("list check jobs: %w", err)
	}

	report := make([]WorkerLaneLoad, 0, len(workers))

	for _, worker := range workers {
		row := WorkerLaneLoad{
			WorkerUID:    worker.UID,
			Name:         worker.Name,
			Region:       worker.Region,
			LastActiveAt: worker.LastActiveAt,
		}

		workerRegion := ""
		if worker.Region != nil {
			workerRegion = *worker.Region
		}

		for i := range jobs {
			job := &jobs[i]
			// Mirror the claim's region gate: NULL region, or the job region
			// is a prefix of the worker's ("eu" jobs match an "eu-fr" worker).
			if job.Region != nil && !strings.HasPrefix(workerRegion, *job.Region) {
				continue
			}

			stats := &row.Fast
			if job.Lane == scheduling.LaneSlow {
				stats = &row.Slow
			}

			stats.Jobs++
			stats.CostEwmaSumMs += job.CostEwmaMs
			stats.DelayEwmaSumMs += job.DelayEwmaMs

			if periodMs := float64(time.Duration(job.Period).Milliseconds()); periodMs > 0 {
				stats.DutySumPct += 100 * job.CostEwmaMs / periodMs
			}
		}

		report = append(report, row)
	}

	return report, nil
}
