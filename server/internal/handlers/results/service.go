package results

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrOrganizationNotFound is returned when organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrInvalidCursor is returned when cursor format is invalid.
	ErrInvalidCursor = errors.New("invalid cursor")
	// ErrCheckNotFound is returned when the check identifier doesn't resolve to a check in the org.
	ErrCheckNotFound = errors.New("check not found")
	// ErrResultNotFound is returned when no result and no covering aggregation exists for the given UID.
	ErrResultNotFound = errors.New("result not found")
)

// Result status string labels.
const (
	statusStrCreated = "created"
	statusStrRunning = "running"
	statusStrDown    = "down"
	statusStrUnknown = "unknown"
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
		statusStrCreated: {int(models.ResultStatusCreated)},
		statusStrRunning: {int(models.ResultStatusRunning)},
		"up":             {int(models.ResultStatusUp)},
		statusStrDown:    {int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError)},
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
		return statusStrUnknown
	}

	switch *status {
	case int(models.ResultStatusCreated):
		return statusStrCreated
	case int(models.ResultStatusRunning):
		return statusStrRunning
	case int(models.ResultStatusUp):
		return "up"
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return statusStrDown
	default:
		return statusStrUnknown
	}
}

const (
	withDurationMs       = "durationms"
	withDurationMinMs    = "durationminms"
	withDurationMaxMs    = "durationmaxms"
	withRegion           = "region"
	withMetrics          = "metrics"
	withOutput           = "output"
	withAvailabilityPct  = "availabilitypct"
	withTotalChecks      = "totalchecks"
	withSuccessfulChecks = "successfulchecks"
	withCheckSlug        = "checkslug"
	withCheckName        = "checkname"
)

// allWithFields returns the union of every optional `with` field that the
// detail endpoint always projects into the response.
func allWithFields() []string {
	return []string{
		withDurationMs, withDurationMinMs, withDurationMaxMs,
		withRegion, withMetrics, withOutput,
		withAvailabilityPct, withTotalChecks, withSuccessfulChecks,
		withCheckSlug, withCheckName,
	}
}

// FallbackInfo describes the fallback that was applied when the requested
// raw result UID had been rolled up into an aggregation.
type FallbackInfo struct {
	RequestedUID string    `json:"requestedUid"`
	RequestedAt  time.Time `json:"requestedAt"`
	Reason       string    `json:"reason"` // rolled_up_to_hour | rolled_up_to_day | rolled_up_to_month
}

// GetResultResponse wraps the standard ResultResponse and an optional
// FallbackInfo describing how the response was resolved when the raw row
// had already been rolled up into an aggregation.
type GetResultResponse struct {
	ResultResponse
	Fallback *FallbackInfo `json:"fallback,omitempty"`
}

// GetResult fetches a single result by UID, falling back to the smallest-period
// aggregation that covers the UID's embedded UUIDv7 timestamp when the raw
// row has been rolled up. checkIdent may be the check UID or slug.
func (s *Service) GetResult(
	ctx context.Context, orgSlug, checkIdent, resultUID string,
) (*GetResultResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdent)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	withAll := allWithFields()

	if direct, getErr := s.db.GetResult(ctx, resultUID); getErr == nil && direct != nil {
		if direct.OrganizationUID == org.UID && direct.CheckUID == check.UID {
			resp := s.convertResultToResponse(direct, withAll)

			return &GetResultResponse{ResultResponse: resp}, nil
		}
	}

	// UUIDv7 timestamps the row was created with; matches PeriodStart for raw
	// rows within ms, and for aggregations matches the rollup time (which still
	// falls inside the larger covering periods).
	parsed, parseErr := uuid.Parse(resultUID)
	if parseErr != nil || parsed.Version() != 7 {
		return nil, ErrResultNotFound
	}

	sec, nsec := parsed.Time().UnixTime()
	requestedAt := time.Unix(sec, nsec).UTC()

	for _, level := range []string{"hour", "day", "month"} {
		row, hitErr := s.findCoveringAggregation(ctx, org.UID, check.UID, level, requestedAt)
		if hitErr != nil {
			return nil, hitErr
		}

		if row == nil {
			continue
		}

		resp := s.convertResultToResponse(row, withAll)

		return &GetResultResponse{
			ResultResponse: resp,
			Fallback: &FallbackInfo{
				RequestedUID: resultUID,
				RequestedAt:  requestedAt,
				Reason:       "rolled_up_to_" + level,
			},
		}, nil
	}

	return nil, ErrResultNotFound
}

// findCoveringAggregation returns the aggregation row of `level` that covers
// `requestedAt` for the given check, or nil if no such row exists. When
// several rows match (e.g. one per region), pick the highest total_checks;
// ties broken by region ASC for determinism.
func (s *Service) findCoveringAggregation(
	ctx context.Context, orgUID, checkUID, level string, requestedAt time.Time,
) (*models.Result, error) {
	startBefore := requestedAt.Add(time.Nanosecond)

	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{checkUID},
		PeriodTypes:      []string{level},
		PeriodStartAfter: nil,
		PeriodEndBefore:  &startBefore,
		Limit:            32,
	}

	resp, err := s.db.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	candidates := make([]*models.Result, 0, len(resp.Results))
	for _, row := range resp.Results {
		if !row.PeriodStart.After(requestedAt) && (row.PeriodEnd == nil || row.PeriodEnd.After(requestedAt)) {
			candidates = append(candidates, row)
		}
	}

	if len(candidates) == 0 {
		return nil, nil //nolint:nilnil // nil,nil signals no match to the caller.
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		countI, countJ := 0, 0
		if candidates[i].TotalChecks != nil {
			countI = *candidates[i].TotalChecks
		}

		if candidates[j].TotalChecks != nil {
			countJ = *candidates[j].TotalChecks
		}

		if countI != countJ {
			return countI > countJ
		}

		regionI, regionJ := "", ""
		if candidates[i].Region != nil {
			regionI = *candidates[i].Region
		}

		if candidates[j].Region != nil {
			regionJ = *candidates[j].Region
		}

		return regionI < regionJ
	})

	return candidates[0], nil
}
