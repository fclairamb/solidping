// Package reportschedules provides HTTP handlers for scheduled uptime-report
// digests, plus the "send one to me right now" test path.
//
// Recipients are PII and are handled to the same bar as status-page
// subscribers: never written into events, never logged, and only ever read
// back to the org's own authenticated admins.
package reportschedules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrReportScheduleNotFound is returned when a schedule is not found.
	ErrReportScheduleNotFound = errors.New("report schedule not found")
	// ErrNameRequired is returned when the name is missing.
	ErrNameRequired = errors.New("name is required")
	// ErrInvalidFrequency is returned when the frequency is not weekly/monthly.
	ErrInvalidFrequency = errors.New("frequency must be weekly or monthly")
	// ErrInvalidTimezone is returned when the timezone is not a known IANA zone.
	ErrInvalidTimezone = errors.New("timezone must be a valid IANA zone")
	// ErrInvalidRecipient is returned when a recipient is not a valid address.
	ErrInvalidRecipient = errors.New("recipients must be valid email addresses")
	// ErrTooManyRecipients is returned when the recipient list is oversized.
	ErrTooManyRecipients = errors.New("too many recipients")
	// ErrNoTestRecipient is returned when a test send has nowhere to go.
	ErrNoTestRecipient = errors.New("no recipient for the test send")
)

// maxRecipients bounds one schedule's fan-out. A digest is not a mailing list.
const maxRecipients = 50

// Mailer enqueues one rendered report email. It is an interface so the handler
// package does not depend on the jobs package (and so tests can assert the
// exact payload without a queue).
type Mailer interface {
	SendReport(ctx context.Context, orgUID, recipient string, data *uptimereport.Data) error
}

// Service provides business logic for report-schedule management.
type Service struct {
	db      db.Service
	cfg     *config.Config
	builder *uptimereport.Builder
	mailer  Mailer
}

// NewService creates a new report-schedules service. builder and mailer may be
// nil, in which case the test-send endpoint reports a 500 rather than pretending
// to have sent something.
func NewService(
	dbService db.Service, cfg *config.Config, builder *uptimereport.Builder, mailer Mailer,
) *Service {
	return &Service{db: dbService, cfg: cfg, builder: builder, mailer: mailer}
}

