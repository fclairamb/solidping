// Package statuspages provides HTTP handlers for status page management endpoints.
package statuspages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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

// StatusPageResponse represents a status page in API responses.
type StatusPageResponse struct {
	UID              string                      `json:"uid"`
	Name             string                      `json:"name"`
	Slug             string                      `json:"slug"`
	Description      *string                     `json:"description,omitempty"`
	Visibility       string                      `json:"visibility"`
	IsDefault        bool                        `json:"isDefault"`
	Enabled          bool                        `json:"enabled"`
	ShowAvailability bool                        `json:"showAvailability"`
	ShowResponseTime bool                        `json:"showResponseTime"`
	HistoryDays      int                         `json:"historyDays"`
	Language         *string                     `json:"language,omitempty"`
	Sections         []StatusPageSectionResponse `json:"sections,omitempty"`
	CreatedAt        *time.Time                  `json:"createdAt,omitempty"`
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

	position := 0
	if req.Position != nil {
		position = *req.Position
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

	position := 0
	if req.Position != nil {
		position = *req.Position
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

//nolint:gocognit,nestif,cyclop,funlen // Availability enrichment has inherent conditional complexity
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

	// Fetch hourly results for today to synthesize current day's availability
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	hourlyFilter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{"hour"},
		PeriodStartAfter: &todayStart,
		Limit:            24 * len(checkUIDs),
	}

	hourlyResp, err := s.db.ListResults(ctx, hourlyFilter)
	if err == nil && hourlyResp != nil {
		// Synthesize today's daily result from hourly data for each check
		hourlyByCheck := make(map[string][]*models.Result)
		for _, r := range hourlyResp.Results {
			hourlyByCheck[r.CheckUID] = append(hourlyByCheck[r.CheckUID], r)
		}

		for _, checkUID := range checkUIDs {
			// Skip if we already have a daily result for today
			hasDailyToday := false
			todayStr := todayStart.Format("2006-01-02")
			for _, r := range resultsByCheck[checkUID] {
				if r.PeriodStart.Format("2006-01-02") == todayStr {
					hasDailyToday = true

					break
				}
			}

			if hasDailyToday {
				continue
			}

			// Aggregate hourly results for this check
			var totalAvail float64

			var count int

			var totalDuration, totalDurationP95 float64

			for _, hourlyResult := range hourlyByCheck[checkUID] {
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

			if count > 0 {
				avgAvail := totalAvail / float64(count)
				avgDuration := float32(totalDuration / float64(count))
				avgP95 := float32(totalDurationP95 / float64(count))
				synthResult := &models.Result{
					CheckUID:        checkUID,
					PeriodStart:     todayStart,
					AvailabilityPct: &avgAvail,
					Duration:        &avgDuration,
					DurationP95:     &avgP95,
					TotalChecks:     &count,
				}
				resultsByCheck[checkUID] = append(resultsByCheck[checkUID], synthResult)
			}
		}
	}

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

func buildAvailabilityData(
	dailyResults, recentResults []*models.Result, historyDays int, showAvailability, showResponseTime bool,
) *ResourceAvailabilityData {
	data := &ResourceAvailabilityData{}

	// Index daily results by date string
	resultsByDate := make(map[string]*models.Result)
	for _, result := range dailyResults {
		dateStr := result.PeriodStart.Format("2006-01-02")
		resultsByDate[dateStr] = result
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

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
		// Use the last 100 results (any type: raw, hour, day) for the response time chart
		rtData := make([]ResponseTimePoint, 0, len(recentResults))

		for _, recentResult := range recentResults {
			// For raw results, use Duration; for aggregated results, use DurationP95
			var duration *float32
			if recentResult.DurationP95 != nil {
				duration = recentResult.DurationP95
			} else if recentResult.Duration != nil {
				duration = recentResult.Duration
			}

			point := ResponseTimePoint{
				Time:        recentResult.PeriodStart.UTC().Format(time.RFC3339),
				DurationP95: duration,
			}
			rtData = append(rtData, point)
		}

		// Results come in DESC order, reverse for chronological display
		for i, j := 0, len(rtData)-1; i < j; i, j = i+1, j-1 {
			rtData[i], rtData[j] = rtData[j], rtData[i]
		}

		data.ResponseTimeData = rtData
	}

	return data
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
	case models.CheckStatusDegraded:
		statusStr = "degraded"
	}

	return &ResourceCheckInfo{
		Name:   check.Name,
		Type:   check.Type,
		Status: statusStr,
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
