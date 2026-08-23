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

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
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
	statusStrCreated  = "created"
	statusStrRunning  = "running"
	statusStrDown     = "down"
	statusStrWarning  = "warning"
	statusStrDegraded = "degraded"
	statusStrUnknown  = "unknown"
	// statusStrAbandoned surfaces models.ResultStatusAbandoned as its own
	// filterable, renderable state: an attempt nobody ever reported on. It is
	// deliberately NOT folded into statusStrDown — an abandoned row is
	// precisely *not* downtime (spec 2026-08-18-10), so `?status=down` must
	// keep excluding it, exactly as availability does.
	statusStrAbandoned = "abandoned"
)

// Service provides business logic for results.
type Service struct {
	db db.Service
	// cfg is only read for the legacy koanf retention fallback when resolving
	// the raw-tier clamp; it may be nil (see NewService).
	cfg *config.Config
	// regions normalizes the `?region=` filter. A read filter is an input path
	// like any other, so it accepts the legacy `@<org>/<slug>` spelling for this
	// org and rejects a foreign one (spec 2026-08-13-01).
	regions *regions.Service
}

// NewService creates a new results service. cfg may be nil (the MCP surface and
// isolated tests have none) — the raw-tier clamp then resolves retention from
// the live performance.* parameters and, failing those, the documented defaults.
func NewService(dbService db.Service, cfg *config.Config) *Service {
	return &Service{db: dbService, cfg: cfg, regions: regions.NewService(dbService)}
}

// normalizeRegionFilter canonicalizes a caller-supplied `?region=` list.
//
// Without this, a bookmarked or shared link carrying the pre-2026-08-13
// spelling (`?region=@acmetech/aws-paris`) silently returns an EMPTY series
// once migration 012 has rewritten the rows — the same unexplained-nothing
// failure mode this whole spec exists to remove. A foreign org slug is a hard
// 400 rather than an empty chart: an explicit error beats a silent nothing, and
// it keeps one rule across every input path (check writes reject it too).
func (s *Service) normalizeRegionFilter(
	ctx context.Context, orgUID string, regionSlugs []string,
) ([]string, error) {
	if len(regionSlugs) == 0 {
		return regionSlugs, nil
	}

	return s.regions.NormalizeRegionsForOrg(ctx, orgUID, regionSlugs)
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
	DurationAvgMs    *float32       `json:"durationAvgMs,omitempty"`
	DurationP95Ms    *float32       `json:"durationP95Ms,omitempty"`
	Region           *string        `json:"region,omitempty"`
	CheckSlug        *string        `json:"checkSlug,omitempty"`
	CheckName        *string        `json:"checkName,omitempty"`
	Metrics          map[string]any `json:"metrics,omitempty"`
	Output           map[string]any `json:"output,omitempty"`
	AvailabilityPct  *float64       `json:"availabilityPct,omitempty"`
	TotalChecks      *int           `json:"totalChecks,omitempty"`
	SuccessfulChecks *int           `json:"successfulChecks,omitempty"`
}

// PaginationResponse contains pagination metadata. Deliberately no `total`:
// results is the largest table in the system, so this endpoint is
// cursor-paginated only — walk pages via `cursor` until it comes back empty
// (spec 2026-08-18-04). Contrast with incidents/checks, which return a real
// total from a bounded query.
type PaginationResponse struct {
	Cursor string `json:"cursor,omitempty"`
	Size   int    `json:"size"`
}