// Response is a report schedule as returned by the API.
type Response struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Frequency string `json:"frequency"`
	Timezone  string `json:"timezone"`
	// Recipients is PII. It is returned only to authenticated org admins, and
	// deliberately never echoed anywhere else (events, logs, public pages).
	Recipients      []string   `json:"recipients"`
	CheckUIDs       []string   `json:"checkUids"`
	CheckGroupUIDs  []string   `json:"checkGroupUids"`
	IncludeSLOs     bool       `json:"includeSlos"`
	Enabled         bool       `json:"enabled"`
	LastPeriodStart *time.Time `json:"lastPeriodStart,omitempty"`
	LastRunAt       *time.Time `json:"lastRunAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// CreateRequest is the POST body.
type CreateRequest struct {
	Name           string   `json:"name"`
	Frequency      string   `json:"frequency"`
	Timezone       string   `json:"timezone"`
	Recipients     []string `json:"recipients"`
	CheckUIDs      []string `json:"checkUids"`
	CheckGroupUIDs []string `json:"checkGroupUids"`
	IncludeSLOs    *bool    `json:"includeSlos"`
	Enabled        *bool    `json:"enabled"`
}

// UpdateRequest is the PATCH body. Nil means "leave alone".
type UpdateRequest struct {
	Name           *string   `json:"name,omitempty"`
	Frequency      *string   `json:"frequency,omitempty"`
	Timezone       *string   `json:"timezone,omitempty"`
	Recipients     *[]string `json:"recipients,omitempty"`
	CheckUIDs      *[]string `json:"checkUids,omitempty"`
	CheckGroupUIDs *[]string `json:"checkGroupUids,omitempty"`
	IncludeSLOs    *bool     `json:"includeSlos,omitempty"`
	Enabled        *bool     `json:"enabled,omitempty"`
}

// List returns the org's report schedules.
func (s *Service) List(ctx context.Context, orgSlug string) ([]Response, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	rows, err := s.db.ListReportSchedules(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	out := make([]Response, 0, len(rows))
	for _, row := range rows {
		out = append(out, toResponse(row))
	}

	return out, nil
}

// Get returns one schedule.
func (s *Service) Get(ctx context.Context, orgSlug, uid string) (Response, error) {
	_, row, err := s.resolve(ctx, orgSlug, uid)
	if err != nil {
		return Response{}, err
	}

	return toResponse(row), nil
}

// Create validates and stores a new schedule.
func (s *Service) Create(ctx context.Context, orgSlug string, req *CreateRequest) (Response, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return Response{}, ErrOrganizationNotFound
	}

	if req.Name == "" {
		return Response{}, ErrNameRequired
	}

	frequency := req.Frequency
	if frequency == "" {
		frequency = models.ReportFrequencyMonthly
	}

	if !models.IsValidReportFrequency(frequency) {
		return Response{}, ErrInvalidFrequency
	}

	timezone := req.Timezone
	if timezone == "" {
		timezone = models.DefaultReportTimezone
	}

	if _, tzErr := slo.LoadLocation(timezone); tzErr != nil {
		return Response{}, ErrInvalidTimezone
	}

	recipients, err := normalizeRecipients(req.Recipients)
	if err != nil {
		return Response{}, err
	}

	row := models.NewReportSchedule(org.UID, req.Name, frequency)
	row.Timezone = timezone
	row.Recipients = recipients
	row.CheckUIDs = normalizeUIDs(req.CheckUIDs)
	row.CheckGroupUIDs = normalizeUIDs(req.CheckGroupUIDs)

	if req.IncludeSLOs != nil {
		row.IncludeSLOs = *req.IncludeSLOs
	}

	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}

	if err := s.db.CreateReportSchedule(ctx, row); err != nil {
		return Response{}, err
	}

	return toResponse(row), nil
}

// Update applies a partial update.
func (s *Service) Update(ctx context.Context, orgSlug, uid string, req UpdateRequest) (Response, error) {
	org, row, err := s.resolve(ctx, orgSlug, uid)
	if err != nil {
		return Response{}, err
	}

	if req.Name != nil && *req.Name == "" {
		return Response{}, ErrNameRequired
	}

	if req.Frequency != nil && !models.IsValidReportFrequency(*req.Frequency) {
		return Response{}, ErrInvalidFrequency
	}

	if req.Timezone != nil {
		if _, tzErr := slo.LoadLocation(*req.Timezone); tzErr != nil {
			return Response{}, ErrInvalidTimezone
		}
	}

	update := models.ReportScheduleUpdate{
		Name:           req.Name,
		Frequency:      req.Frequency,
		Timezone:       req.Timezone,
		CheckUIDs:      req.CheckUIDs,
		CheckGroupUIDs: req.CheckGroupUIDs,
		IncludeSLOs:    req.IncludeSLOs,
		Enabled:        req.Enabled,
	}

	if req.Recipients != nil {
		recipients, recErr := normalizeRecipients(*req.Recipients)
		if recErr != nil {
			return Response{}, recErr
		}

		update.Recipients = &recipients
	}

	if updateErr := s.db.UpdateReportSchedule(ctx, row.UID, update); updateErr != nil {
		return Response{}, updateErr
	}

	updated, err := s.db.GetReportSchedule(ctx, org.UID, row.UID)
	if err != nil {
		return Response{}, err
	}

	return toResponse(updated), nil
}

// Delete soft-deletes a schedule.
func (s *Service) Delete(ctx context.Context, orgSlug, uid string) error {
	org, row, err := s.resolve(ctx, orgSlug, uid)
	if err != nil {
		return err
	}

	return s.db.DeleteReportSchedule(ctx, org.UID, row.UID)
}

// TestSend renders the schedule's report for the period that most recently
// closed and mails it to a single caller-supplied address.
//
// It deliberately does NOT fan out to the schedule's recipients: "let me see
// what this looks like" must never be a button that mails forty people.
func (s *Service) TestSend(ctx context.Context, orgSlug, uid, recipient string, now time.Time) error {
	org, row, err := s.resolve(ctx, orgSlug, uid)
	if err != nil {
		return err
	}

	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return ErrNoTestRecipient
	}

	if _, parseErr := mail.ParseAddress(recipient); parseErr != nil {
		return ErrInvalidRecipient
	}

	if s.builder == nil || s.mailer == nil {
		return fmt.Errorf("report delivery is not configured: %w", ErrNoTestRecipient)
	}

	// The test send goes to the authenticated caller, who by definition did not
	// unsubscribe from a digest they are configuring — but honoring the
	// suppression list here too means there is exactly one code path that can
	// mail a suppressed address, and it is closed.
	suppressed, err := s.db.IsEmailSuppressed(ctx, org.UID, recipient, "")
	if err != nil {
		return fmt.Errorf("check suppression: %w", err)
	}

	if suppressed {
		return nil
	}

	window, _ := uptimereport.Window(row, now)

	data, err := s.builder.Build(ctx, org, row, window, now)
	if err != nil {
		return err
	}

	return s.mailer.SendReport(ctx, org.UID, recipient, data)
}

func (s *Service) resolve(
	ctx context.Context, orgSlug, uid string,
) (*models.Organization, *models.ReportSchedule, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, ErrOrganizationNotFound
	}

	row, err := s.db.GetReportSchedule(ctx, org.UID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrReportScheduleNotFound
		}

		return nil, nil, err
	}

	return org, row, nil
}

func toResponse(row *models.ReportSchedule) Response {
	return Response{
		UID:             row.UID,
		Name:            row.Name,
		Frequency:       row.Frequency,
		Timezone:        row.Timezone,
		Recipients:      nonNil(row.Recipients),
		CheckUIDs:       nonNil(row.CheckUIDs),
		CheckGroupUIDs:  nonNil(row.CheckGroupUIDs),
		IncludeSLOs:     row.IncludeSLOs,
		Enabled:         row.Enabled,
		LastPeriodStart: row.LastPeriodStart,
		LastRunAt:       row.LastRunAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}

	return values
}

// normalizeRecipients trims, de-duplicates (case-insensitively) and validates
// the address list.
func normalizeRecipients(values []string) ([]string, error) {
	if len(values) > maxRecipients {
		return nil, ErrTooManyRecipients
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))

	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		if _, err := mail.ParseAddress(trimmed); err != nil {
			return nil, ErrInvalidRecipient
		}

		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			continue
		}

		seen[key] = struct{}{}

		out = append(out, trimmed)
	}

	return out, nil
}

func normalizeUIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		if _, dup := seen[trimmed]; dup {
			continue
		}

		seen[trimmed] = struct{}{}

		out = append(out, trimmed)
	}

	return out
}
