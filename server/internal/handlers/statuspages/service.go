// Package statuspages provides HTTP handlers for status page management endpoints.
package statuspages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// slugRegex validates slug format: lowercase letter, then 2-39 lowercase letters/digits/hyphens.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusPageNotFound is returned when a status page is not found.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrStatusPageSectionNotFound is returned when a section is not found.
	ErrStatusPageSectionNotFound = errors.New("status page section not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrSlugConflict is returned when a slug already exists.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrInvalidSlugFormat is returned when a slug has an invalid format.
	ErrInvalidSlugFormat = errors.New("invalid slug format")
	// ErrReorderUIDsMismatch is returned when a reorder request's UID list
	// does not exactly match the section's current resources (missing,
	// extra, or duplicate UIDs).
	ErrReorderUIDsMismatch = errors.New("reorder uids do not match the section's resources")
)

func validateSlug(slug string) error {
	if slug == "" {
		return nil
	}

	if _, err := uuid.Parse(slug); err == nil {
		return ErrInvalidSlugFormat
	}

	if !slugRegex.MatchString(slug) {
		return ErrInvalidSlugFormat
	}

	return nil
}

// Service provides business logic for status page management.
type Service struct {
	db db.Service
}

// NewService creates a new status pages service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// --- Response types ---

// StatusUpdatePublicResponse represents a status update for public display.
type StatusUpdatePublicResponse struct {
	UID          string    `json:"uid"`
	SectionUID   *string   `json:"sectionUid,omitempty"`
	CheckUID     *string   `json:"checkUid,omitempty"`
	IncidentUID  *string   `json:"incidentUid,omitempty"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	LinkURL      *string   `json:"linkUrl,omitempty"`
	Kind         string    `json:"kind"`
	PublishedAt  time.Time `json:"publishedAt"`
}

// StatusPageResponse represents a status page in API responses.
type StatusPageResponse struct {
	UID              string                       `json:"uid"`
	Name             string                       `json:"name"`
	Slug             string                       `json:"slug"`
	Description      *string                      `json:"description,omitempty"`
	Visibility       string                       `json:"visibility"`
	IsDefault        bool                         `json:"isDefault"`
	Enabled          bool                         `json:"enabled"`
	ShowAvailability bool                         `json:"showAvailability"`
	ShowResponseTime bool                         `json:"showResponseTime"`
	HistoryDays      int                          `json:"historyDays"`
	Language         *string                      `json:"language,omitempty"`
	Sections         []StatusPageSectionResponse  `json:"sections,omitempty"`
	RecentUpdates    []StatusUpdatePublicResponse `json:"recentUpdates,omitempty"`
	CreatedAt        *time.Time                   `json:"createdAt,omitempty"`
}

// StatusPageSectionResponse represents a section in API responses.
type StatusPageSectionResponse struct {
	UID       string                       `json:"uid"`
	Name      string                       `json:"name"`
	Slug      string                       `json:"slug"`
	Position  int                          `json:"position"`
	Resources []StatusPageResourceResponse `json:"resources,omitempty"`
	CreatedAt *time.Time                   `json:"createdAt,omitempty"`
}

// StatusPageResourceResponse represents a resource in API responses.
type StatusPageResourceResponse struct {
	UID          string                    `json:"uid"`
	CheckUID     string                    `json:"checkUid"`
	PublicName   *string                   `json:"publicName,omitempty"`
	Explanation  *string                   `json:"explanation,omitempty"`
	Position     int                       `json:"position"`
	Check        *ResourceCheckInfo        `json:"check,omitempty"`
	Availability *ResourceAvailabilityData `json:"availability,omitempty"`
	CreatedAt    *time.Time                `json:"createdAt,omitempty"`
}

// ResourceCheckInfo contains live check data for a resource.
type ResourceCheckInfo struct {
	Name   *string `json:"name,omitempty"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
	// InMaintenance is true when the check is inside an active maintenance
	// window at request time, so the public page can show a "Scheduled
	// Maintenance" badge instead of a raw up/down state.
	InMaintenance bool `json:"inMaintenance"`
}

// ResourceAvailabilityData contains availability and performance data for public display.
type ResourceAvailabilityData struct {
	OverallAvailabilityPct *float64                 `json:"overallAvailabilityPct,omitempty"`
	DailyAvailability      []DailyAvailabilityPoint `json:"dailyAvailability,omitempty"`
	ResponseTimeData       []ResponseTimePoint      `json:"responseTimeData,omitempty"`
}

// DailyAvailabilityPoint represents availability data for a single day.
type DailyAvailabilityPoint struct {
	Date            string  `json:"date"`
	AvailabilityPct float64 `json:"availabilityPct"`
	Status          string  `json:"status"`
}

// ResponseTimePoint represents response time data for a single time period (hourly granularity).
type ResponseTimePoint struct {
	Time        string   `json:"time"`
	DurationP95 *float32 `json:"durationP95,omitempty"`
	// Status indicates the check's outcome at this point: "up", "down",
	// "timeout", "error", or "" when not applicable.
	Status string `json:"status,omitempty"`
}

// --- Request types ---

