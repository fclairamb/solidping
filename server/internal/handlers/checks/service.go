package checks

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// ValidateCheckRequest is the request body for the validate check endpoint.
type ValidateCheckRequest struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// ValidateCheckResponse is the response body for the validate check endpoint.
type ValidateCheckResponse struct {
	Valid  bool                        `json:"valid"`
	Fields []base.ValidationErrorField `json:"fields,omitempty"`
}

// slugRegex validates slug format: lowercase letter, then 2-19 lowercase letters/digits/hyphens.
// Total length: 3-20 characters.
var slugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{2,19}$`)

// slugInvalidCharsRegex matches characters that are not valid in a slug.
var slugInvalidCharsRegex = regexp.MustCompile(`[^a-z0-9-]`)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrInvalidCheckType is returned when an unsupported check type is provided.
	ErrInvalidCheckType = errors.New("invalid check type")
	// ErrSlugConflict is returned when a slug already exists.
	ErrSlugConflict = errors.New("slug already exists")
	// ErrSlugGenerationFailed is returned when a unique slug cannot be generated.
	ErrSlugGenerationFailed = errors.New("could not generate unique slug after 99 attempts")
	// ErrInvalidSlugFormat is returned when a slug has an invalid format (e.g., looks like a UUID).
	ErrInvalidSlugFormat = errors.New("invalid slug format")
	// ErrCheckHasActiveIncidents is returned when trying to delete a check with active incidents.
	ErrCheckHasActiveIncidents = errors.New("check has active incidents")
	// ErrInvalidCursor is returned when the cursor parameter is malformed.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrUnsupportedExportVersion is returned when the export document has an unsupported version.
	ErrUnsupportedExportVersion = errors.New("unsupported export version")
	// ErrEmptyChecksArray is returned when the import document has no checks.
	ErrEmptyChecksArray = errors.New("checks array must not be empty")
)

// isUUID checks if a string is a valid UUID.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// validateSlug validates that a slug has a valid format.
// Valid slugs: start with lowercase letter, followed by 2-19 lowercase letters, digits, or hyphens.
// Total length: 3-20 characters. Must not look like a UUID.
func validateSlug(slug string) error {
	if slug == "" {
		return nil // Empty slug is allowed (will be auto-generated)
	}

	if isUUID(slug) {
		return ErrInvalidSlugFormat
	}

	if !slugRegex.MatchString(slug) {
		return ErrInvalidSlugFormat
	}

	return nil
}

// sanitizeSlug cleans a slug by removing invalid characters, ensuring it starts with a letter,
// and truncating to the max slug length (20 chars). This is used for auto-generated slugs.
func sanitizeSlug(slug string) string {
	const maxSlugLen = 20

	// Convert to lowercase
	slug = strings.ToLower(slug)

	// Replace invalid characters with hyphens
	slug = slugInvalidCharsRegex.ReplaceAllString(slug, "-")

	// Remove consecutive hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}

	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")

	// Ensure slug starts with a letter (prepend 'x' if it starts with a digit or is empty)
	if len(slug) == 0 || (slug[0] >= '0' && slug[0] <= '9') {
		slug = "x" + slug
	}

	// Truncate to max length and trim any trailing hyphen from truncation
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}

	return slug
}

// validateAndCheckSlugConflict validates a slug and checks for conflicts in the database.
func (s *Service) validateAndCheckSlugConflict(
	ctx context.Context, orgUID, newSlug string, currentSlug *string,
) error {
	if errValidate := validateSlug(newSlug); errValidate != nil {
		return errValidate
	}

	// Check for slug conflict (only if slug is different from current)
	if currentSlug != nil && *currentSlug == newSlug {
		return nil // Same slug, no conflict
	}

	existingCheck, errCheck := s.db.GetCheckByUidOrSlug(ctx, orgUID, newSlug)
	if errCheck != nil && !errors.Is(errCheck, sql.ErrNoRows) {
		return errCheck
	}
	if existingCheck != nil {
		return ErrSlugConflict
	}

	return nil
}

// Service provides business logic for check management.
type Service struct {
	db            db.Service
	eventNotifier notifier.EventNotifier
	regions       *regions.Service
}

// NewService creates a new checks service.
func NewService(dbService db.Service, eventNotifier notifier.EventNotifier) *Service {
	return &Service{
		db:            dbService,
		eventNotifier: eventNotifier,
		regions:       regions.NewService(dbService),
	}
}

// ValidateCheck validates a check configuration without persisting it.
func (s *Service) ValidateCheck(
	_ context.Context, req ValidateCheckRequest,
) (ValidateCheckResponse, error) {
	checkType := checkerdef.CheckType(req.Type)

	checker, ok := registry.GetChecker(checkType)
	if !ok {
		return ValidateCheckResponse{
			Valid: false,
			Fields: []base.ValidationErrorField{
				{Name: "type", Message: "unsupported check type"},
			},
		}, nil
	}

	spec := &checkerdef.CheckSpec{
		Config: req.Config,
	}

	err := checker.Validate(spec)
	if err == nil {
		return ValidateCheckResponse{Valid: true}, nil
	}

	configErr := checkerdef.IsConfigError(err)
	if configErr == nil {
		return ValidateCheckResponse{}, err
	}

	return ValidateCheckResponse{
		Valid: false,
		Fields: []base.ValidationErrorField{
			{
				Name:    configErr.Parameter,
				Message: configErr.Message,
			},
		},
	}, nil
}

// CheckResponse represents a check in API responses.
type CheckResponse struct {
	UID              string                    `json:"uid"`
	Name             *string                   `json:"name,omitempty"`
	Slug             *string                   `json:"slug,omitempty"`
	Description      *string                   `json:"description,omitempty"`
	CheckGroupUID    *string                   `json:"checkGroupUid,omitempty"`
	Type             *string                   `json:"type,omitempty"`
	Config           map[string]any            `json:"config,omitempty"`
	Regions          []string                  `json:"regions,omitempty"`
	Enabled          *bool                     `json:"enabled,omitempty"`
	Internal         *bool                     `json:"internal,omitempty"`
	Period           *string                   `json:"period,omitempty"`
	Labels           map[string]string         `json:"labels,omitempty"`
	LastResult       *LastResultResponse       `json:"lastResult,omitempty"`
	LastStatusChange *LastStatusChangeResponse `json:"lastStatusChange,omitempty"`
	CreatedAt        *time.Time                `json:"createdAt,omitempty"`

	// Adaptive resolution settings
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	MaxAdaptiveIncrease      *int `json:"maxAdaptiveIncrease,omitempty"`
}

// CheckStatus represents the current status of a check.
type CheckStatus struct {
	LastResult  *string    `json:"lastResult,omitempty"`
	LastChecked *time.Time `json:"lastChecked,omitempty"`
	Uptime24h   *float64   `json:"uptime24h,omitempty"`
}

// LastResultResponse represents the last execution result for a check.
type LastResultResponse struct {
	UID        string         `json:"uid"`
	Status     string         `json:"status"`
	Timestamp  time.Time      `json:"timestamp"`
	DurationMs *float32       `json:"durationMs,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// LastStatusChangeResponse represents when and to what status a check last changed.