// WindowResponse reports the time window the server actually queried, which is
// not always the one the caller asked for: a raw-tier request is clamped to the
// raw-retention band (see clampRawWindow). It is emitted on every response —
// including when nothing was clamped — so `clamped: false` is an answer a
// client can act on rather than an absent key it has to guess about. Additive
// and optional: no pre-existing consumer reads it.
type WindowResponse struct {
	// PeriodStartAfter is the effective `period_start >=` bound, absent when the
	// query has no lower bound at all.
	PeriodStartAfter *time.Time `json:"periodStartAfter,omitempty"`
	// PeriodEndBefore is the effective `period_start <` bound (NOT period_end —
	// see the openapi description), absent when the query has no upper bound.
	PeriodEndBefore *time.Time `json:"periodEndBefore,omitempty"`
	// Clamped reports whether the server moved PeriodStartAfter forward from
	// what the caller requested.
	Clamped bool `json:"clamped"`
}

// ListResultsResponse is the response for listing results.
type ListResultsResponse struct {
	Data       []ResultResponse   `json:"data"`
	Pagination PaginationResponse `json:"pagination"`
	Window     WindowResponse     `json:"window"`
}

// clampRawWindow bounds a raw-only request to the raw-retention band and reports
// whether it moved the caller's lower bound.
//
// This endpoint is public API — dash0, the MCP tools and third-party scripts all
// reach it — so a `periodType=raw` request spanning a year is otherwise planned
// and executed against the largest table in the system with nothing to stop it.
// Rows older than the band do not exist: the aggregation job compacts a bucket
// and deletes its source raw rows in one transaction, so this removes work
// without removing results.
//
// It deliberately fires ONLY for a raw-only request, not for every request whose
// period types "include" raw. A mixed (or unfiltered) request also selects
// rollup rows, whose retention is months rather than hours; clamping those to
// the raw band would delete real results from the answer instead of removing
// dead work. Mixed is separately the shape spec 2026-08-22-04 forbids clients
// from sending, because neither partial index on `results` can serve it.
//
// Retention is resolved through systemconfig, never from cfg.Aggregation
// directly: the server "Aggregation" settings tab writes performance.*
// parameters that never reach the koanf struct, and a reader clamping to 24 h
// while the job keeps 168 h would silently drop six days of raw that no rollup
// covers. uptimebar.RawTierStart is the one raw bound in the system — reused,
// not reimplemented, so the two cannot drift.
func (s *Service) clampRawWindow(ctx context.Context, filter *models.ListResultsFilter) bool {
	if models.PeriodTypesTierSide(filter.PeriodTypes) != models.PeriodTierRaw {
		return false
	}

	rawHours, _ := systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)

	var requested time.Time
	if filter.PeriodStartAfter != nil {
		requested = *filter.PeriodStartAfter
	}

	bound := uptimebar.RawTierStart(requested, time.Now().UTC(), rawHours)
	if filter.PeriodStartAfter != nil && !bound.After(requested) {
		return false
	}

	filter.PeriodStartAfter = &bound

	return true
}

