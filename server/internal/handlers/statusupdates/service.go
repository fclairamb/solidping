// Package statusupdates provides HTTP handlers for status update management endpoints.
package statusupdates

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the organization does not exist.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusUpdateNotFound is returned when the status update does not exist.
	ErrStatusUpdateNotFound = errors.New("status update not found")
	// ErrStatusPageNotFound is returned when the referenced status page does not exist.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrStatusPageForbidden is returned when the status page belongs to a different org.
	ErrStatusPageForbidden = errors.New("status page belongs to a different org")
	// ErrSectionNotFound is returned when the section does not exist on the page.
	ErrSectionNotFound = errors.New("section not found on status page")
	// ErrCheckNotFound is returned when the check resource is not on the page.
	ErrCheckNotFound = errors.New("check not found on status page")
	// ErrIncidentNotFound is returned when the incident does not exist.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrInvalidKind is returned when the kind is not one of the valid values.
	ErrInvalidKind = errors.New("invalid status update kind")
	// ErrTitleRequired is returned when the title is empty.
	ErrTitleRequired = errors.New("title is required")
	// ErrTitleTooLong is returned when the title exceeds 200 characters.
	ErrTitleTooLong = errors.New("title must be at most 200 characters")
	// ErrBodyRequired is returned when bodyMarkdown is empty.
	ErrBodyRequired = errors.New("body is required")
	// ErrBodyTooLong is returned when bodyMarkdown exceeds 16384 characters.
	ErrBodyTooLong = errors.New("body must be at most 16384 characters")
	// ErrInvalidLinkURL is returned when linkUrl is not a valid http/https URL.
	ErrInvalidLinkURL = errors.New("link URL must be a valid http or https URL")
	// ErrCheckUIDMismatch is returned when checkUID does not match the incident's check.
	ErrCheckUIDMismatch = errors.New("check does not match the incident's check")
)

// Service provides business logic for status update management.
type Service struct {
	db db.Service
}

// NewService creates a new status updates service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// StatusUpdateResponse represents a status update in API responses.
type StatusUpdateResponse struct {
	UID           string    `json:"uid"`
	StatusPageUID string    `json:"statusPageUid"`
	SectionUID    *string   `json:"sectionUid,omitempty"`
	CheckUID      *string   `json:"checkUid,omitempty"`
	IncidentUID   *string   `json:"incidentUid,omitempty"`
	Title         string    `json:"title"`
	BodyMarkdown  string    `json:"bodyMarkdown"`
	LinkURL       *string   `json:"linkUrl,omitempty"`
	Kind          string    `json:"kind"`
	PublishedAt   time.Time `json:"publishedAt"`
	AuthorUID     string    `json:"authorUid"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// CreateStatusUpdateRequest represents a request to create a status update.
type CreateStatusUpdateRequest struct {
	StatusPageUID string    `json:"statusPageUid"`
	SectionUID    *string   `json:"sectionUid,omitempty"`
	CheckUID      *string   `json:"checkUid,omitempty"`
	IncidentUID   *string   `json:"incidentUid,omitempty"`
	Title         string    `json:"title"`
	BodyMarkdown  string    `json:"bodyMarkdown"`
	LinkURL       *string   `json:"linkUrl,omitempty"`
	Kind          string    `json:"kind"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
}

// UpdateStatusUpdateRequest represents a request to update a status update (all fields optional).
type UpdateStatusUpdateRequest struct {
	SectionUID   *string    `json:"sectionUid,omitempty"`
	CheckUID     *string    `json:"checkUid,omitempty"`
	IncidentUID  *string    `json:"incidentUid,omitempty"`
	Title        *string    `json:"title,omitempty"`
	BodyMarkdown *string    `json:"bodyMarkdown,omitempty"`
	LinkURL      *string    `json:"linkUrl,omitempty"`
	Kind         *string    `json:"kind,omitempty"`
	PublishedAt  *time.Time `json:"publishedAt,omitempty"`
}

// ListStatusUpdatesOptions holds query options for listing status updates.
type ListStatusUpdatesOptions struct {
	StatusPage string
	Section    string
	Check      string
	Incident   string
	Limit      int
	Offset     int
}

