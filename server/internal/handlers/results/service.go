package results

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrOrganizationNotFound is returned when organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrInvalidCursor is returned when cursor format is invalid.
	ErrInvalidCursor = errors.New("invalid cursor")
)

// Service provides business logic for results.
type Service struct {
	db db.Service
}

// NewService creates a new results service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// ListResultsOptions provides filtering and pagination options for listing results.
type ListResultsOptions struct {
	Checks           []string // Check UIDs or slugs
	CheckTypes       []string // http, dns, ping, ssl
	Statuses         []string // up, down, unknown
	Regions          []string
	PeriodTypes      []string   // raw, hour, day, month
	PeriodStartAfter *time.Time // Filter period_start >= this value
	PeriodEndBefore  *time.Time // Filter period_start < this value
	Cursor           string
	Size             int
	With             []string // Optional fields to include
}

// ResultResponse represents a single result in the API response.
type ResultResponse struct {
	UID         string     `json:"uid"`
	CheckUID    string     `json:"checkUid"`
	PeriodType  string     `json:"periodType"`
	PeriodStart time.Time  `json:"periodStart"`
	PeriodEnd   *time.Time `json:"periodEnd,omitempty"`
	Status      string     `json:"status"`

	// Optional fields (controlled by 'with' parameter)
	DurationMs       *float32       `json:"durationMs,omitempty"`
	DurationMinMs    *float32       `json:"durationMinMs,omitempty"`
	DurationMaxMs    *float32       `json:"durationMaxMs,omitempty"`
	Region           *string        `json:"region,omitempty"`
	CheckSlug        *string        `json:"checkSlug,omitempty"`
	CheckName        *string        `json:"checkName,omitempty"`
	Metrics          map[string]any `json:"metrics,omitempty"`
	Output           map[string]any `json:"output,omitempty"`
	AvailabilityPct  *float64       `json:"availabilityPct,omitempty"`
	TotalChecks      *int           `json:"totalChecks,omitempty"`
	SuccessfulChecks *int           `json:"successfulChecks,omitempty"`
}

// PaginationResponse contains pagination metadata.
type PaginationResponse struct {
	Total  int64  `json:"total"`
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}