// buildListFilter turns the parsed query options into the DB-layer filter,
// resolving the identifiers (check UID-or-slug, region spelling, status names)
// that only the service knows how to normalize.
func (s *Service) buildListFilter(
	ctx context.Context, orgUID string, opts *ListResultsOptions,
) (*models.ListResultsFilter, error) {
	filter := &models.ListResultsFilter{
		OrganizationUID: orgUID,
		Limit:           opts.Size + 1, // Fetch one extra to determine hasMore
		CheckTypes:      opts.CheckTypes,
		PeriodTypes:     opts.PeriodTypes,

		PeriodStartAfter: opts.PeriodStartAfter,
		PeriodEndBefore:  opts.PeriodEndBefore,

		// Determine if we need check info for response
		IncludeCheckInfo: s.needsCheckInfo(opts.With),

		// `metrics` and `output` are by far the widest columns on a results row.
		// Unless this request actually asked for one of them, drop both from the
		// projection so a 1 000-row chart page stops shipping and jsonb-decoding
		// two blobs per row that convertResultToResponse would immediately
		// discard (spec 2026-08-22-04 §3). Derived from the SAME lowercased
		// `with`-set the response conversion uses, so the projection and the
		// serialization can never disagree: a blob that was asked for is never
		// dropped by the projection.
		SkipBlobs: !needsBlobs(opts.With),
	}

	// Resolve check identifiers to UIDs
	if len(opts.Checks) > 0 {
		filter.CheckUIDs = s.resolveCheckIdentifiers(ctx, orgUID, opts.Checks)
	}

	// Map status strings to integers
	if len(opts.Statuses) > 0 {
		filter.Statuses = s.mapStatusStringsToInts(opts.Statuses)
	}

	normalizedRegions, err := s.normalizeRegionFilter(ctx, orgUID, opts.Regions)
	if err != nil {
		return nil, err
	}

	filter.Regions = normalizedRegions

	// Parse cursor
	if opts.Cursor != "" {
		ts, uid, errCursor := s.decodeCursor(opts.Cursor)
		if errCursor != nil {
			return nil, ErrInvalidCursor
		}

		filter.CursorTimestamp = &ts
		filter.CursorUID = &uid
	}

	return filter, nil
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

	filter, err := s.buildListFilter(ctx, org.UID, opts)
	if err != nil {
		return nil, err
	}

	// Bound a raw-only request to the raw-retention band before the query is
	// built, so the reported window is the one that actually ran.
	clamped := s.clampRawWindow(ctx, filter)

	// Execute query
	dbResults, err := s.db.ListResults(sloghook.WithCallsite(ctx, "results.list"), filter)
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
			Cursor: nextCursor,
			Size:   len(responses),
		},
		Window: WindowResponse{
			PeriodStartAfter: filter.PeriodStartAfter,
			PeriodEndBefore:  filter.PeriodEndBefore,
			Clamped:          clamped,
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
		// Filterable on its own; never part of `down` — see statusStrAbandoned.
		statusStrAbandoned: {int(models.ResultStatusAbandoned)},
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

// needsBlobs reports whether `with` names a field backed by one of the two
// JSON blob columns (metrics, output). It builds the same set
// convertResultToResponse builds, so the projection decision in ListResults and
// the serialization decision in applyDetailFields are driven by one rule.
func needsBlobs(with []string) bool {
	withSet := buildWithSet(with)

	return withSet[withMetrics] || withSet[withOutput]
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

	if withSet["durationavgms"] && result.DurationAvg != nil {
		resp.DurationAvgMs = result.DurationAvg
	}

	if withSet["durationp95ms"] && result.DurationP95 != nil {
		resp.DurationP95Ms = result.DurationP95
	}
}

func (s *Service) applyDetailFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	if withSet["region"] {
		resp.Region = result.Region
	}

	// CheckSlug/CheckName are only populated on the model when the query
	// joined `checks` (ListResultsFilter.IncludeCheckInfo, set whenever
	// needsCheckInfo saw checkslug/checkname in `with`) — nil here for any
	// other read path, or for an orphaned check_uid behind a LEFT JOIN.
	if withSet["checkslug"] && result.CheckSlug != nil {
		resp.CheckSlug = result.CheckSlug
	}

	if withSet["checkname"] && result.CheckName != nil {
		resp.CheckName = result.CheckName
	}

	if withSet["metrics"] && len(result.Metrics) > 0 {
		resp.Metrics = result.Metrics
	}

	if withSet["output"] && len(result.Output) > 0 {
		resp.Output = result.Output
	}
}