func toResponse(su *models.StatusUpdate) StatusUpdateResponse {
	return StatusUpdateResponse{
		UID:           su.UID,
		StatusPageUID: su.StatusPageUID,
		SectionUID:    su.SectionUID,
		CheckUID:      su.CheckUID,
		IncidentUID:   su.IncidentUID,
		Title:         su.Title,
		BodyMarkdown:  su.BodyMarkdown,
		LinkURL:       su.LinkURL,
		Kind:          string(su.Kind),
		PublishedAt:   su.PublishedAt,
		AuthorUID:     su.AuthorUID,
		CreatedAt:     su.CreatedAt,
		UpdatedAt:     su.UpdatedAt,
	}
}

func validateLinkURL(linkURL *string) error {
	if linkURL == nil || *linkURL == "" {
		return nil
	}

	u, err := url.Parse(*linkURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrInvalidLinkURL
	}

	return nil
}

// ListStatusUpdates returns status updates for an org with filtering.
func (s *Service) ListStatusUpdates(
	ctx context.Context, orgSlug string, opts ListStatusUpdatesOptions,
) ([]StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	filter := models.StatusUpdatesFilter{
		StatusPageUID: opts.StatusPage,
		Limit:         opts.Limit,
		Offset:        opts.Offset,
	}

	if opts.Section != "" {
		filter.SectionUID = &opts.Section
	}

	if opts.Check != "" {
		filter.CheckUID = &opts.Check
	}

	if opts.Incident != "" {
		filter.IncidentUID = &opts.Incident
	}

	updates, err := s.db.ListStatusUpdates(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusUpdateResponse, len(updates))
	for i, u := range updates {
		responses[i] = toResponse(u)
	}

	return responses, nil
}

// CreateStatusUpdate creates a new status update after validation.
func (s *Service) CreateStatusUpdate(
	ctx context.Context, orgSlug, authorUID string, req *CreateStatusUpdateRequest,
) (StatusUpdateResponse, error) {
	// Validate kind
	kind := models.StatusUpdateKind(req.Kind)
	if !kind.IsValid() {
		return StatusUpdateResponse{}, ErrInvalidKind
	}

	// Validate title
	if req.Title == "" {
		return StatusUpdateResponse{}, ErrTitleRequired
	}
	if len(req.Title) > 200 {
		return StatusUpdateResponse{}, ErrTitleTooLong
	}

	// Validate body
	if req.BodyMarkdown == "" {
		return StatusUpdateResponse{}, ErrBodyRequired
	}
	if len(req.BodyMarkdown) > 16384 {
		return StatusUpdateResponse{}, ErrBodyTooLong
	}

	// Validate linkUrl
	if err := validateLinkURL(req.LinkURL); err != nil {
		return StatusUpdateResponse{}, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	// Validate statusPage exists and belongs to org
	page, err := s.db.GetStatusPage(ctx, org.UID, req.StatusPageUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusPageNotFound
		}
		return StatusUpdateResponse{}, err
	}

	if page.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusPageForbidden
	}

	// Validate sectionUID
	if req.SectionUID != nil && *req.SectionUID != "" {
		section, sectErr := s.db.GetStatusPageSection(ctx, page.UID, *req.SectionUID)
		if sectErr != nil || section == nil {
			return StatusUpdateResponse{}, ErrSectionNotFound
		}
	}

	// Validate checkUID (must be a resource on this status page)
	checkUID := req.CheckUID
	if checkUID != nil && *checkUID != "" {
		if err := s.validateCheckOnPage(ctx, page.UID, *checkUID); err != nil {
			return StatusUpdateResponse{}, err
		}
	}

	// Resolve incidentUID — auto-fill checkUID if not set
	if req.IncidentUID != nil && *req.IncidentUID != "" {
		incident, incErr := s.db.GetIncident(ctx, org.UID, *req.IncidentUID)
		if incErr != nil {
			if errors.Is(incErr, sql.ErrNoRows) {
				return StatusUpdateResponse{}, ErrIncidentNotFound
			}
			return StatusUpdateResponse{}, incErr
		}

		if incident.CheckUID != "" {
			if checkUID != nil && *checkUID != "" && *checkUID != incident.CheckUID {
				return StatusUpdateResponse{}, ErrCheckUIDMismatch
			}
			if checkUID == nil || *checkUID == "" {
				checkUID = &incident.CheckUID
			}
		}
	}

	su := models.NewStatusUpdate(org.UID, page.UID, authorUID)
	su.Kind = kind
	su.Title = req.Title
	su.BodyMarkdown = req.BodyMarkdown
	su.LinkURL = req.LinkURL
	su.CheckUID = checkUID
	su.IncidentUID = req.IncidentUID

	if req.SectionUID != nil && *req.SectionUID != "" {
		su.SectionUID = req.SectionUID
	}

	if req.PublishedAt != nil {
		su.PublishedAt = *req.PublishedAt
	}

	if err := s.db.CreateStatusUpdate(ctx, su); err != nil {
		return StatusUpdateResponse{}, err
	}

	// Emit audit event
	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateCreated, models.ActorTypeUser)
	event.ActorUID = &authorUID
	event.Payload["status_update_uid"] = su.UID
	_ = s.db.CreateEvent(ctx, event)

	return toResponse(su), nil
}