type LastStatusChangeResponse struct {
	Time   time.Time `json:"time"`
	Status string    `json:"status"`
}

// ListChecksOptions contains options for listing checks.
type ListChecksOptions struct {
	IncludeLastResult       bool
	IncludeLastStatusChange bool
	Labels                  map[string]string
	CheckGroupUID           *string
	Query                   string
	Internal                *string
	Cursor                  string
	Limit                   int
}

// PaginationResponse contains pagination metadata.
type PaginationResponse struct {
	Total  int64  `json:"total"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

// ListChecksResponse is the response for listing checks.
type ListChecksResponse struct {
	Data       []CheckResponse    `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// GetCheckOptions contains optional parameters for getting a single check.
type GetCheckOptions struct {
	IncludeLastResult       bool
	IncludeLastStatusChange bool
}

// ListChecks retrieves checks for an organization with pagination and filtering.
//
//nolint:cyclop,gocognit,funlen // Complex due to optional field handling and label filtering
func (s *Service) ListChecks(ctx context.Context, orgSlug string, opts ListChecksOptions) (*ListChecksResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Build filter
	filter := &models.ListChecksFilter{
		Labels:        opts.Labels,
		CheckGroupUID: opts.CheckGroupUID,
		Query:         opts.Query,
		Internal:      opts.Internal,
		Limit:         opts.Limit,
	}

	// Parse cursor
	if opts.Cursor != "" {
		ts, uid, errCursor := s.decodeCursor(opts.Cursor)
		if errCursor != nil {
			return nil, ErrInvalidCursor
		}
		filter.CursorCreatedAt = &ts
		filter.CursorUID = &uid
	}

	// Get checks for the organization
	checks, total, err := s.db.ListChecks(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	// Detect "has more" and trim
	hasMore := filter.Limit > 0 && len(checks) > filter.Limit
	if hasMore {
		checks = checks[:filter.Limit]
	}

	// Convert to response format
	responses := make([]CheckResponse, len(checks))
	for i, check := range checks {
		responses[i] = s.convertCheckToResponse(check)
	}

	// Fetch labels for all checks
	if len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		labelsMap, err := s.db.GetLabelsForChecks(ctx, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach labels to responses
		for i := range responses {
			if labels, ok := labelsMap[checks[i].UID]; ok && len(labels) > 0 {
				labelMap := make(map[string]string)
				for _, label := range labels {
					labelMap[label.Key] = label.Value
				}
				responses[i].Labels = labelMap
			}
		}
	}

	// If last result is requested, fetch them in a single query
	if opts.IncludeLastResult && len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		lastResults, err := s.db.GetLastResultForChecks(ctx, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach last results to responses
		for i := range responses {
			if result, ok := lastResults[checks[i].UID]; ok {
				responses[i].LastResult = s.convertResultToLastResultResponse(result)
			}
		}
	}

	// If last status change is requested, fetch them in a single query
	if opts.IncludeLastStatusChange && len(checks) > 0 {
		checkUIDs := make([]string, len(checks))
		for i, check := range checks {
			checkUIDs[i] = check.UID
		}

		lastStatusChanges, err := s.db.GetLastStatusChangeForChecks(ctx, checkUIDs)
		if err != nil {
			return nil, err
		}

		// Attach last status change data to responses
		for i := range responses {
			if change, ok := lastStatusChanges[checks[i].UID]; ok {
				responses[i].LastStatusChange = &LastStatusChangeResponse{
					Time:   change.Time,
					Status: change.Status,
				}
			}
		}
	}

	// Build next cursor
	var nextCursor string
	if hasMore && len(checks) > 0 {
		lastCheck := checks[len(checks)-1]
		nextCursor = s.encodeCursor(lastCheck.CreatedAt, lastCheck.UID)
	}

	limit := opts.Limit
	if limit == 0 {
		limit = len(responses)
	}

	return &ListChecksResponse{
		Data: responses,
		Pagination: PaginationResponse{
			Total:  total,
			Cursor: nextCursor,
			Limit:  limit,
		},
	}, nil
}

func (s *Service) encodeCursor(createdAt time.Time, uid string) string {
	cursorStr := fmt.Sprintf("%s|%s", createdAt.Format(time.RFC3339Nano), uid)
	return base64.URLEncoding.EncodeToString([]byte(cursorStr))
}

func (s *Service) decodeCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return time.Time{}, "", ErrInvalidCursor
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}

	return ts, parts[1], nil
}