// CreateStatusPageRequest represents a request to create a status page.
type CreateStatusPageRequest struct {
	Name             string  `json:"name"`
	Slug             string  `json:"slug"`
	Description      *string `json:"description,omitempty"`
	Visibility       *string `json:"visibility,omitempty"`
	IsDefault        *bool   `json:"isDefault,omitempty"`
	ShowAvailability *bool   `json:"showAvailability,omitempty"`
	ShowResponseTime *bool   `json:"showResponseTime,omitempty"`
	HistoryDays      *int    `json:"historyDays,omitempty"`
	Language         *string `json:"language,omitempty"`
}

// UpdateStatusPageRequest represents a request to update a status page.
type UpdateStatusPageRequest struct {
	Name             *string `json:"name,omitempty"`
	Slug             *string `json:"slug,omitempty"`
	Description      *string `json:"description,omitempty"`
	Visibility       *string `json:"visibility,omitempty"`
	IsDefault        *bool   `json:"isDefault,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	ShowAvailability *bool   `json:"showAvailability,omitempty"`
	ShowResponseTime *bool   `json:"showResponseTime,omitempty"`
	HistoryDays      *int    `json:"historyDays,omitempty"`
	Language         *string `json:"language,omitempty"`
}

// CreateSectionRequest represents a request to create a section.
type CreateSectionRequest struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Position *int   `json:"position,omitempty"`
}

// UpdateSectionRequest represents a request to update a section.
type UpdateSectionRequest struct {
	Name     *string `json:"name,omitempty"`
	Slug     *string `json:"slug,omitempty"`
	Position *int    `json:"position,omitempty"`
}

// CreateResourceRequest represents a request to add a check to a section.
type CreateResourceRequest struct {
	CheckUID    string  `json:"checkUid"`
	PublicName  *string `json:"publicName,omitempty"`
	Explanation *string `json:"explanation,omitempty"`
	Position    *int    `json:"position,omitempty"`
}

// UpdateResourceRequest represents a request to update a resource.
type UpdateResourceRequest struct {
	PublicName  *string `json:"publicName,omitempty"`
	Explanation *string `json:"explanation,omitempty"`
	Position    *int    `json:"position,omitempty"`
}

// --- Options ---

// GetStatusPageOptions contains optional parameters for getting a status page.
type GetStatusPageOptions struct {
	IncludeSections bool
}

// --- StatusPage CRUD ---

// ListStatusPages retrieves all status pages for an organization.
func (s *Service) ListStatusPages(ctx context.Context, orgSlug string) ([]StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	pages, err := s.db.ListStatusPages(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageResponse, len(pages))
	for i, page := range pages {
		responses[i] = convertPageToResponse(page)
	}

	return responses, nil
}

// applyCreateFields sets optional fields from the create request onto the page model.
func applyCreateFields(page *models.StatusPage, req *CreateStatusPageRequest) {
	if req.Description != nil {
		page.Description = req.Description
	}

	if req.Visibility != nil {
		page.Visibility = *req.Visibility
	}

	if req.ShowAvailability != nil {
		page.ShowAvailability = *req.ShowAvailability
	}

	if req.ShowResponseTime != nil {
		page.ShowResponseTime = *req.ShowResponseTime
	}

	if req.HistoryDays != nil {
		page.HistoryDays = *req.HistoryDays
	}

	if req.Language != nil {
		page.Language = req.Language
	}
}

// CreateStatusPage creates a new status page.
func (s *Service) CreateStatusPage(
	ctx context.Context, orgSlug string, req *CreateStatusPageRequest,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	if errSlug := validateSlug(req.Slug); errSlug != nil {
		return StatusPageResponse{}, errSlug
	}

	// Check slug conflict
	existing, err := s.db.GetStatusPageBySlug(ctx, org.UID, req.Slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StatusPageResponse{}, err
	}
	if existing != nil {
		return StatusPageResponse{}, ErrSlugConflict
	}

	page := models.NewStatusPage(org.UID, req.Name, req.Slug)
	applyCreateFields(page, req)

	// Check if this should be default (first page or explicitly set)
	existingPages, _ := s.db.ListStatusPages(ctx, org.UID)
	if len(existingPages) == 0 {
		page.IsDefault = true
	} else if req.IsDefault != nil && *req.IsDefault {
		if errClear := s.clearDefaultStatusPage(ctx, org.UID); errClear != nil {
			return StatusPageResponse{}, errClear
		}
		page.IsDefault = true
	}

	if errCreate := s.db.CreateStatusPage(ctx, page); errCreate != nil {
		return StatusPageResponse{}, errCreate
	}

	return convertPageToResponse(page), nil
}

// GetStatusPage retrieves a single status page by UID or slug.
func (s *Service) GetStatusPage(
	ctx context.Context, orgSlug, identifier string, opts GetStatusPageOptions,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	response := convertPageToResponse(page)

	if opts.IncludeSections {
		sections, err := s.loadSectionsWithResources(ctx, page.UID)
		if err != nil {
			return StatusPageResponse{}, err
		}

		// Enrich resources with live check data
		for i := range sections {
			for j := range sections[i].Resources {
				checkInfo, infoErr := s.getCheckInfo(ctx, org.UID, sections[i].Resources[j].CheckUID)
				if infoErr == nil {
					sections[i].Resources[j].Check = checkInfo
				}
			}
		}

		response.Sections = sections
	}

	return response, nil
}

// UpdateStatusPage updates an existing status page.
func (s *Service) UpdateStatusPage(
	ctx context.Context, orgSlug, identifier string, req *UpdateStatusPageRequest,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	if errVal := s.validatePageSlugChange(ctx, org.UID, page.Slug, req.Slug); errVal != nil {
		return StatusPageResponse{}, errVal
	}

	// Handle default toggle
	if req.IsDefault != nil && *req.IsDefault && !page.IsDefault {
		if errClear := s.clearDefaultStatusPage(ctx, org.UID); errClear != nil {
			return StatusPageResponse{}, errClear
		}
	}

	update := models.StatusPageUpdate{
		Name:             req.Name,
		Slug:             req.Slug,
		Description:      req.Description,
		Visibility:       req.Visibility,
		IsDefault:        req.IsDefault,
		Enabled:          req.Enabled,
		ShowAvailability: req.ShowAvailability,
		ShowResponseTime: req.ShowResponseTime,
		HistoryDays:      req.HistoryDays,
		Language:         req.Language,
	}

	if errUpdate := s.db.UpdateStatusPage(ctx, page.UID, &update); errUpdate != nil {
		return StatusPageResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPage(ctx, org.UID, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	return convertPageToResponse(updated), nil
}

// DeleteStatusPage soft-deletes a status page.
func (s *Service) DeleteStatusPage(ctx context.Context, orgSlug, identifier string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return ErrStatusPageNotFound
	}

	return s.db.DeleteStatusPage(ctx, page.UID)
}

// --- Section CRUD ---

// ListSections retrieves all sections for a status page.
func (s *Service) ListSections(
	ctx context.Context, orgSlug, pageIdentifier string,
) ([]StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	sections, err := s.db.ListStatusPageSections(ctx, page.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageSectionResponse, len(sections))
	for i, section := range sections {
		responses[i] = convertSectionToResponse(section)
	}

	return responses, nil
}

// resolveSectionPosition picks the position for a new section: the caller's
// requested value if any, otherwise max(existing) + 1 so the section appends
// at the end. Defaulting to 0 would force every new section to share
// position=0, breaking the swap-based reorder UI.
func (s *Service) resolveSectionPosition(ctx context.Context, pageUID string, requested *int) (int, error) {
	if requested != nil {
		return *requested, nil
	}

	maxPosition, err := s.db.MaxStatusPageSectionPosition(ctx, pageUID)
	if err != nil {
		return 0, fmt.Errorf("failed to compute next section position: %w", err)
	}

	return maxPosition + 1, nil
}

// resolveResourcePosition is the per-section equivalent of resolveSectionPosition.
func (s *Service) resolveResourcePosition(ctx context.Context, sectionUID string, requested *int) (int, error) {
	if requested != nil {
		return *requested, nil
	}

	maxPosition, err := s.db.MaxStatusPageResourcePosition(ctx, sectionUID)
	if err != nil {
		return 0, fmt.Errorf("failed to compute next resource position: %w", err)
	}

	return maxPosition + 1, nil
}

// CreateSection creates a new section within a status page.
func (s *Service) CreateSection(
	ctx context.Context, orgSlug, pageIdentifier string, req CreateSectionRequest,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	if errSlug := validateSlug(req.Slug); errSlug != nil {
		return StatusPageSectionResponse{}, errSlug
	}

	// Check slug conflict within the page
	existing, errGet := s.db.GetStatusPageSectionBySlug(ctx, page.UID, req.Slug)
	if errGet != nil && !errors.Is(errGet, sql.ErrNoRows) {
		return StatusPageSectionResponse{}, errGet
	}
	if existing != nil {
		return StatusPageSectionResponse{}, ErrSlugConflict
	}

	position, err := s.resolveSectionPosition(ctx, page.UID, req.Position)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section := models.NewStatusPageSection(page.UID, req.Name, req.Slug, position)

	if errCreate := s.db.CreateStatusPageSection(ctx, section); errCreate != nil {
		return StatusPageSectionResponse{}, errCreate
	}

	return convertSectionToResponse(section), nil
}

// GetSection retrieves a single section.
func (s *Service) GetSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	return convertSectionToResponse(section), nil
}

// UpdateSection updates an existing section.
func (s *Service) UpdateSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, req UpdateSectionRequest,
) (StatusPageSectionResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" && *req.Slug != section.Slug {
		if errSlug := validateSlug(*req.Slug); errSlug != nil {
			return StatusPageSectionResponse{}, errSlug
		}
		existing, errCheck := s.db.GetStatusPageSectionBySlug(ctx, page.UID, *req.Slug)
		if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
			return StatusPageSectionResponse{}, errCheck
		}
		if existing != nil {
			return StatusPageSectionResponse{}, ErrSlugConflict
		}
	}

	update := models.StatusPageSectionUpdate{
		Name:     req.Name,
		Slug:     req.Slug,
		Position: req.Position,
	}

	if errUpdate := s.db.UpdateStatusPageSection(ctx, section.UID, &update); errUpdate != nil {
		return StatusPageSectionResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPageSection(ctx, page.UID, section.UID)
	if err != nil {
		return StatusPageSectionResponse{}, err
	}

	return convertSectionToResponse(updated), nil
}

// DeleteSection soft-deletes a section.
func (s *Service) DeleteSection(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return err
	}

	return s.db.DeleteStatusPageSection(ctx, section.UID)
}

// --- Resource CRUD ---

// ListResources retrieves all resources for a section.
func (s *Service) ListResources(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string,
) ([]StatusPageResourceResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return nil, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return nil, err
	}

	resources, err := s.db.ListStatusPageResources(ctx, section.UID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageResourceResponse, len(resources))
	for i, resource := range resources {
		responses[i] = convertResourceToResponse(resource)
	}

	return responses, nil
}

// CreateResource adds a check to a section.
func (s *Service) CreateResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, req CreateResourceRequest,
) (StatusPageResourceResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResourceResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, pageIdentifier)
	if err != nil || page == nil {
		return StatusPageResourceResponse{}, ErrStatusPageNotFound
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	// Verify the check exists in this org
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, req.CheckUID)
	if err != nil || check == nil {
		return StatusPageResourceResponse{}, ErrCheckNotFound
	}

	position, err := s.resolveResourcePosition(ctx, section.UID, req.Position)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	resource := models.NewStatusPageResource(section.UID, check.UID, position)
	resource.PublicName = req.PublicName
	resource.Explanation = req.Explanation

	if err := s.db.CreateStatusPageResource(ctx, resource); err != nil {
		return StatusPageResourceResponse{}, fmt.Errorf("failed to create resource: %w", err)
	}

	return convertResourceToResponse(resource), nil
}

// UpdateResource updates a resource.
func (s *Service) UpdateResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier, resourceUID string,
	req UpdateResourceRequest,
) (StatusPageResourceResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	update := models.StatusPageResourceUpdate{
		PublicName:  req.PublicName,
		Explanation: req.Explanation,
		Position:    req.Position,
	}

	if errUpdate := s.db.UpdateStatusPageResource(ctx, resourceUID, &update); errUpdate != nil {
		return StatusPageResourceResponse{}, errUpdate
	}

	updated, err := s.db.GetStatusPageResource(ctx, section.UID, resourceUID)
	if err != nil {
		return StatusPageResourceResponse{}, err
	}

	return convertResourceToResponse(updated), nil
}

// ReorderResources rewrites the section's resources so that orderedUIDs[i]
// gets position i+1. Validates that orderedUIDs is exactly the current set
// of resources in the section before applying — partial or stale orderings
// are rejected so the dashboard can't accidentally drop or duplicate rows.
func (s *Service) ReorderResources(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier string, orderedUIDs []string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	section, err := s.resolveSection(ctx, page.UID, sectionIdentifier)
	if err != nil {
		return err
	}

	existing, err := s.db.ListStatusPageResources(ctx, section.UID)
	if err != nil {
		return fmt.Errorf("failed to list resources: %w", err)
	}
	if len(existing) != len(orderedUIDs) {
		return ErrReorderUIDsMismatch
	}
	known := make(map[string]struct{}, len(existing))
	for _, r := range existing {
		known[r.UID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(orderedUIDs))
	for _, uid := range orderedUIDs {
		if _, ok := known[uid]; !ok {
			return ErrReorderUIDsMismatch
		}
		if _, dup := seen[uid]; dup {
			return ErrReorderUIDsMismatch
		}
		seen[uid] = struct{}{}
	}

	return s.db.ReorderStatusPageResources(ctx, section.UID, orderedUIDs)
}

// ReorderSections rewrites the page's sections so that orderedUIDs[i] gets
// position i+1. Validates that orderedUIDs is exactly the current set of
// sections on the page before applying — partial or stale orderings are
// rejected so the dashboard can't accidentally drop or duplicate sections.
func (s *Service) ReorderSections(
	ctx context.Context, orgSlug, pageIdentifier string, orderedUIDs []string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	existing, err := s.db.ListStatusPageSections(ctx, page.UID)
	if err != nil {
		return fmt.Errorf("failed to list sections: %w", err)
	}
	if len(existing) != len(orderedUIDs) {
		return ErrReorderUIDsMismatch
	}
	known := make(map[string]struct{}, len(existing))
	for _, sec := range existing {
		known[sec.UID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(orderedUIDs))
	for _, uid := range orderedUIDs {
		if _, ok := known[uid]; !ok {
			return ErrReorderUIDsMismatch
		}
		if _, dup := seen[uid]; dup {
			return ErrReorderUIDsMismatch
		}
		seen[uid] = struct{}{}
	}

	return s.db.ReorderStatusPageSections(ctx, page.UID, orderedUIDs)
}

// DeleteResource removes a check from a section (hard delete).
func (s *Service) DeleteResource(
	ctx context.Context, orgSlug, pageIdentifier, sectionIdentifier, resourceUID string,
) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, pageIdentifier)
	if err != nil {
		return err
	}

	if _, err := s.resolveSection(ctx, page.UID, sectionIdentifier); err != nil {
		return err
	}

	return s.db.DeleteStatusPageResource(ctx, resourceUID)
}

// --- Public view ---

// ViewStatusPage returns a public view of a status page with sections, resources, and live check status.
//
//nolint:cyclop // building the composite response requires checking many optional fields
func (s *Service) ViewStatusPage(
	ctx context.Context, orgSlug, slug string,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageBySlug(ctx, org.UID, slug)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	if !page.Enabled || page.Visibility != "public" {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	response := convertPageToResponse(page)

	sections, err := s.loadSectionsWithResources(ctx, page.UID)
	if err != nil {
		return StatusPageResponse{}, err
	}

	// Enrich resources with live check data
	for i := range sections {
		for j := range sections[i].Resources {
			checkInfo, checkErr := s.getCheckInfo(ctx, org.UID, sections[i].Resources[j].CheckUID)
			if checkErr == nil {
				sections[i].Resources[j].Check = checkInfo
			}
		}
	}

	// Enrich resources with availability data
	if page.ShowAvailability || page.ShowResponseTime {
		s.enrichWithAvailability(ctx, org.UID, page, sections)
	}

	response.Sections = sections

	// Populate recent status updates (graceful — empty when table doesn't exist yet)
	if page.HistoryDays > 0 {
		updates, updErr := s.db.ListPublicStatusUpdates(ctx, page.UID, page.HistoryDays)
		if updErr == nil && len(updates) > 0 {
			recentUpdates := make([]StatusUpdatePublicResponse, len(updates))
			for i, upd := range updates {
				recentUpdates[i] = StatusUpdatePublicResponse{
					UID:          upd.UID,
					SectionUID:   upd.SectionUID,
					CheckUID:     upd.CheckUID,
					IncidentUID:  upd.IncidentUID,
					Title:        upd.Title,
					BodyMarkdown: upd.BodyMarkdown,
					LinkURL:      upd.LinkURL,
					Kind:         upd.Kind,
					PublishedAt:  upd.PublishedAt,
				}
			}

			response.RecentUpdates = recentUpdates
		}
	}

	return response, nil
}

// ViewDefaultStatusPage returns the default status page for an organization.
func (s *Service) ViewDefaultStatusPage(
	ctx context.Context, orgSlug string,
) (StatusPageResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusPageResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetDefaultStatusPage(ctx, org.UID)
	if err != nil || page == nil {
		return StatusPageResponse{}, ErrStatusPageNotFound
	}

	return s.ViewStatusPage(ctx, orgSlug, page.Slug)
}

// --- Availability enrichment ---

//nolint:cyclop,funlen // Availability enrichment has inherent conditional complexity
func (s *Service) enrichWithAvailability(
	ctx context.Context, orgUID string, page *models.StatusPage, sections []StatusPageSectionResponse,
) {
	// Collect all check UIDs
	var checkUIDs []string
	for i := range sections {
		for j := range sections[i].Resources {
			checkUIDs = append(checkUIDs, sections[i].Resources[j].CheckUID)
		}
	}

	if len(checkUIDs) == 0 {
		return
	}

	// Fetch daily results for all checks in a single query
	startDate := time.Now().AddDate(0, 0, -page.HistoryDays)
	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{"day"},
		PeriodStartAfter: &startDate,
		Limit:            page.HistoryDays * len(checkUIDs),
	}

	resultsResp, err := s.db.ListResults(ctx, filter)
	if err != nil || resultsResp == nil {
		return
	}

	// Group results by check UID
	resultsByCheck := make(map[string][]*models.Result)
	for _, result := range resultsResp.Results {
		resultsByCheck[result.CheckUID] = append(resultsByCheck[result.CheckUID], result)
	}

	// Fetch hourly results across the whole history window so we can synthesize daily
	// buckets for any day where the rolled-up daily row hasn't been produced yet (the
	// aggregator only rolls hour→day once hourly data is older than RetentionHour days
	// — 30 days by default — so new installs have no daily rows for ~30 days).
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	historyStartUTC := todayStart.AddDate(0, 0, -(page.HistoryDays - 1))

	hourlyFilter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{"hour"},
		PeriodStartAfter: &historyStartUTC,
		Limit:            page.HistoryDays * hoursPerDay * len(checkUIDs),
	}

	hourlyResp, err := s.db.ListResults(ctx, hourlyFilter)
	if err == nil && hourlyResp != nil {
		hourlyByCheck := make(map[string][]*models.Result)
		for _, r := range hourlyResp.Results {
			hourlyByCheck[r.CheckUID] = append(hourlyByCheck[r.CheckUID], r)
		}

		synthesizeMissingDailyBuckets(resultsByCheck, hourlyByCheck, checkUIDs, todayStart, page.HistoryDays)
	}

	// Final fallback for today: hourly rollups lag 24h (RetentionRaw default), so
	// today's bucket is empty for most of the day even on a healthy install.
	// Sample the most recent raw rows for any check still missing a today bucket.
	s.fillTodayFromRaw(ctx, orgUID, resultsByCheck, checkUIDs, todayStart)

	// Fetch the last 100 results per check (any period type) for the response time chart
	recentByCheck := make(map[string][]*models.Result)

	if page.ShowResponseTime {
		const responseTimeLimit = 100

		recentFilter := &models.ListResultsFilter{
			OrganizationUID: orgUID,
			CheckUIDs:       checkUIDs,
			Limit:           responseTimeLimit * len(checkUIDs),
		}

		recentResp, recentErr := s.db.ListResults(ctx, recentFilter)
		if recentErr == nil && recentResp != nil {
			for _, r := range recentResp.Results {
				if len(recentByCheck[r.CheckUID]) < responseTimeLimit {
					recentByCheck[r.CheckUID] = append(recentByCheck[r.CheckUID], r)
				}
			}
		}
	}

	// Build availability data for each resource
	for i := range sections {
		for j := range sections[i].Resources {
			checkUID := sections[i].Resources[j].CheckUID
			results := resultsByCheck[checkUID]
			recentResults := recentByCheck[checkUID]
			availData := buildAvailabilityData(
				results, recentResults, page.HistoryDays, page.ShowAvailability, page.ShowResponseTime,
			)
			sections[i].Resources[j].Availability = availData
		}
	}
}

// hoursPerDay caps how many hourly rows we ever expect per day per check.
const hoursPerDay = 24

// synthesizeMissingDailyBuckets fills any UTC day in the history window
// [todayStart - (historyDays-1), todayStart] that lacks a stored daily row
// with a synthetic daily row computed from hourly data for that UTC day. The
// row for "today" is always rebuilt from hourly (today's stored daily, if
// any, is in-progress and stale relative to live hourly buckets).
//
// This matters because the aggregator only rolls hour→day once hourly data
// is older than RetentionHour days (30 by default), so on fresh installs the
// status page would otherwise render an all-grey row for 30 days.
func synthesizeMissingDailyBuckets(
	resultsByCheck, hourlyByCheck map[string][]*models.Result,
	checkUIDs []string,
	todayStart time.Time,
	historyDays int,
) {
	todayStr := todayStart.Format("2006-01-02")

	for _, checkUID := range checkUIDs {
		// Index existing daily rows by UTC date string.
		existingByDate := make(map[string]*models.Result, len(resultsByCheck[checkUID]))
		for _, r := range resultsByCheck[checkUID] {
			existingByDate[r.PeriodStart.UTC().Format("2006-01-02")] = r
		}

		// Group hourly rows by UTC date string.
		hourlyByDate := make(map[string][]*models.Result)
		for _, h := range hourlyByCheck[checkUID] {
			key := h.PeriodStart.UTC().Format("2006-01-02")
			hourlyByDate[key] = append(hourlyByDate[key], h)
		}

		for dayOffset := 0; dayOffset < historyDays; dayOffset++ {
			day := todayStart.AddDate(0, 0, -dayOffset)
			dateStr := day.Format("2006-01-02")

			_, hasDaily := existingByDate[dateStr]
			if hasDaily && dateStr != todayStr {
				continue
			}

			synth := aggregateHourlyToDaily(checkUID, hourlyByDate[dateStr], day)
			if synth == nil {
				continue
			}

			existingByDate[dateStr] = synth
		}

		// Rebuild the per-check slice from the merged map.
		merged := make([]*models.Result, 0, len(existingByDate))
		for _, r := range existingByDate {
			merged = append(merged, r)
		}
		resultsByCheck[checkUID] = merged
	}
}

// rawSamplesPerCheck caps how many raw rows we sample per check when synthesizing
// today's availability from raw data. 500 evenly-spaced samples give a stable
// percentage without loading the full day's raw firehose into memory.
const rawSamplesPerCheck = 500

// fillTodayFromRaw queries raw results for `today` and synthesizes a daily
// bucket for any check that still lacks one (the hourly synth had nothing to
// average — typical in the first 24h of a new install, or for today since the
// raw→hour aggregator runs with RetentionRaw lag). Order DESC by period_start
// means the sampled rows are the freshest available.
func (s *Service) fillTodayFromRaw(
	ctx context.Context, orgUID string,
	resultsByCheck map[string][]*models.Result,
	checkUIDs []string,
	todayStart time.Time,
) {
	todayStr := todayStart.Format("2006-01-02")

	var missing []string
	for _, checkUID := range checkUIDs {
		hasToday := false
		for _, r := range resultsByCheck[checkUID] {
			if r.PeriodStart.UTC().Format("2006-01-02") == todayStr {
				hasToday = true

				break
			}
		}

		if !hasToday {
			missing = append(missing, checkUID)
		}
	}

	if len(missing) == 0 {
		return
	}

	rawFilter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        missing,
		PeriodTypes:      []string{"raw"},
		PeriodStartAfter: &todayStart,
		Limit:            rawSamplesPerCheck * len(missing),
	}

	rawResp, err := s.db.ListResults(ctx, rawFilter)
	if err != nil || rawResp == nil {
		return
	}

	rawByCheck := make(map[string][]*models.Result)
	for _, r := range rawResp.Results {
		if len(rawByCheck[r.CheckUID]) < rawSamplesPerCheck {
			rawByCheck[r.CheckUID] = append(rawByCheck[r.CheckUID], r)
		}
	}

	for _, checkUID := range missing {
		synth := aggregateRawToDaily(checkUID, rawByCheck[checkUID], todayStart)
		if synth == nil {
			continue
		}

		resultsByCheck[checkUID] = append(resultsByCheck[checkUID], synth)
	}
}

// aggregateRawToDaily derives a synthetic daily Result from raw rows.
// Availability = (status==up count) / total; duration is averaged over
// successful rows only (failures often have skewed/zero durations).
func aggregateRawToDaily(checkUID string, rawResults []*models.Result, periodStart time.Time) *models.Result {
	var (
		upCount       int
		total         int
		durationSum   float64
		durationCount int
	)

	for _, rawResult := range rawResults {
		if rawResult.Status == nil {
			continue
		}

		total++

		if *rawResult.Status == int(models.ResultStatusUp) {
			upCount++

			if rawResult.Duration != nil {
				durationSum += float64(*rawResult.Duration)
				durationCount++
			}
		}
	}

	if total == 0 {
		return nil
	}

	avail := 100.0 * float64(upCount) / float64(total)
	out := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     periodStart,
		AvailabilityPct: &avail,
		TotalChecks:     &total,
	}

	if durationCount > 0 {
		avgDur := float32(durationSum / float64(durationCount))
		out.Duration = &avgDur
		out.DurationP95 = &avgDur
	}

	return out
}

// aggregateHourlyToDaily averages a check's hourly rows into a synthetic
// daily result anchored at periodStart. Returns nil if no hourly row has an
// availability value (no signal to derive a day from).
func aggregateHourlyToDaily(checkUID string, hourlyResults []*models.Result, periodStart time.Time) *models.Result {
	var totalAvail, totalDuration, totalDurationP95 float64

	var count int

	for _, hourlyResult := range hourlyResults {
		if hourlyResult.AvailabilityPct != nil {
			totalAvail += *hourlyResult.AvailabilityPct
			count++
		}

		if hourlyResult.Duration != nil {
			totalDuration += float64(*hourlyResult.Duration)
		}

		if hourlyResult.DurationP95 != nil {
			totalDurationP95 += float64(*hourlyResult.DurationP95)
		}
	}

	if count == 0 {
		return nil
	}

	avgAvail := totalAvail / float64(count)
	avgDuration := float32(totalDuration / float64(count))
	avgP95 := float32(totalDurationP95 / float64(count))

	return &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     periodStart,
		AvailabilityPct: &avgAvail,
		Duration:        &avgDuration,
		DurationP95:     &avgP95,
		TotalChecks:     &count,
	}
}

func buildAvailabilityData(
	dailyResults, recentResults []*models.Result, historyDays int, showAvailability, showResponseTime bool,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	// Index daily results by UTC date string. The aggregator writes daily rows
	// at midnight UTC; reading them back in local time would mis-bucket any row
	// whose UTC date differs from the server's local date.
	resultsByDate := make(map[string]*models.Result)
	for _, result := range dailyResults {
		dateStr := result.PeriodStart.UTC().Format("2006-01-02")
		resultsByDate[dateStr] = result
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	if showAvailability {
		daily := make([]DailyAvailabilityPoint, 0, historyDays)

		var totalWeightedAvail float64

		var totalChecksSum int

		for dayOffset := historyDays - 1; dayOffset >= 0; dayOffset-- {
			day := today.AddDate(0, 0, -dayOffset)
			dateStr := day.Format("2006-01-02")

			point := DailyAvailabilityPoint{
				Date:   dateStr,
				Status: "noData",
			}

			if result, ok := resultsByDate[dateStr]; ok && result.AvailabilityPct != nil {
				point.AvailabilityPct = *result.AvailabilityPct
				point.Status = availabilityToStatus(*result.AvailabilityPct)

				checks := 1
				if result.TotalChecks != nil {
					checks = *result.TotalChecks
				}

				totalWeightedAvail += *result.AvailabilityPct * float64(checks)
				totalChecksSum += checks
			}

			daily = append(daily, point)
		}

		data.DailyAvailability = daily

		if totalChecksSum > 0 {
			overall := totalWeightedAvail / float64(totalChecksSum)
			data.OverallAvailabilityPct = &overall
		}
	}

	if showResponseTime {
		data.ResponseTimeData = buildResponseTimeData(recentResults)
	}

	return data
}

func buildResponseTimeData(recentResults []*models.Result) []ResponseTimePoint {
	rtData := make([]ResponseTimePoint, 0, len(recentResults))

	for _, recentResult := range recentResults {
		var duration *float32
		if recentResult.DurationP95 != nil {
			duration = recentResult.DurationP95
		} else if recentResult.Duration != nil {
			duration = recentResult.Duration
		}

		var statusStr string
		if recentResult.Status != nil {
			statusStr = strings.ToLower(models.StatusToString(*recentResult.Status))
		}

		rtData = append(rtData, ResponseTimePoint{
			Time:        recentResult.PeriodStart.UTC().Format(time.RFC3339),
			DurationP95: duration,
			Status:      statusStr,
		})
	}

	for i, j := 0, len(rtData)-1; i < j; i, j = i+1, j-1 {
		rtData[i], rtData[j] = rtData[j], rtData[i]
	}

	return rtData
}

func availabilityToStatus(pct float64) string {
	switch {
	case pct >= 99.9:
		return "up"
	case pct >= 99.0:
		return "degraded"
	default:
		return "down"
	}
}

// --- Helpers ---

func (s *Service) resolveStatusPage(
	ctx context.Context, orgSlug, identifier string,
) (*models.StatusPage, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPageByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	return page, nil
}

func (s *Service) resolveSection(
	ctx context.Context, pageUID, identifier string,
) (*models.StatusPageSection, error) {
	var section *models.StatusPageSection
	var err error

	if _, parseErr := uuid.Parse(identifier); parseErr == nil {
		section, err = s.db.GetStatusPageSection(ctx, pageUID, identifier)
	} else {
		section, err = s.db.GetStatusPageSectionBySlug(ctx, pageUID, identifier)
	}

	if err != nil || section == nil {
		return nil, ErrStatusPageSectionNotFound
	}

	return section, nil
}

func (s *Service) validatePageSlugChange(
	ctx context.Context, orgUID, currentSlug string, newSlug *string,
) error {
	if newSlug == nil || *newSlug == "" || *newSlug == currentSlug {
		return nil
	}

	if errSlug := validateSlug(*newSlug); errSlug != nil {
		return errSlug
	}

	existing, errCheck := s.db.GetStatusPageBySlug(ctx, orgUID, *newSlug)
	if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
		return errCheck
	}

	if existing != nil {
		return ErrSlugConflict
	}

	return nil
}

func (s *Service) clearDefaultStatusPage(ctx context.Context, orgUID string) error {
	pages, err := s.db.ListStatusPages(ctx, orgUID)
	if err != nil {
		return err
	}

	for _, p := range pages {
		if p.IsDefault {
			falseVal := false
			if err := s.db.UpdateStatusPage(ctx, p.UID, &models.StatusPageUpdate{IsDefault: &falseVal}); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Service) loadSectionsWithResources(
	ctx context.Context, pageUID string,
) ([]StatusPageSectionResponse, error) {
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusPageSectionResponse, len(sections))
	for i, section := range sections {
		responses[i] = convertSectionToResponse(section)

		resources, err := s.db.ListStatusPageResources(ctx, section.UID)
		if err != nil {
			return nil, err
		}

		resourceResponses := make([]StatusPageResourceResponse, len(resources))
		for j, resource := range resources {
			resourceResponses[j] = convertResourceToResponse(resource)
		}
		responses[i].Resources = resourceResponses
	}

	return responses, nil
}

func (s *Service) getCheckInfo(ctx context.Context, orgUID, checkUID string) (*ResourceCheckInfo, error) {
	check, err := s.db.GetCheck(ctx, orgUID, checkUID)
	if err != nil {
		return nil, err
	}

	statusStr := "created"

	switch check.Status {
	case models.CheckStatusCreated:
		statusStr = "created"
	case models.CheckStatusUp:
		statusStr = "up"
	case models.CheckStatusDown:
		statusStr = "down"
	case models.CheckStatusValidating:
		// Validating is a transient internal state — the public status page
		// should still read "up" until the failure is confirmed and an
		// incident opens.
		statusStr = "up"
	case models.CheckStatusWarning:
		// Live "up, but something to report" — surfaced amber on the public
		// page (counts as up for availability, but is not hidden like
		// validating: the operator deliberately flagged something).
		statusStr = "warning"
	case models.CheckStatusDegraded:
		statusStr = "degraded"
	}

	// Flag the resource as under maintenance when the check sits inside an
	// active maintenance window right now. A lookup error must never take the
	// public page down, so we log nothing fatal and default to false.
	inMaintenance := false

	if windows, errMW := s.db.ListMaintenanceWindowsForCheck(ctx, checkUID); errMW == nil {
		now := time.Now()
		for _, w := range windows {
			if models.IsActiveAt(w, now) {
				inMaintenance = true

				break
			}
		}
	}

	return &ResourceCheckInfo{
		Name:          check.Name,
		Type:          check.Type,
		Status:        statusStr,
		InMaintenance: inMaintenance,
	}, nil
}

func convertPageToResponse(page *models.StatusPage) StatusPageResponse {
	return StatusPageResponse{
		UID:              page.UID,
		Name:             page.Name,
		Slug:             page.Slug,
		Description:      page.Description,
		Visibility:       page.Visibility,
		IsDefault:        page.IsDefault,
		Enabled:          page.Enabled,
		ShowAvailability: page.ShowAvailability,
		ShowResponseTime: page.ShowResponseTime,
		HistoryDays:      page.HistoryDays,
		Language:         page.Language,
		CreatedAt:        &page.CreatedAt,
	}
}

func convertSectionToResponse(section *models.StatusPageSection) StatusPageSectionResponse {
	return StatusPageSectionResponse{
		UID:       section.UID,
		Name:      section.Name,
		Slug:      section.Slug,
		Position:  section.Position,
		CreatedAt: &section.CreatedAt,
	}
}

func convertResourceToResponse(resource *models.StatusPageResource) StatusPageResourceResponse {
	return StatusPageResourceResponse{
		UID:         resource.UID,
		CheckUID:    resource.CheckUID,
		PublicName:  resource.PublicName,
		Explanation: resource.Explanation,
		Position:    resource.Position,
		CreatedAt:   &resource.CreatedAt,
	}
}