// GetStatusUpdate retrieves a single status update by UID, scoped to the org.
func (s *Service) GetStatusUpdate(
	ctx context.Context, orgSlug, uid string,
) (StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	su, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusUpdateNotFound
		}
		return StatusUpdateResponse{}, err
	}

	if su.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusUpdateNotFound
	}

	return toResponse(su), nil
}

// UpdateStatusUpdate applies a partial update to a status update.
func (s *Service) UpdateStatusUpdate(
	ctx context.Context, orgSlug, uid, actorUID string, req *UpdateStatusUpdateRequest,
) (StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	su, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusUpdateNotFound
		}
		return StatusUpdateResponse{}, err
	}

	if su.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusUpdateNotFound
	}

	if req.Kind != nil {
		kind := models.StatusUpdateKind(*req.Kind)
		if !kind.IsValid() {
			return StatusUpdateResponse{}, ErrInvalidKind
		}
		su.Kind = kind
	}

	if req.Title != nil {
		if *req.Title == "" {
			return StatusUpdateResponse{}, ErrTitleRequired
		}
		if len(*req.Title) > 200 {
			return StatusUpdateResponse{}, ErrTitleTooLong
		}
		su.Title = *req.Title
	}

	if req.BodyMarkdown != nil {
		if *req.BodyMarkdown == "" {
			return StatusUpdateResponse{}, ErrBodyRequired
		}
		if len(*req.BodyMarkdown) > 16384 {
			return StatusUpdateResponse{}, ErrBodyTooLong
		}
		su.BodyMarkdown = *req.BodyMarkdown
	}

	if req.LinkURL != nil {
		if err := validateLinkURL(req.LinkURL); err != nil {
			return StatusUpdateResponse{}, err
		}
		su.LinkURL = req.LinkURL
	}

	if req.SectionUID != nil {
		su.SectionUID = req.SectionUID
	}

	if req.CheckUID != nil {
		su.CheckUID = req.CheckUID
	}

	if req.IncidentUID != nil {
		su.IncidentUID = req.IncidentUID
	}

	if req.PublishedAt != nil {
		su.PublishedAt = *req.PublishedAt
	}

	if err := s.db.UpdateStatusUpdate(ctx, su); err != nil {
		return StatusUpdateResponse{}, err
	}

	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateUpdated, models.ActorTypeUser)
	event.ActorUID = &actorUID
	event.Payload["status_update_uid"] = su.UID
	_ = s.db.CreateEvent(ctx, event)

	return toResponse(su), nil
}

// DeleteStatusUpdate soft-deletes a status update.
func (s *Service) DeleteStatusUpdate(
	ctx context.Context, orgSlug, uid, actorUID string,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	su, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStatusUpdateNotFound
		}
		return err
	}

	if su.OrganizationUID != org.UID {
		return ErrStatusUpdateNotFound
	}

	if err := s.db.SoftDeleteStatusUpdate(ctx, uid); err != nil {
		return err
	}

	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateDeleted, models.ActorTypeUser)
	event.ActorUID = &actorUID
	event.Payload["status_update_uid"] = uid
	_ = s.db.CreateEvent(ctx, event)

	return nil
}

// validateCheckOnPage checks that a check UID is registered as a resource on the given status page.
func (s *Service) validateCheckOnPage(ctx context.Context, pageUID, checkUID string) error {
	// List all sections on the page and check each for the resource
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return err
	}

	for _, section := range sections {
		resources, resErr := s.db.ListStatusPageResources(ctx, section.UID)
		if resErr != nil {
			continue
		}
		for _, r := range resources {
			if r.CheckUID == checkUID {
				return nil
			}
		}
	}

	return ErrCheckNotFound
}