func (s *Service) applyAggregationFields(resp *ResultResponse, result *models.Result, withSet map[string]bool) {
	// availability_pct is no longer stored; derive it from the count columns at
	// serialization time (successful_checks / total_checks × 100), null when
	// total_checks == 0 — matching the availability and badges handlers.
	if withSet["availabilitypct"] && result.TotalChecks != nil && *result.TotalChecks > 0 {
		successful := 0
		if result.SuccessfulChecks != nil {
			successful = *result.SuccessfulChecks
		}

		pct := float64(successful) * 100.0 / float64(*result.TotalChecks)
		resp.AvailabilityPct = &pct
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
	case int(models.ResultStatusWarning):
		// Raw "up, but something to report" — counts as up, rendered amber.
		return statusStrWarning
	case int(models.ResultStatusDegraded):
		// Aggregated rollup status (hour/day/month rows).
		return statusStrDegraded
	case int(models.ResultStatusAbandoned):
		// Reaper-minted: nothing was ever reported for this attempt. Neutral,
		// never "down" — it is excluded from availability, not counted as an
		// outage (spec 2026-08-18-10).
		return statusStrAbandoned
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
	withDurationAvgMs    = "durationavgms"
	withDurationP95Ms    = "durationp95ms"
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
		withDurationAvgMs, withDurationP95Ms,
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
	// PreviousUID is the next-older result in the same org+check+periodType
	// series (optionally narrowed by region); omitted when the effective row
	// is the oldest in scope.
	PreviousUID string `json:"previousUid,omitempty"`
	// NextUID is the next-newer result in the same series; omitted when the
	// effective row is the newest in scope.
	NextUID string `json:"nextUid,omitempty"`
}

// resolveResultScope resolves the org, the check, and the normalized region
// scope a GetResult call operates in. Split out of GetResult so the fallback
// walk below stays inside the cyclomatic budget.
func (s *Service) resolveResultScope(
	ctx context.Context, orgSlug, checkIdent string, regionFilter []string,
) (*models.Organization, *models.Check, []string, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdent)
	if err != nil || check == nil {
		return nil, nil, nil, ErrCheckNotFound
	}

	regionScope, err := s.normalizeRegionFilter(ctx, org.UID, regionFilter)
	if err != nil {
		return nil, nil, nil, err
	}

	return org, check, regionScope, nil
}

// GetResult fetches a single result by UID, falling back to the smallest-period
// aggregation that covers the UID's embedded UUIDv7 timestamp when the raw
// row has been rolled up. checkIdent may be the check UID or slug. regionFilter
// (optional) narrows the previous/next neighbor scope only — the row itself
// is still resolved by UID regardless of region — and is normalized exactly
// like the list filter, so a legacy `@<org>/<slug>` link keeps working.
func (s *Service) GetResult(
	ctx context.Context, orgSlug, checkIdent, resultUID string, regionFilter []string,
) (*GetResultResponse, error) {
	org, check, regionScope, err := s.resolveResultScope(ctx, orgSlug, checkIdent, regionFilter)
	if err != nil {
		return nil, err
	}

	withAll := allWithFields()

	if direct, getErr := s.db.GetResult(ctx, resultUID); getErr == nil && direct != nil {
		if direct.OrganizationUID == org.UID && direct.CheckUID == check.UID {
			resp := s.convertResultToResponse(direct, withAll)

			prevUID, nextUID, neighborErr := s.db.GetResultNeighbors(
				ctx, org.UID, check.UID, resp.PeriodType, regionScope, resp.PeriodStart, resp.UID)
			if neighborErr != nil {
				return nil, neighborErr
			}

			return &GetResultResponse{ResultResponse: resp, PreviousUID: prevUID, NextUID: nextUID}, nil
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

		// Neighbors are computed from the covering aggregation's own series
		// (same periodType as the effective row), not the requested raw UID —
		// every step then lands on a real row with no fallback banner.
		prevUID, nextUID, neighborErr := s.db.GetResultNeighbors(
			ctx, org.UID, check.UID, resp.PeriodType, regionScope, resp.PeriodStart, resp.UID)
		if neighborErr != nil {
			return nil, neighborErr
		}

		return &GetResultResponse{
			ResultResponse: resp,
			Fallback: &FallbackInfo{
				RequestedUID: resultUID,
				RequestedAt:  requestedAt,
				Reason:       "rolled_up_to_" + level,
			},
			PreviousUID: prevUID,
			NextUID:     nextUID,
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
