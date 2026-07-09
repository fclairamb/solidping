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

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
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
	// ErrInvalidHistoryPeriod is returned when historyPeriod is not one of the
	// supported enum values (24h, 7d, 30d, 90d).
	ErrInvalidHistoryPeriod = errors.New("invalid history period")
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

// validateHistoryPeriod rejects a non-nil historyPeriod that is not one of the
// supported enum values. A nil pointer (field omitted) is valid.
func validateHistoryPeriod(period *string) error {
	if period == nil {
		return nil
	}

	if !models.StatusPagePeriod(*period).Valid() {
		return ErrInvalidHistoryPeriod
	}

	return nil
}

// Service provides business logic for status page management.
type Service struct {
	db  db.Service
	cfg *config.Config
}

// NewService creates a new status pages service. cfg may be nil (e.g. the MCP
// handler doesn't have an app config to hand) — the uptime-bar query's safety
// cap then falls back to the documented retention defaults instead of the
// org's actual configured values.
func NewService(dbService db.Service, cfg *config.Config) *Service {
	return &Service{db: dbService, cfg: cfg}
}

// retentionHints returns the org's configured raw/hour retention (hours of
// raw kept / days of hourly rollups kept), or (0, 0) when cfg is nil — which
// uptimebar.BucketAvailability treats as "use the documented defaults" when
// sizing its safety cap.
func (s *Service) retentionHints() (int, int) {
	if s.cfg == nil {
		return 0, 0
	}

	return s.cfg.Aggregation.RetentionRaw, s.cfg.Aggregation.RetentionHour
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
	HistoryPeriod    string                       `json:"historyPeriod"`
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
	OverallAvailabilityPct *float64 `json:"overallAvailabilityPct,omitempty"`
	// DailyAvailability holds the per-bucket availability points. The JSON key is
	// kept for back-compat even when BucketUnit is "hour" (hourly buckets); read
	// BucketUnit/Period to label the axis correctly.
	DailyAvailability []AvailabilityPoint `json:"dailyAvailability,omitempty"`
	ResponseTimeData  []ResponseTimePoint `json:"responseTimeData,omitempty"`
	// Period is the active history period ("24h"|"7d"|"30d"|"90d").
	Period string `json:"period,omitempty"`
	// BucketUnit is the granularity of each point: "day" or "hour".
	BucketUnit string `json:"bucketUnit,omitempty"`
}

// AvailabilityPoint represents availability data for a single bucket (a day in
// daily mode, an hour in 24h mode). Date is the UTC calendar date (kept for
// back-compat); Time is the bucket start as RFC3339 and is the authoritative
// anchor for hourly buckets.
type AvailabilityPoint struct {
	Date            string  `json:"date"`
	Time            string  `json:"time,omitempty"`
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
	HistoryPeriod    *string `json:"historyPeriod,omitempty"`
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
	HistoryPeriod    *string `json:"historyPeriod,omitempty"`
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

	// History window: the period enum is the source of truth. Accept the legacy
	// historyDays too (mapped to the enum) for one release. When the enum is set,
	// keep history_days roughly in sync for back-compat (24h → 1).
	if req.HistoryPeriod != nil {
		page.HistoryPeriod = *req.HistoryPeriod
		page.HistoryDays = daysForPeriod(models.StatusPagePeriod(*req.HistoryPeriod))
	} else if req.HistoryDays != nil {
		page.HistoryDays = *req.HistoryDays
		page.HistoryPeriod = string(models.PeriodFromDays(*req.HistoryDays))
	}

	if req.Language != nil {
		page.Language = req.Language
	}
}

