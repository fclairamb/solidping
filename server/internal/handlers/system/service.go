// Package system provides handlers for system-wide configuration endpoints.
package system

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// Errors for system parameter operations.
var (
	ErrParameterNotFound       = errors.New("parameter not found")
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
	db    db.Service
	inbox JMAPInboxManager
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
}

// SetParameterRequest represents a request to set a parameter.
type SetParameterRequest struct {
	Value  any   `json:"value"`
	Secret *bool `json:"secret,omitempty"`
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

	return &ListParametersResponse{Data: responses}, nil
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

// SetParameter creates or updates a system parameter.
func (s *Service) SetParameter(ctx context.Context, key string, value any, secret bool) (*ParameterResponse, error) {
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

	// Create a temporary sender with current settings
	sender := email.NewSender(emailCfg, slog.Default())

	testBody := "This is a test email from SolidPing. " +
		"If you received this, your email configuration is working correctly."
	msg := &email.Message{
		Recipients: email.Recipients{To: []string{recipient}},
		Subject:    "SolidPing Test Email",
		Text:       testBody,
		HTML:       "<h2>SolidPing Test Email</h2><p>" + testBody + "</p>",
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
