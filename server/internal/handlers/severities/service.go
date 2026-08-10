// Package severities provides HTTP handlers for the severity primitive
// — per-org channel-set bundles consumed by escalation step fan-out.
// Spec 2026-05-08-03-severity-primitive-for-escalation-steps.md.
package severities

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service-level error sentinels translated to HTTP codes by the handler.
var (
	// ErrOrganizationNotFound is returned when the org slug doesn't resolve.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrSeverityNotFound is returned when the slug/uid lookup misses.
	ErrSeverityNotFound = errors.New("severity not found")
	// ErrSlugConflict is returned when a create or rename collides with
	// an existing live row in the org.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrInvalidSlug is returned when a provided slug fails formatting.
	ErrInvalidSlug = errors.New("invalid slug format")
	// ErrInvalidChannel is returned when channels[] contains an unknown
	// channel-type. Validation is open-ended: the union of ConnectionType
	// values plus the synthetic direct-channel types.
	ErrInvalidChannel = errors.New("invalid channel type")
	// ErrCannotDeleteDefault is returned when an operator tries to delete
	// the row marked is_default. They have to promote another severity
	// first.
	ErrCannotDeleteDefault = errors.New("cannot delete the default severity; mark another severity as default first")
)

// slugRegex mirrors the check-group / channel slug format: lowercase
// kebab-case, 3–100 characters, must start with a letter.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,99}$`)

// allowedChannels returns the validation enum: ConnectionType values plus
// the synthetic direct-channel types. Function rather than var to satisfy
// gochecknoglobals; the map is rebuilt per validation call (negligible cost).
func allowedChannels() map[string]bool {
	return map[string]bool{
		// Existing connection types (mirror models.ConnectionType*).
		"slack": true, "discord": true, "webhook": true, "email": true,
		"googlechat": true, "mattermost": true, "msteams": true, "ntfy": true,
		"opsgenie": true, "pushover": true, "msteams-bot": true,
		// Direct-channel types. These are synthetic tokens, not connection
		// types: "whatsapp" pages a user's verified WhatsApp contact through
		// the instance-level WABA, exactly as "sms" pages their phone contact,
		// and "telegram" pages their connected chat through the instance bot.
		"sms": true, "voice": true, "push": true, "critical_push": true,
		"whatsapp": true, "telegram": true,
	}
}

// SeverityResponse is the JSON shape returned by the API. Channels is
// decoded back into a slice so clients don't have to parse the
// JSON-encoded column.
type SeverityResponse struct {
	UID         string    `json:"uid"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	Channels    []string  `json:"channels"`
	IsDefault   bool      `json:"isDefault"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateSeverityRequest captures the new-row inputs.
type CreateSeverityRequest struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Channels    []string `json:"channels"`
	IsDefault   bool     `json:"isDefault,omitempty"`
}

// UpdateSeverityRequest captures the patch inputs. nil pointers leave
// the column alone; explicit false/empty are honored.
type UpdateSeverityRequest struct {
	Slug        *string   `json:"slug,omitempty"`
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Channels    *[]string `json:"channels,omitempty"`
	IsDefault   *bool     `json:"isDefault,omitempty"`
}

// Service implements the severity domain.
type Service struct {
	db db.Service
}

// NewService creates the severities service bound to the given DB.
func NewService(database db.Service) *Service {
	return &Service{db: database}
}

// ListSeverities returns every live severity in the org, default-first
// then by slug.
func (s *Service) ListSeverities(ctx context.Context, orgSlug string) ([]SeverityResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	rows, err := s.db.ListSeverities(ctx, &models.ListSeveritiesFilter{
		OrganizationUID: org.UID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list severities: %w", err)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsDefault != rows[j].IsDefault {
			return rows[i].IsDefault
		}

		return rows[i].Slug < rows[j].Slug
	})

	out := make([]SeverityResponse, 0, len(rows))
	for _, sev := range rows {
		out = append(out, toResponse(sev))
	}

	return out, nil
}

// GetSeverity loads one severity by uid or slug.
func (s *Service) GetSeverity(
	ctx context.Context, orgSlug, identifier string,
) (*SeverityResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	sev, err := s.db.GetSeverity(ctx, org.UID, identifier)
	if err != nil {
		return nil, ErrSeverityNotFound
	}

	resp := toResponse(sev)

	return &resp, nil
}