// ListResultsResponse is the response for listing results.
type ListResultsResponse struct {
	Data       []ResultResponse   `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
}

// ListResults lists results for an organization with filtering and pagination.
func (s *Service) ListResults(
	ctx context.Context, orgSlug string, opts *ListResultsOptions,
) (*ListResultsResponse, error) {
	// Get organization
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	// Build filter
	filter := models.ListResultsFilter{
		OrganizationUID: org.UID,
		Limit:           opts.Size + 1, // Fetch one extra to determine hasMore
	}

	// Resolve check identifiers to UIDs
	if len(opts.Checks) > 0 {
		checkUIDs := s.resolveCheckIdentifiers(ctx, org.UID, opts.Checks)
		filter.CheckUIDs = checkUIDs
	}

	// Map status strings to integers
	if len(opts.Statuses) > 0 {
		filter.Statuses = s.mapStatusStringsToInts(opts.Statuses)
	}

	// Set other filters
	filter.CheckTypes = opts.CheckTypes
	filter.Regions = opts.Regions
	filter.PeriodTypes = opts.PeriodTypes
	filter.PeriodStartAfter = opts.PeriodStartAfter
	filter.PeriodEndBefore = opts.PeriodEndBefore

	// Parse cursor
	if opts.Cursor != "" {
		ts, uid, errCursor := s.decodeCursor(opts.Cursor)
		if errCursor != nil {
			return nil, ErrInvalidCursor
		}
		filter.CursorTimestamp = &ts
		filter.CursorUID = &uid
	}

	// Determine if we need check info for response
	filter.IncludeCheckInfo = s.needsCheckInfo(opts.With)

	// Execute query
	dbResults, err := s.db.ListResults(ctx, &filter)
	if err != nil {
		return nil, err
	}

	// Check if there are more results
	hasMore := len(dbResults.Results) > opts.Size
	results := dbResults.Results
	if hasMore {
		results = results[:opts.Size]
	}

	// Convert to response format
	responses := make([]ResultResponse, len(results))
	for i, result := range results {
		responses[i] = s.convertResultToResponse(result, opts.With)
	}

	// Build next cursor
	var nextCursor string
	if hasMore && len(results) > 0 {
		lastResult := results[len(results)-1]
		nextCursor = s.encodeCursor(lastResult.PeriodStart, lastResult.UID)
	}

	return &ListResultsResponse{
		Data: responses,
		Pagination: PaginationResponse{
			Total:  dbResults.Total,
			Cursor: nextCursor,
			Size:   len(responses),
		},
	}, nil
}

func (s *Service) resolveCheckIdentifiers(ctx context.Context, orgUID string, identifiers []string) []string {
	uids := make([]string, 0, len(identifiers))

	for _, id := range identifiers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		// Look up check by UID or slug (auto-detected)
		check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, id)
		if err == nil && check != nil {
			uids = append(uids, check.UID)
		}
		// Silently ignore identifiers that don't match any check
	}

	return uids
}

func (s *Service) mapStatusStringsToInts(statuses []string) []int {
	statusMap := map[string][]int{
		"up":      {1},
		"down":    {2, 3, 4}, // down, timeout, error
		"unknown": {0},
		"running": {5},
	}

	var result []int
	seen := make(map[int]bool)

	for _, status := range statuses {
		if ints, ok := statusMap[strings.ToLower(strings.TrimSpace(status))]; ok {
			for _, i := range ints {
				if !seen[i] {
					seen[i] = true
					result = append(result, i)
				}
			}
		}
	}

	return result
}

func (s *Service) encodeCursor(timestamp time.Time, uid string) string {
	cursorStr := fmt.Sprintf("%s|%s", timestamp.Format(time.RFC3339Nano), uid)

	return base64.URLEncoding.EncodeToString([]byte(cursorStr))
}

func (s *Service) decodeCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return time.Time{}, "", fmt.Errorf("invalid cursor format: %w", ErrInvalidCursor)
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}

	return ts, parts[1], nil
}

func (s *Service) needsCheckInfo(with []string) bool {
	for _, field := range with {
		field = strings.TrimSpace(strings.ToLower(field))
		if field == "checkslug" || field == "checkname" {
			return true
		}
	}

	return false
}

func (s *Service) convertResultToResponse(result *models.Result, with []string) ResultResponse {
	resp := ResultResponse{
		UID:         result.UID,
		CheckUID:    result.CheckUID,
		PeriodType:  result.PeriodType,
		PeriodStart: result.PeriodStart,
		PeriodEnd:   result.PeriodEnd,
		Status:      s.statusIntToString(result.Status),
	}

	withSet := buildWithSet(with)
	s.applyOptionalFields(&resp, result, withSet)

	return resp
}

func buildWithSet(with []string) map[string]bool {
	withSet := make(map[string]bool, len(with))
	for _, field := range with {
		withSet[strings.TrimSpace(strings.ToLower(field))] = true
	}

	return withSet
}

func (s *Service) applyOptionalFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	s.applyDurationFields(resp, result, withSet)
	s.applyDetailFields(resp, result, withSet)
	s.applyAggregationFields(resp, result, withSet)
}

func (s *Service) applyDurationFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	if withSet["durationms"] && result.Duration != nil {
		resp.DurationMs = result.Duration
	}

	if withSet["durationminms"] && result.DurationMin != nil {
		resp.DurationMinMs = result.DurationMin
	}

	if withSet["durationmaxms"] && result.DurationMax != nil {
		resp.DurationMaxMs = result.DurationMax
	}
}

func (s *Service) applyDetailFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	if withSet["region"] {
		resp.Region = result.Region
	}

	if withSet["metrics"] && len(result.Metrics) > 0 {
		resp.Metrics = result.Metrics
	}

	if withSet["output"] && len(result.Output) > 0 {
		resp.Output = result.Output
	}
}

func (s *Service) applyAggregationFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	if withSet["availabilitypct"] && result.AvailabilityPct != nil {
		resp.AvailabilityPct = result.AvailabilityPct
	}

	if withSet["totalchecks"] && result.TotalChecks != nil {
		resp.TotalChecks = result.TotalChecks
	}

	if withSet["successfulchecks"] && result.SuccessfulChecks != nil {
		resp.SuccessfulChecks = result.SuccessfulChecks
	}
}

func (s *Service) statusIntToString(status *int) string {
	if status == nil {
		return "unknown"
	}

	switch *status {
	case 1:
		return "up"
	case 2, 3, 4:
		return "down"
	case 5:
		return "running"
	case 6:
		return "created"
	default:
		return "unknown"
	}
}