// CreateCheckRequest represents a request to create a new check.
type CreateCheckRequest struct {
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Description   string            `json:"description"`
	CheckGroupUID *string           `json:"checkGroupUid"`
	Type          string            `json:"type"`
	Config        map[string]any    `json:"config"`
	Regions       []string          `json:"regions"`
	Enabled       *bool             `json:"enabled"`
	Internal      *bool             `json:"internal,omitempty"`
	Period        *string           `json:"period"`
	Labels        map[string]string `json:"labels"`

	// Adaptive resolution settings
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	MaxAdaptiveIncrease      *int `json:"maxAdaptiveIncrease,omitempty"`
}

// CreateCheck creates a new check for an organization.
//
//nolint:cyclop,funlen,gocritic,gocognit // Complex due to validation and field handling
func (s *Service) CreateCheck(ctx context.Context, orgSlug string, req CreateCheckRequest) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Get the checker to validate the configuration
	checker, ok := registry.GetChecker(checkerdef.CheckType(req.Type))
	if !ok {
		return CheckResponse{}, ErrInvalidCheckType
	}

	// Parse the period if provided
	var period time.Duration
	if req.Period != nil && *req.Period != "" {
		var duration timeutils.Duration
		if scanErr := duration.Scan(*req.Period); scanErr != nil {
			return CheckResponse{}, scanErr
		}
		period = time.Duration(duration)
	}

	// Track if slug was user-provided
	userProvidedSlug := req.Slug != ""

	// Validate slug format if provided by user
	if userProvidedSlug {
		if slugErr := validateSlug(req.Slug); slugErr != nil {
			return CheckResponse{}, slugErr
		}
	}

	// Create CheckSpec for validation
	spec := &checkerdef.CheckSpec{
		Name:   req.Name,
		Slug:   req.Slug,
		Period: period,
		Config: req.Config,
	}

	// Validate the spec - this may modify Name and Slug
	if err := checker.Validate(spec); err != nil { //nolint:govet // Intentional shadowing for scoped error
		return CheckResponse{}, err
	}

	// If slug was not user-provided, generate a slug based on the checker type and detected slug
	if !userProvidedSlug {
		spec.Slug = string(checker.Type()) + "-" + spec.Slug
	}

	// Handle slug conflicts
	finalSlug, err := s.ensureUniqueSlug(ctx, org.UID, spec.Slug, userProvidedSlug)
	if err != nil {
		return CheckResponse{}, err
	}

	// Create check with unique slug
	check := models.NewCheck(org.UID, finalSlug, req.Type)

	// Set check group
	if req.CheckGroupUID != nil && *req.CheckGroupUID != "" {
		check.CheckGroupUID = req.CheckGroupUID
	}

	// Set name from validated spec
	if spec.Name != "" {
		check.Name = &spec.Name
	}

	// Set description
	if req.Description != "" {
		check.Description = &req.Description
	}

	// Set config
	if req.Config != nil {
		check.Config = req.Config
	}

	// Set enabled (default is true from NewCheck)
	if req.Enabled != nil {
		check.Enabled = *req.Enabled
	}

	// Set internal flag
	if req.Internal != nil {
		check.Internal = *req.Internal
	}

	// Set period (default is 1 minute from NewCheck)
	if req.Period != nil && *req.Period != "" {
		var duration timeutils.Duration
		if err := duration.Scan(*req.Period); err != nil { //nolint:govet
			return CheckResponse{}, err
		}
		check.Period = duration
	}

	// Resolve regions for the check
	resolvedRegions, err := s.regions.ResolveRegionsForCheck(ctx, req.Regions, org.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", err)
	}
	check.Regions = resolvedRegions

	// Set adaptive resolution settings
	check.ReopenCooldownMultiplier = req.ReopenCooldownMultiplier
	check.MaxAdaptiveIncrease = req.MaxAdaptiveIncrease

	// Create check in DB
	if err := s.db.CreateCheck(ctx, check); err != nil { //nolint:govet
		return CheckResponse{}, err
	}

	// Handle labels if provided
	if len(req.Labels) > 0 {
		labelUIDs := make([]string, 0, len(req.Labels))
		for key, value := range req.Labels {
			label, err := s.db.GetOrCreateLabel(ctx, org.UID, key, value) //nolint:govet
			if err != nil {
				return CheckResponse{}, fmt.Errorf("failed to create label: %w", err)
			}
			labelUIDs = append(labelUIDs, label.UID)
		}
		//nolint:govet // Intentional shadowing for scoped error
		if err := s.db.SetCheckLabels(ctx, check.UID, labelUIDs); err != nil {
			return CheckResponse{}, fmt.Errorf("failed to set check labels: %w", err)
		}
	}

	// Auto-attach default connections
	defaultConnections, err := s.db.ListDefaultConnections(ctx, org.UID)
	if err == nil {
		for _, conn := range defaultConnections {
			checkConn := models.NewCheckConnection(check.UID, conn.UID, org.UID)
			_ = s.db.CreateCheckConnection(ctx, checkConn)
		}
	}

	// Emit check.created event
	if err := s.emitEvent(ctx, org.UID, models.EventTypeCheckCreated, check); err != nil {
		slog.WarnContext(ctx, "failed to emit check.created event", "error", err)
		// Don't fail check creation for event emission errors
	}

	// Fetch the check with labels for response
	response := s.convertCheckToResponse(check)
	if len(req.Labels) > 0 {
		response.Labels = req.Labels
	}

	return response, nil
}