// CreateSeverity inserts a new severity row after validating slug,
// channel-types, and the unique-default invariant.
func (s *Service) CreateSeverity(
	ctx context.Context, orgSlug string, req CreateSeverityRequest,
) (*SeverityResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if !slugRegex.MatchString(req.Slug) {
		return nil, ErrInvalidSlug
	}
	if vErr := validateChannels(req.Channels); vErr != nil {
		return nil, vErr
	}
	if existing, getErr := s.db.GetSeverity(ctx, org.UID, req.Slug); getErr == nil && existing != nil {
		return nil, ErrSlugConflict
	}

	sev := models.NewSeverity(org.UID, req.Slug, req.Name, req.Channels, req.IsDefault)
	sev.Description = req.Description

	if req.IsDefault {
		// Demote the existing default first so the partial unique index
		// holds during the insert.
		if demoteErr := s.db.ClearOrgDefaultSeverity(ctx, org.UID); demoteErr != nil {
			return nil, fmt.Errorf("failed to demote previous default: %w", demoteErr)
		}
	}

	if cErr := s.db.CreateSeverity(ctx, sev); cErr != nil {
		return nil, fmt.Errorf("failed to create severity: %w", cErr)
	}

	resp := toResponse(sev)

	return &resp, nil
}

// UpdateSeverity patches an existing row.
func (s *Service) UpdateSeverity(
	ctx context.Context, orgSlug, identifier string, req UpdateSeverityRequest,
) (*SeverityResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	sev, err := s.db.GetSeverity(ctx, org.UID, identifier)
	if err != nil {
		return nil, ErrSeverityNotFound
	}

	update, err := s.buildSeverityUpdate(ctx, org.UID, sev, req)
	if err != nil {
		return nil, err
	}

	if updErr := s.db.UpdateSeverity(ctx, sev.UID, update); updErr != nil {
		return nil, fmt.Errorf("failed to update severity: %w", updErr)
	}

	updated, err := s.db.GetSeverity(ctx, org.UID, sev.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload severity: %w", err)
	}
	resp := toResponse(updated)

	return &resp, nil
}

// buildSeverityUpdate validates the patch fields and returns a SeverityUpdate
// ready for the DB. Side effect: demotes the previous default when the
// caller is promoting a new one (the partial unique index requires it).
func (s *Service) buildSeverityUpdate(
	ctx context.Context, orgUID string, sev *models.Severity, req UpdateSeverityRequest,
) (*models.SeverityUpdate, error) {
	update := &models.SeverityUpdate{}

	if req.Slug != nil {
		if !slugRegex.MatchString(*req.Slug) {
			return nil, ErrInvalidSlug
		}
		if *req.Slug != sev.Slug {
			if existing, getErr := s.db.GetSeverity(ctx, orgUID, *req.Slug); getErr == nil && existing != nil {
				return nil, ErrSlugConflict
			}
		}
		update.Slug = req.Slug
	}
	if req.Name != nil {
		update.Name = req.Name
	}
	if req.Description != nil {
		update.Description = req.Description
	}
	if req.Channels != nil {
		if vErr := validateChannels(*req.Channels); vErr != nil {
			return nil, vErr
		}
		update.Channels = req.Channels
	}
	if req.IsDefault != nil {
		if *req.IsDefault && !sev.IsDefault {
			if dErr := s.db.ClearOrgDefaultSeverity(ctx, orgUID); dErr != nil {
				return nil, fmt.Errorf("failed to demote previous default: %w", dErr)
			}
		}
		update.IsDefault = req.IsDefault
	}

	return update, nil
}

// DeleteSeverity soft-deletes a severity. Refuses to delete the org's
// current default — operators must promote another row first.
func (s *Service) DeleteSeverity(ctx context.Context, orgSlug, identifier string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	sev, err := s.db.GetSeverity(ctx, org.UID, identifier)
	if err != nil {
		return ErrSeverityNotFound
	}

	if sev.IsDefault {
		return ErrCannotDeleteDefault
	}

	if err := s.db.DeleteSeverity(ctx, sev.UID); err != nil {
		return fmt.Errorf("failed to delete severity: %w", err)
	}

	return nil
}

// SeedDefaults inserts the three baseline severities for a new org.
// Idempotent: skips slugs that already have a live row.
func (s *Service) SeedDefaults(ctx context.Context, orgUID string) error {
	seeds := models.DefaultSeveritySeeds()
	for i := range seeds {
		seed := &seeds[i]
		if existing, err := s.db.GetSeverity(ctx, orgUID, seed.Slug); err == nil && existing != nil {
			continue
		}
		row := models.NewSeverity(orgUID, seed.Slug, seed.Name, seed.Channels, seed.IsDefault)
		if err := s.db.CreateSeverity(ctx, row); err != nil {
			return fmt.Errorf("failed to seed severity %q: %w", seed.Slug, err)
		}
	}

	return nil
}

func toResponse(sev *models.Severity) SeverityResponse {
	return SeverityResponse{
		UID:         sev.UID,
		Slug:        sev.Slug,
		Name:        sev.Name,
		Description: sev.Description,
		Channels:    sev.ChannelList(),
		IsDefault:   sev.IsDefault,
		CreatedAt:   sev.CreatedAt,
		UpdatedAt:   sev.UpdatedAt,
	}
}

func validateChannels(channels []string) error {
	allowed := allowedChannels()
	for _, c := range channels {
		if !allowed[c] {
			return fmt.Errorf("%w: %q", ErrInvalidChannel, c)
		}
	}

	return nil
}