// daysForPeriod returns a back-compat history_days count for a period enum.
// 24h has no day equivalent, so it maps to 1 (the smallest sane value) purely
// to keep the deprecated column populated; bucketing always uses the enum.
func daysForPeriod(period models.StatusPagePeriod) int {
	switch period {
	case models.StatusPagePeriod24h:
		return 1
	case models.StatusPagePeriod7d:
		return 7
	case models.StatusPagePeriod30d:
		return 30
	case models.StatusPagePeriod90d:
		return 90
	default:
		return 90
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

	if errPeriod := validateHistoryPeriod(req.HistoryPeriod); errPeriod != nil {
		return StatusPageResponse{}, errPeriod
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

	if errPeriod := validateHistoryPeriod(req.HistoryPeriod); errPeriod != nil {
		return StatusPageResponse{}, errPeriod
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
		HistoryPeriod:    req.HistoryPeriod,
		Language:         req.Language,
	}

	// The period enum is the source of truth; keep history_days in sync for
	// back-compat when only the enum is sent. When the legacy historyDays is
	// sent without an enum, derive the enum so the column stays consistent.
	if req.HistoryPeriod != nil {
		days := daysForPeriod(models.StatusPagePeriod(*req.HistoryPeriod))
		update.HistoryDays = &days
	} else if req.HistoryDays != nil {
		derived := string(models.PeriodFromDays(*req.HistoryDays))
		update.HistoryPeriod = &derived
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

// statusPagePeriodInfo returns the bucket period type, the number of buckets,
// and the bucket duration for a status-page history period. It mirrors the
// badges endpoint's uptimeBarPeriodInfo so the two surfaces bucket identically:
// 24h → (hour, 24, 1h); 7d/30d/90d → (day, N, 24h). An unknown value defaults
// to 90 daily buckets.
func statusPagePeriodInfo(period models.StatusPagePeriod) (string, int, time.Duration) {
	switch period {
	case models.StatusPagePeriod24h:
		return models.PeriodTypeHour, 24, time.Hour
	case models.StatusPagePeriod7d:
		return models.PeriodTypeDay, 7, 24 * time.Hour
	case models.StatusPagePeriod30d:
		return models.PeriodTypeDay, 30, 24 * time.Hour
	case models.StatusPagePeriod90d:
		return models.PeriodTypeDay, 90, 24 * time.Hour
	default:
		return models.PeriodTypeDay, 90, 24 * time.Hour
	}
}

// pagePeriod resolves a page's effective history period. It prefers the
// history_period enum; for legacy rows whose column is empty or invalid it
// falls back to mapping history_days.
func pagePeriod(page *models.StatusPage) models.StatusPagePeriod {
	p := models.StatusPagePeriod(page.HistoryPeriod)
	if p.Valid() {
		return p
	}

	return models.PeriodFromDays(page.HistoryDays)
}

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

	// 24h renders 24 hourly buckets — a separate code path from the daily mode,
	// mirroring the badges period=24h hourly bucketing.
	if pagePeriod(page).IsHourly() {
		s.enrichHourly(ctx, orgUID, page, sections, checkUIDs)

		return
	}

	// Bucket daily availability from the shared raw+hour+day union, exactly like
	// the badge: every day with any row (raw OR rollup) is filled, so a recent day
	// whose raw rows haven't been rolled up yet no longer reads "No data".
	// historyStart matches the loop in buildAvailabilityData: the oldest of the
	// HistoryDays buckets is (HistoryDays-1) days before today (UTC midnight).
	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	historyStart := todayStart.AddDate(0, 0, -(page.HistoryDays - 1))

	retentionRawHours, retentionHourDays := s.retentionHints()

	bucketsByCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, checkUIDs, 24*time.Hour, historyStart, page.HistoryDays,
		retentionRawHours, retentionHourDays,
	)
	if err != nil {
		return
	}

	recentByCheck := s.fetchRecentResults(ctx, orgUID, checkUIDs, page.ShowResponseTime)

	// Build availability data for each resource
	period := string(pagePeriod(page))

	for i := range sections {
		for j := range sections[i].Resources {
			checkUID := sections[i].Resources[j].CheckUID
			availData := buildAvailabilityData(
				bucketsByCheck[checkUID], recentByCheck[checkUID],
				todayStart, page.HistoryDays, page.ShowAvailability, page.ShowResponseTime,
			)
			availData.Period = period
			availData.BucketUnit = models.PeriodTypeDay
			sections[i].Resources[j].Availability = availData
		}
	}
}

// fetchRecentResults loads the last responseTimeLimit results per check (any
// period type) for the response-time chart. Returns an empty map when the
// response-time chart is disabled. This is unchanged from the previous behavior
// and is kept separate from the availability bucketing (Q4: the response-time
// path is not affected by the data-source unification).
func (s *Service) fetchRecentResults(
	ctx context.Context, orgUID string, checkUIDs []string, showResponseTime bool,
) map[string][]*models.Result {
	recentByCheck := make(map[string][]*models.Result)

	if !showResponseTime {
		return recentByCheck
	}

	const responseTimeLimit = 100

	recentFilter := &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		Limit:           responseTimeLimit * len(checkUIDs),
	}

	recentResp, err := s.db.ListResults(ctx, recentFilter)
	if err != nil || recentResp == nil {
		return recentByCheck
	}

	for _, r := range recentResp.Results {
		if len(recentByCheck[r.CheckUID]) < responseTimeLimit {
			recentByCheck[r.CheckUID] = append(recentByCheck[r.CheckUID], r)
		}
	}

	return recentByCheck
}

// hourlyBucketCount is the number of hourly buckets the 24h view renders.
const hourlyBucketCount = 24

// enrichHourly populates each resource's availability with 24 hourly buckets for
// the 24h period, bucketed from the shared raw+hour+day union (uptimebar) exactly
// like the badges period=24h path. A past hour with raw-only data (the raw→hour
// rollup lags by RetentionRaw) is filled from raw rather than reading "No data".
func (s *Service) enrichHourly(
	ctx context.Context, orgUID string, page *models.StatusPage,
	sections []StatusPageSectionResponse, checkUIDs []string,
) {
	now := time.Now().UTC()
	// The newest of the 24 buckets is the current, in-progress hour; the oldest is
	// 23 hours earlier. -(n-1) keeps the current hour inside the window.
	bucketStart := now.Truncate(time.Hour).Add(-time.Duration(hourlyBucketCount-1) * time.Hour)

	retentionRawHours, retentionHourDays := s.retentionHints()

	bucketsByCheck, err := uptimebar.BucketAvailability(
		ctx, s.db, orgUID, checkUIDs, time.Hour, bucketStart, hourlyBucketCount,
		retentionRawHours, retentionHourDays,
	)
	if err != nil {
		return
	}

	recentByCheck := s.fetchRecentResults(ctx, orgUID, checkUIDs, page.ShowResponseTime)

	for i := range sections {
		for j := range sections[i].Resources {
			checkUID := sections[i].Resources[j].CheckUID
			availData := buildHourlyAvailabilityData(
				bucketsByCheck[checkUID], recentByCheck[checkUID], bucketStart,
				page.ShowAvailability, page.ShowResponseTime,
			)
			availData.Period = string(models.StatusPagePeriod24h)
			availData.BucketUnit = models.PeriodTypeHour
			sections[i].Resources[j].Availability = availData
		}
	}
}

// buildHourlyAvailabilityData builds the 24 hourly availability points anchored
// on bucketStart (oldest → newest, newest = current hour) from per-bucket stats.
// Buckets absent from byBucket (no rows) get status "noData". The overall
// weighted average matches the daily path.
func buildHourlyAvailabilityData(
	byBucket map[time.Time]uptimebar.BucketStats, recentResults []*models.Result, bucketStart time.Time,
	showAvailability, showResponseTime bool,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	if showAvailability {
		points := make([]AvailabilityPoint, 0, hourlyBucketCount)

		var (
			totalWeightedAvail float64
			totalChecksSum     int
		)

		for i := range hourlyBucketCount {
			bucket := bucketStart.Add(time.Duration(i) * time.Hour)

			point := AvailabilityPoint{
				Date:   bucket.Format("2006-01-02"),
				Time:   bucket.Format(time.RFC3339),
				Status: statusNoData,
			}

			if stats, ok := byBucket[bucket]; ok {
				if pct, hasData := stats.AvailabilityPct(); hasData {
					point.AvailabilityPct = pct
					point.Status = availabilityToStatus(pct)

					totalWeightedAvail += pct * float64(stats.Total)
					totalChecksSum += stats.Total
				}
			}

			points = append(points, point)
		}

		data.DailyAvailability = points

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

// buildAvailabilityData builds the daily availability points over the history
// window (oldest → newest, newest = today) from per-bucket stats keyed by the
// day's UTC-midnight start. Buckets absent from byBucket (no rows) get status
// "noData". todayStart is the UTC-midnight of the current day; the window's
// oldest bucket is (historyDays-1) days earlier, matching the BucketAvailability
// call in enrichWithAvailability.
func buildAvailabilityData(
	byBucket map[time.Time]uptimebar.BucketStats, recentResults []*models.Result,
	todayStart time.Time, historyDays int, showAvailability, showResponseTime bool,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	if showAvailability {
		daily := make([]AvailabilityPoint, 0, historyDays)

		var totalWeightedAvail float64

		var totalChecksSum int

		for dayOffset := historyDays - 1; dayOffset >= 0; dayOffset-- {
			day := todayStart.AddDate(0, 0, -dayOffset)
			dateStr := day.Format("2006-01-02")

			point := AvailabilityPoint{
				Date:   dateStr,
				Time:   day.Format(time.RFC3339),
				Status: statusNoData,
			}

			if stats, ok := byBucket[day]; ok {
				if pct, hasData := stats.AvailabilityPct(); hasData {
					point.AvailabilityPct = pct
					point.Status = availabilityToStatus(pct)

					totalWeightedAvail += pct * float64(stats.Total)
					totalChecksSum += stats.Total
				}
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

// statusNoData is the availability-point status for a bucket that has no rows in
// the shared raw+hour+day union — the front end renders it gray.
const statusNoData = "noData"

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
		HistoryPeriod:    string(pagePeriod(page)),
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