// GetCheck retrieves a single check by UID or slug.
func (s *Service) GetCheck(
	ctx context.Context, orgSlug, identifier string, opts GetCheckOptions,
) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	// Convert to response
	response := s.convertCheckToResponse(check)

	// Fetch and attach labels
	labels, err := s.db.GetLabelsForCheck(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to get labels: %w", err)
	}
	if len(labels) > 0 {
		labelMap := make(map[string]string)
		for _, label := range labels {
			labelMap[label.Key] = label.Value
		}
		response.Labels = labelMap
	}

	// Fetch last result if requested
	if opts.IncludeLastResult {
		lastResults, err := s.db.GetLastResultForChecks(ctx, []string{check.UID})
		if err != nil {
			return CheckResponse{}, fmt.Errorf("failed to get last result: %w", err)
		}
		if result, ok := lastResults[check.UID]; ok {
			response.LastResult = s.convertResultToLastResultResponse(result)
		}
	}

	// Fetch last status change if requested
	if opts.IncludeLastStatusChange {
		lastStatusChanges, err := s.db.GetLastStatusChangeForChecks(ctx, []string{check.UID})
		if err != nil {
			return CheckResponse{}, fmt.Errorf("failed to get last status change: %w", err)
		}
		if change, ok := lastStatusChanges[check.UID]; ok {
			response.LastStatusChange = &LastStatusChangeResponse{
				Time:   change.Time,
				Status: change.Status,
			}
		}
	}

	return response, nil
}

// UpdateCheckRequest represents a request to update a check.
type UpdateCheckRequest struct {
	Name          *string            `json:"name,omitempty"`
	Slug          *string            `json:"slug,omitempty"`
	Description   *string            `json:"description,omitempty"`
	CheckGroupUID *string            `json:"checkGroupUid"`
	Config        *map[string]any    `json:"config,omitempty"`
	Regions       *[]string          `json:"regions,omitempty"`
	Enabled       *bool              `json:"enabled,omitempty"`
	Internal      *bool              `json:"internal,omitempty"`
	Period        *string            `json:"period,omitempty"`
	Labels        *map[string]string `json:"labels,omitempty"`

	// Adaptive resolution settings
	ReopenCooldownMultiplier *int `json:"reopenCooldownMultiplier,omitempty"`
	MaxAdaptiveIncrease      *int `json:"maxAdaptiveIncrease,omitempty"`
}

// UpsertCheckRequest represents a request to create or update a check by slug.
type UpsertCheckRequest struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	CheckGroupUID *string           `json:"checkGroupUid"`
	Type          string            `json:"type"`
	Config        map[string]any    `json:"config"`
	Enabled       *bool             `json:"enabled"`
	Internal      *bool             `json:"internal,omitempty"`
	Period        *string           `json:"period"`
	Labels        map[string]string `json:"labels"`
}

// UpdateCheck updates an existing check by UID or slug.
//
//nolint:cyclop,funlen,gocognit // Complex due to multiple validation and field update paths
func (s *Service) UpdateCheck(
	ctx context.Context, orgSlug, identifier string, req *UpdateCheckRequest,
) (CheckResponse, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return CheckResponse{}, ErrCheckNotFound
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" {
		if errSlug := s.validateAndCheckSlugConflict(ctx, org.UID, *req.Slug, check.Slug); errSlug != nil {
			return CheckResponse{}, errSlug
		}
	}

	// Build update object
	update := models.CheckUpdate{}
	if req.CheckGroupUID != nil {
		update.CheckGroupUID = req.CheckGroupUID
	}
	if req.Name != nil {
		update.Name = req.Name
	}
	if req.Slug != nil {
		update.Slug = req.Slug
	}
	if req.Description != nil {
		update.Description = req.Description
	}
	if req.Config != nil {
		configMap := models.JSONMap(*req.Config)
		update.Config = &configMap
	}
	if req.Enabled != nil {
		update.Enabled = req.Enabled
	}
	if req.Internal != nil {
		update.Internal = req.Internal
	}
	if req.Period != nil {
		var duration timeutils.Duration
		if errScan := duration.Scan(*req.Period); errScan != nil {
			return CheckResponse{}, errScan
		}
		update.Period = &duration
	}
	if req.Regions != nil {
		resolvedRegions, regErr := s.regions.ResolveRegionsForCheck(ctx, *req.Regions, org.UID)
		if regErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to resolve regions: %w", regErr)
		}
		update.Regions = &resolvedRegions
	}
	if req.ReopenCooldownMultiplier != nil {
		update.ReopenCooldownMultiplier = req.ReopenCooldownMultiplier
	}
	if req.MaxAdaptiveIncrease != nil {
		update.MaxAdaptiveIncrease = req.MaxAdaptiveIncrease
	}

	// Update check in DB
	if errUpdate := s.db.UpdateCheck(ctx, check.UID, &update); errUpdate != nil {
		return CheckResponse{}, errUpdate
	}

	// Reconcile check jobs if regions, period, enabled, or config changed
	if req.Regions != nil || req.Period != nil || req.Enabled != nil || req.Config != nil {
		updatedCheck, fetchErr := s.db.GetCheck(ctx, org.UID, check.UID)
		if fetchErr != nil {
			return CheckResponse{}, fetchErr
		}

		if reconcileErr := s.reconcileCheckJobs(ctx, updatedCheck); reconcileErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to reconcile check jobs: %w", reconcileErr)
		}
	}

	// Handle labels if provided (nil means no change, empty map means clear all)
	if req.Labels != nil {
		labelUIDs := make([]string, 0, len(*req.Labels))
		for key, value := range *req.Labels {
			label, labelErr := s.db.GetOrCreateLabel(ctx, org.UID, key, value)
			if labelErr != nil {
				return CheckResponse{}, fmt.Errorf("failed to create label: %w", labelErr)
			}
			labelUIDs = append(labelUIDs, label.UID)
		}
		if setLabelsErr := s.db.SetCheckLabels(ctx, check.UID, labelUIDs); setLabelsErr != nil {
			return CheckResponse{}, fmt.Errorf("failed to set check labels: %w", setLabelsErr)
		}
	}

	// Fetch updated check
	updatedCheck, err := s.db.GetCheck(ctx, org.UID, check.UID)
	if err != nil {
		return CheckResponse{}, err
	}

	// Convert to response
	response := s.convertCheckToResponse(updatedCheck)

	// Fetch and attach labels
	labels, err := s.db.GetLabelsForCheck(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to get labels: %w", err)
	}
	if len(labels) > 0 {
		labelMap := make(map[string]string)
		for _, label := range labels {
			labelMap[label.Key] = label.Value
		}
		response.Labels = labelMap
	}

	return response, nil
}

// UpsertCheck creates or updates a check by slug (idempotent operation).
func (s *Service) UpsertCheck(
	ctx context.Context, orgSlug, slug string, req *UpsertCheckRequest,
) (CheckResponse, bool, error) {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return CheckResponse{}, false, ErrOrganizationNotFound
	}

	// Try to get existing check by slug
	existingCheck, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return CheckResponse{}, false, fmt.Errorf("failed to query check: %w", err)
	}

	if existingCheck != nil {
		// Check exists - update it
		updateReq := UpdateCheckRequest{
			Name:          &req.Name,
			Description:   &req.Description,
			CheckGroupUID: req.CheckGroupUID,
			Config:        &req.Config,
			Enabled:       req.Enabled,
			Internal:      req.Internal,
			Period:        req.Period,
			Labels:        &req.Labels,
		}
		updatedCheck, updateErr := s.UpdateCheck(ctx, orgSlug, slug, &updateReq)
		return updatedCheck, false, updateErr // false = not created
	}

	// Check doesn't exist - create it
	createReq := CreateCheckRequest{
		Name:          req.Name,
		Slug:          slug,
		Description:   req.Description,
		CheckGroupUID: req.CheckGroupUID,
		Type:          req.Type,
		Config:        req.Config,
		Enabled:       req.Enabled,
		Internal:      req.Internal,
		Period:        req.Period,
		Labels:        req.Labels,
	}
	check, err := s.CreateCheck(ctx, orgSlug, createReq)
	return check, true, err // true = created
}

// DeleteCheck deletes a check by UID or slug (soft delete).
func (s *Service) DeleteCheck(ctx context.Context, orgSlug, identifier string) error {
	// Get organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	// Resolve check by UID or slug (auto-detected)
	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	// Check for active incidents
	count, err := s.db.CountActiveIncidentsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to check active incidents: %w", err)
	}
	if count > 0 {
		return ErrCheckHasActiveIncidents
	}

	// Delete all check jobs for this check
	existingJobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list check jobs: %w", err)
	}
	for _, job := range existingJobs {
		if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
			return fmt.Errorf("failed to delete check job: %w", err)
		}
	}

	// Delete check
	return s.db.DeleteCheck(ctx, check.UID)
}

// ensureUniqueSlug ensures the slug is unique within the organization.
// If userProvided is false and slug exists, it appends a number to make it unique.
// If userProvided is true and slug exists, it returns ErrSlugConflict.
func (s *Service) ensureUniqueSlug(ctx context.Context, orgUID, slug string, userProvided bool) (string, error) {
	const maxSlugLength = 20
	const minSlugLength = 3

	// Sanitize auto-generated slugs to remove invalid characters
	if !userProvided {
		slug = sanitizeSlug(slug)
	}

	// Truncate slug if too long (reserve space for suffix like "-99")
	if len(slug) > maxSlugLength && !userProvided {
		// Auto-generated slug is too long, truncate it
		// Reserve 3 characters for potential "-XX" suffix
		slug = slug[:maxSlugLength-3]
	}

	// Check if slug already exists
	existingCheck, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, slug)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Real error occurred during lookup (not just "not found")
		return "", err
	}

	// Slug exists
	if existingCheck != nil { //nolint:nestif // Slug conflict resolution logic
		if userProvided {
			// User explicitly provided this slug, return conflict error
			return "", ErrSlugConflict
		}

		// Slug was auto-generated, find a unique one by appending numbers
		baseSlug := slug
		for i := 2; i <= 99; i++ { // Try up to 99 (fits in 2 chars)
			suffix := fmt.Sprintf("-%d", i)
			// Ensure base + suffix doesn't exceed max length
			maxBaseLen := maxSlugLength - len(suffix)
			if len(baseSlug) > maxBaseLen {
				baseSlug = baseSlug[:maxBaseLen]
			}

			candidateSlug := baseSlug + suffix

			// Ensure we're above minimum length
			if len(candidateSlug) < minSlugLength {
				continue
			}

			existing, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, candidateSlug)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				// Real error occurred during lookup (not just "not found")
				return "", err
			}
			if existing == nil {
				// Slug is available
				return candidateSlug, nil
			}
		}

		// Couldn't find unique slug after 99 attempts
		return "", ErrSlugGenerationFailed
	}

	return slug, nil
}

// reconcileCheckJobs ensures the check_jobs match the check's current regions and period.
//
//nolint:cyclop,gocognit,nestif,funlen // Reconciliation handles multiple update paths
func (s *Service) reconcileCheckJobs(ctx context.Context, check *models.Check) error {
	existingJobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to list check jobs: %w", err)
	}

	// If check is disabled, delete all jobs
	if !check.Enabled {
		for _, job := range existingJobs {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}

		return nil
	}

	targetRegions := check.Regions
	basePeriod := time.Duration(check.Period)
	n := len(targetRegions)

	// No regions: ensure exactly one job without a region
	if n == 0 {
		// Delete all existing jobs and create one without region
		for _, job := range existingJobs {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}

		now := time.Now()
		job := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
		job.Type = check.Type
		job.Config = check.Config
		job.ScheduledAt = &now

		return s.db.CreateCheckJob(ctx, job)
	}

	// Build map of existing regions
	existingByRegion := make(map[string]*models.CheckJob)
	for _, job := range existingJobs {
		if job.Region != nil {
			existingByRegion[*job.Region] = job
		}
	}

	targetSet := make(map[string]bool, n)
	for _, r := range targetRegions {
		targetSet[r] = true
	}

	// Delete jobs for removed regions (and any null-region jobs)
	for _, job := range existingJobs {
		if job.Region == nil || !targetSet[*job.Region] {
			if err := s.db.DeleteCheckJob(ctx, job.UID); err != nil {
				return fmt.Errorf("failed to delete check job: %w", err)
			}
		}
	}

	// Create jobs for new regions, update period for existing
	splitPeriod := timeutils.Duration(basePeriod * time.Duration(n))

	for i, region := range targetRegions {
		if existing, ok := existingByRegion[region]; ok {
			// Update period, config, and type if changed
			needsUpdate := existing.Period != splitPeriod ||
				existing.Type != check.Type ||
				!configEqual(existing.Config, check.Config)

			if needsUpdate {
				if _, err := s.db.DB().NewUpdate().
					Model((*models.CheckJob)(nil)).
					Set("period = ?", splitPeriod).
					Set("type = ?", check.Type).
					Set("config = ?", check.Config).
					Set("updated_at = ?", time.Now()).
					Where("uid = ?", existing.UID).
					Exec(ctx); err != nil {
					return fmt.Errorf("failed to update check job: %w", err)
				}
			}
		} else {
			// Create new job
			scheduledAt := time.Now().Add(basePeriod * time.Duration(i))
			regionCopy := region

			job := models.NewCheckJob(check.OrganizationUID, check.UID, splitPeriod)
			job.Type = check.Type
			job.Config = check.Config
			job.Region = &regionCopy
			job.ScheduledAt = &scheduledAt

			if err := s.db.CreateCheckJob(ctx, job); err != nil {
				return fmt.Errorf("failed to create check job: %w", err)
			}
		}
	}

	return nil
}

// configEqual compares two JSONMap configs for equality.
func configEqual(configA, configB models.JSONMap) bool {
	if len(configA) != len(configB) {
		return false
	}

	for key, valA := range configA {
		valB, ok := configB[key]
		if !ok {
			return false
		}

		if fmt.Sprintf("%v", valA) != fmt.Sprintf("%v", valB) {
			return false
		}
	}

	return true
}

// convertCheckToResponse converts a database Check model to a CheckResponse.
func (s *Service) convertCheckToResponse(check *models.Check) CheckResponse {
	// Convert Period to database string format (HH:MM:SS)
	periodValue, _ := check.Period.Value()
	periodStr, _ := periodValue.(string)

	return CheckResponse{
		UID:                      check.UID,
		Name:                     check.Name,
		Slug:                     check.Slug,
		Description:              check.Description,
		CheckGroupUID:            check.CheckGroupUID,
		Type:                     &check.Type,
		Config:                   check.Config,
		Regions:                  check.Regions,
		Enabled:                  &check.Enabled,
		Internal:                 &check.Internal,
		Period:                   &periodStr,
		CreatedAt:                &check.CreatedAt,
		ReopenCooldownMultiplier: check.ReopenCooldownMultiplier,
		MaxAdaptiveIncrease:      check.MaxAdaptiveIncrease,
	}
}

// convertResultToLastResultResponse converts a Result model to LastResultResponse.
func (s *Service) convertResultToLastResultResponse(result *models.Result) *LastResultResponse {
	if result == nil {
		return nil
	}

	// Convert status int to string
	statusStr := "unknown"
	if result.Status != nil {
		switch *result.Status {
		case int(models.ResultStatusUp):
			statusStr = "up"
		case int(models.ResultStatusDown):
			statusStr = "down"
		case int(models.ResultStatusTimeout):
			statusStr = "timeout"
		case int(models.ResultStatusError):
			statusStr = "error"
		case int(models.ResultStatusCreated):
			statusStr = "created"
		}
	}

	return &LastResultResponse{
		UID:        result.UID,
		Status:     statusStr,
		Timestamp:  result.PeriodStart,
		DurationMs: result.Duration,
		Output:     result.Output,
		Metrics:    result.Metrics,
	}
}

// emitEvent creates an event for the check lifecycle.
func (s *Service) emitEvent(
	ctx context.Context,
	orgUID string,
	eventType models.EventType,
	check *models.Check,
) error {
	event := models.NewEvent(orgUID, eventType, models.ActorTypeUser)
	event.CheckUID = &check.UID
	event.Payload = models.JSONMap{
		"check_uid":  check.UID,
		"check_slug": check.Slug,
		"check_name": check.Name,
		"check_type": check.Type,
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		return fmt.Errorf("failed to create event: %w", err)
	}

	// Notify workers to pick up the new check immediately
	if s.eventNotifier != nil {
		if err := s.eventNotifier.Notify(ctx, string(eventType), "{}"); err != nil {
			slog.WarnContext(ctx, "failed to send real-time notification",
				"event_type", eventType,
				"error", err,
			)
			// Don't fail check creation for notification errors
		}
	}

	return nil
}

// Export/Import types

// ExportDocument represents the top-level JSON export format.
type ExportDocument struct {
	Version      int           `json:"version"`
	ExportedAt   string        `json:"exportedAt"`
	Organization string        `json:"organization"`
	Checks       []ExportCheck `json:"checks"`
}

// ExportCheck represents a single check in the export format.
type ExportCheck struct {
	Name                     string            `json:"name"`
	Slug                     string            `json:"slug"`
	Description              string            `json:"description,omitempty"`
	Type                     string            `json:"type"`
	Config                   map[string]any    `json:"config"`
	Regions                  []string          `json:"regions,omitempty"`
	Labels                   map[string]string `json:"labels,omitempty"`
	Enabled                  bool              `json:"enabled"`
	Internal                 bool              `json:"internal,omitempty"`
	Period                   string            `json:"period,omitempty"`
	Group                    string            `json:"group,omitempty"`
	IncidentThreshold        int               `json:"incidentThreshold,omitempty"`
	EscalationThreshold      int               `json:"escalationThreshold,omitempty"`
	RecoveryThreshold        int               `json:"recoveryThreshold,omitempty"`
	ReopenCooldownMultiplier *int              `json:"reopenCooldownMultiplier,omitempty"`
	MaxAdaptiveIncrease      *int              `json:"maxAdaptiveIncrease,omitempty"`
}

// ImportResult represents the result of an import operation.
type ImportResult struct {
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Skipped int           `json:"skipped"`
	Errors  []ImportError `json:"errors"`
}

// ImportError represents an error for a specific check during import.
type ImportError struct {
	Index int    `json:"index"`
	Slug  string `json:"slug"`
	Error string `json:"error"`
}

// ExportChecks exports checks for an organization in the portable JSON format.
//
//nolint:cyclop,funlen // Complex due to group resolution and label handling
func (s *Service) ExportChecks(
	ctx context.Context, orgSlug string, opts ListChecksOptions,
) (*ExportDocument, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Fetch all checks (no pagination limit for export)
	filter := &models.ListChecksFilter{
		Labels:        opts.Labels,
		CheckGroupUID: opts.CheckGroupUID,
		Query:         opts.Query,
	}

	checks, _, err := s.db.ListChecks(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	// Fetch labels for all checks
	checkUIDs := make([]string, len(checks))
	for i, c := range checks {
		checkUIDs[i] = c.UID
	}

	labelsMap, err := s.db.GetLabelsForChecks(ctx, checkUIDs)
	if err != nil {
		return nil, err
	}

	// Fetch check groups for group name resolution
	groups, err := s.db.ListCheckGroups(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	groupMap := make(map[string]string, len(groups))
	for _, g := range groups {
		groupMap[g.UID] = g.Name
	}

	// Build export checks
	exportChecks := make([]ExportCheck, 0, len(checks))
	for _, check := range checks {
		periodValue, _ := check.Period.Value()
		periodStr, _ := periodValue.(string)

		exported := ExportCheck{
			Type:                     check.Type,
			Config:                   check.Config,
			Regions:                  check.Regions,
			Enabled:                  check.Enabled,
			Internal:                 check.Internal,
			Period:                   periodStr,
			IncidentThreshold:        check.IncidentThreshold,
			EscalationThreshold:      check.EscalationThreshold,
			RecoveryThreshold:        check.RecoveryThreshold,
			ReopenCooldownMultiplier: check.ReopenCooldownMultiplier,
			MaxAdaptiveIncrease:      check.MaxAdaptiveIncrease,
		}

		if check.Name != nil {
			exported.Name = *check.Name
		}
		if check.Slug != nil {
			exported.Slug = *check.Slug
		}
		if check.Description != nil {
			exported.Description = *check.Description
		}

		// Resolve group name
		if check.CheckGroupUID != nil {
			if name, ok := groupMap[*check.CheckGroupUID]; ok {
				exported.Group = name
			}
		}

		// Attach labels
		if labels, ok := labelsMap[check.UID]; ok && len(labels) > 0 {
			exported.Labels = make(map[string]string, len(labels))
			for _, label := range labels {
				exported.Labels[label.Key] = label.Value
			}
		}

		exportChecks = append(exportChecks, exported)
	}

	return &ExportDocument{
		Version:      1,
		ExportedAt:   time.Now().UTC().Format(time.RFC3339),
		Organization: orgSlug,
		Checks:       exportChecks,
	}, nil
}

// ImportChecks imports checks from an export document.
func (s *Service) ImportChecks(
	ctx context.Context, orgSlug string, doc *ExportDocument, dryRun bool,
) (*ImportResult, error) {
	if doc.Version != 1 {
		return nil, ErrUnsupportedExportVersion
	}

	if len(doc.Checks) == 0 {
		return nil, ErrEmptyChecksArray
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Pre-fetch existing groups for name matching
	existingGroups, err := s.db.ListCheckGroups(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	groupByName := make(map[string]*models.CheckGroup, len(existingGroups))
	for _, g := range existingGroups {
		groupByName[strings.ToLower(g.Name)] = g
	}

	result := &ImportResult{
		Errors: []ImportError{},
	}

	for i := range doc.Checks {
		created, importErr := s.importSingleCheck(ctx, org, orgSlug, &doc.Checks[i], i, dryRun, groupByName)
		if importErr != nil {
			result.Errors = append(result.Errors, *importErr)

			continue
		}

		if created {
			result.Created++
		} else {
			result.Updated++
		}
	}

	return result, nil
}

// importSingleCheck handles importing a single check.
// Returns (wasCreated, error). wasCreated is true if a new check was created, false if updated.
func (s *Service) importSingleCheck(
	ctx context.Context,
	org *models.Organization,
	orgSlug string,
	exportedCheck *ExportCheck,
	index int,
	dryRun bool,
	groupByName map[string]*models.CheckGroup,
) (bool, *ImportError) {
	// Validate required fields
	if exportedCheck.Slug == "" {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "slug is required"}
	}
	if exportedCheck.Type == "" {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "type is required"}
	}
	if exportedCheck.Config == nil {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: "config is required"}
	}

	// Validate check type
	if _, ok := registry.GetChecker(checkerdef.CheckType(exportedCheck.Type)); !ok {
		return false, &ImportError{
			Index: index, Slug: exportedCheck.Slug, Error: "invalid check type: " + exportedCheck.Type,
		}
	}

	// Check if slug exists to determine created vs updated
	existing, _ := s.db.GetCheckByUidOrSlug(ctx, org.UID, exportedCheck.Slug)
	created := existing == nil

	if dryRun {
		return created, nil
	}

	// Resolve group by name (case-insensitive), auto-create if needed
	var checkGroupUID *string
	if exportedCheck.Group != "" {
		group, ok := groupByName[strings.ToLower(exportedCheck.Group)]
		if !ok {
			// Auto-create group
			groupSlug := sanitizeSlug(strings.ToLower(exportedCheck.Group))
			newGroup := models.NewCheckGroup(org.UID, exportedCheck.Group, groupSlug)
			if createErr := s.db.CreateCheckGroup(ctx, newGroup); createErr != nil {
				return false, &ImportError{
					Index: index, Slug: exportedCheck.Slug,
					Error: "failed to create group: " + createErr.Error(),
				}
			}
			groupByName[strings.ToLower(exportedCheck.Group)] = newGroup
			group = newGroup
		}
		checkGroupUID = &group.UID
	}

	// Build upsert request
	upsertReq := UpsertCheckRequest{
		Name:          exportedCheck.Name,
		Description:   exportedCheck.Description,
		CheckGroupUID: checkGroupUID,
		Type:          exportedCheck.Type,
		Config:        exportedCheck.Config,
		Enabled:       &exportedCheck.Enabled,
		Internal:      &exportedCheck.Internal,
		Labels:        exportedCheck.Labels,
	}
	if exportedCheck.Period != "" {
		upsertReq.Period = &exportedCheck.Period
	}

	_, _, upsertErr := s.UpsertCheck(ctx, orgSlug, exportedCheck.Slug, &upsertReq)
	if upsertErr != nil {
		return false, &ImportError{Index: index, Slug: exportedCheck.Slug, Error: upsertErr.Error()}
	}

	return created, nil
}
