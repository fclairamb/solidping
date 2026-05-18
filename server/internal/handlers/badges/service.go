package badges

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Badge service errors.
var (
	ErrCheckNotFound        = errors.New("check not found")
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrInvalidFormat        = errors.New("invalid badge format")
)

// Component token constants.
const (
	componentStatus       = "status"
	componentAvailability = "availability"
	componentDuration     = "duration"
	componentResponseTime = "response-time"
	statusUp              = "up"
	statusDown            = "down"
	statusUnknown         = "unknown"
)

// UptimeBarOptions contains options for uptime bar generation.
type UptimeBarOptions struct {
	Period string // "24h", "7d", "30d", "90d"
	Width  int    // px, default 300, range 60-800
	Height int    // px, default 20, range 10-40
	Style  string // "flat", "flat-square"
}

// BadgeOptions contains options for badge generation.
type BadgeOptions struct {
	Period   string // "1h", "24h", "7d", "30d"
	Label    string // Custom label (default: check name)
	Style    string // "flat", "flat-square"
	MinWidth int    // 0 = no minimum
}

// Service provides badge generation functionality.
type Service struct {
	dbSvc db.Service
}

// NewService creates a new badge service.
func NewService(dbSvc db.Service) *Service {
	return &Service{dbSvc: dbSvc}
}

func isAllowedComponent(token string) bool {
	switch token {
	case componentStatus, componentAvailability, componentDuration, componentResponseTime:
		return true
	default:
		return false
	}
}

// parseComponents validates and returns the ordered list of component tokens.
func parseComponents(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		token := strings.TrimSpace(part)
		if !isAllowedComponent(token) {
			return nil, ErrInvalidFormat
		}

		if seen[token] {
			return nil, ErrInvalidFormat
		}

		seen[token] = true
		result = append(result, token)
	}

	// At least one primary metric (status or availability) is required.
	if !seen[componentStatus] && !seen[componentAvailability] {
		return nil, ErrInvalidFormat
	}

	return result, nil
}

// GenerateBadge generates an SVG badge for a check.
func (s *Service) GenerateBadge(
	ctx context.Context, orgSlug, checkIdentifier, components string, opts BadgeOptions,
) (string, error) {
	// 1. Parse components first — fast validation before DB access.
	tokens, err := parseComponents(components)
	if err != nil {
		return "", err
	}

	// 2. Resolve organization.
	org, err := s.dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return "", ErrOrganizationNotFound
	}

	// 3. Resolve check by UID or slug (auto-detected).
	check, err := s.dbSvc.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return "", ErrCheckNotFound
	}

	// 4. Set defaults.
	opts = s.applyDefaults(opts, check)

	// 5. Fetch results.
	results, err := s.fetchResults(ctx, org.UID, check.UID, tokens, opts.Period)
	if err != nil {
		return "", err
	}

	// 6. Compose value substrings in URL order.
	value := s.composeValue(tokens, results)

	// 7. Resolve color.
	color := resolveColor(tokens, results)

	return GenerateSVG(opts.Label, value, color, opts.Style, opts.MinWidth), nil
}

// fetchResults fetches the results needed for the given tokens.
func (s *Service) fetchResults(
	ctx context.Context, orgUID, checkUID string, tokens []string, period string,
) ([]*models.Result, error) {
	needsPeriod := false

	for _, token := range tokens {
		if token == componentAvailability || token == componentResponseTime {
			needsPeriod = true

			break
		}
	}

	if needsPeriod {
		periodDuration := parsePeriod(period)
		startTime := time.Now().Add(-periodDuration)
		filter := &models.ListResultsFilter{
			OrganizationUID:  orgUID,
			CheckUIDs:        []string{checkUID},
			PeriodTypes:      []string{"raw"},
			PeriodStartAfter: &startTime,
		}

		res, err := s.dbSvc.ListResults(ctx, filter)
		if err != nil {
			return nil, err
		}

		return res.Results, nil
	}

	// Only status / duration — fetch latest 1 raw result.
	filter := &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       []string{checkUID},
		PeriodTypes:     []string{"raw"},
		Limit:           1,
	}

	res, err := s.dbSvc.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	return res.Results, nil
}

// composeValue builds the badge value string from the selected tokens.
func (s *Service) composeValue(tokens []string, results []*models.Result) string {
	parts := make([]string, 0, len(tokens))

	for _, token := range tokens {
		switch token {
		case componentStatus:
			parts = append(parts, formatStatus(results))
		case componentAvailability:
			parts = append(parts, formatAvailability(calculateAvailability(results)))
		case componentDuration:
			dur, isUp, ok := calculateStatusDuration(results)
			if ok {
				if isUp {
					parts = append(parts, "↑ "+formatDuration(dur))
				} else {
					parts = append(parts, "↓ "+formatDuration(dur))
				}
			}
			// When unknown (ok=false), omit the duration component.
		case componentResponseTime:
			parts = append(parts, formatResponseTime(results))
		}
	}

	return strings.Join(parts, " ")
}

func (s *Service) applyDefaults(opts BadgeOptions, check *models.Check) BadgeOptions {
	if opts.Period == "" {
		opts.Period = "24h"
	}

	if opts.Label == "" && check.Name != nil {
		opts.Label = *check.Name
	}

	if opts.Style == "" {
		opts.Style = "flat"
	}

	return opts
}

// formatStatus returns the status string for the most recent result.
func formatStatus(results []*models.Result) string {
	if len(results) == 0 {
		return statusUnknown
	}

	if results[0].Status == nil {
		return statusUnknown
	}

	switch *results[0].Status {
	case int(models.ResultStatusUp):
		return statusUp
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return statusDown
	default:
		return statusUnknown
	}
}

// resolveColor returns the badge color based on component precedence.
func resolveColor(tokens []string, results []*models.Result) string {
	hasStatus := false
	hasAvailability := false

	for _, token := range tokens {
		if token == componentStatus {
			hasStatus = true
		}

		if token == componentAvailability {
			hasAvailability = true
		}
	}

	if hasStatus {
		switch formatStatus(results) {
		case statusUp:
			return ColorGreen
		case statusDown:
			return ColorRed
		default:
			return ColorGray
		}
	}

	if hasAvailability {
		return availabilityColor(calculateAvailability(results))
	}

	return ColorGray
}

// formatResponseTime computes the mean response time from results.
func formatResponseTime(results []*models.Result) string {
	var sum float64

	count := 0

	for _, result := range results {
		if result.Duration != nil {
			sum += float64(*result.Duration)
			count++
		}
	}

	if count == 0 {
		return "n/a"
	}

	// mean is in milliseconds (Duration field stores ms, not seconds)
	mean := sum / float64(count)

	if mean < 1000 {
		return fmt.Sprintf("%dms", int(math.Round(mean)))
	}

	return fmt.Sprintf("%.1fs", mean/1000)
}

// calculateStatusDuration returns the time since last status change, a
// boolean indicating whether the current status is up, and a third boolean
// indicating whether the status is known at all.
func calculateStatusDuration(results []*models.Result) (time.Duration, bool, bool) {
	// Find the most recent actionable status.
	currentStatus := -1

	for _, res := range results {
		if res.Status == nil {
			continue
		}

		s := models.ResultStatus(*res.Status)
		if s == models.ResultStatusCreated || s == models.ResultStatusRunning {
			continue
		}

		currentStatus = *res.Status

		break
	}

	if currentStatus == -1 {
		return 0, false, false
	}

	isUp := currentStatus == int(models.ResultStatusUp)

	// Walk newest→oldest, stopping at the first transition away from currentStatus.
	// Record the period_start of the oldest consecutive result with currentStatus.
	lastChangeTime := time.Time{}
	seenCount := 0

	for _, res := range results {
		if res.Status == nil {
			continue
		}

		s := models.ResultStatus(*res.Status)
		if s == models.ResultStatusCreated || s == models.ResultStatusRunning {
			continue
		}

		if *res.Status != currentStatus {
			// First result that differs — stop here.
			break
		}

		lastChangeTime = res.PeriodStart
		seenCount++
	}

	if seenCount == 0 {
		return 0, isUp, false
	}

	return time.Since(lastChangeTime), isUp, true
}

// uptimeBarPeriodInfo returns the periodType, number of segments, and bucket
// duration for the given period string.
func uptimeBarPeriodInfo(period string) (periodType string, n int, bucketDuration time.Duration) {
	switch period {
	case "24h":
		return models.PeriodTypeHour, 24, time.Hour
	case "7d":
		return models.PeriodTypeDay, 7, 24 * time.Hour
	case "90d":
		return models.PeriodTypeDay, 90, 24 * time.Hour
	default: // "30d"
		return models.PeriodTypeDay, 30, 24 * time.Hour
	}
}

// uptimeBarColor returns the SVG hex color for the given availability percentage.
func uptimeBarColor(pct float64) string {
	switch {
	case pct >= 99.9:
		return ColorGreen
	case pct >= 99:
		return ColorYellow
	case pct >= 98:
		return ColorOrange
	default:
		return ColorRed
	}
}

// GenerateUptimeBar generates an SVG uptime bar for a check.
func (s *Service) GenerateUptimeBar(
	ctx context.Context, orgSlug, checkIdentifier string, opts UptimeBarOptions,
) (string, error) {
	// 1. Resolve organization.
	org, err := s.dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return "", ErrOrganizationNotFound
	}

	// 2. Resolve check by UID or slug (auto-detected).
	check, err := s.dbSvc.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return "", ErrCheckNotFound
	}

	// 3. Determine period info.
	periodType, n, bucketDuration := uptimeBarPeriodInfo(opts.Period)

	// 4. Compute bucket start: go back N full buckets from the current bucket boundary.
	now := time.Now().UTC()
	bucketStart := now.Truncate(bucketDuration).Add(-time.Duration(n) * bucketDuration)

	// 5. Fetch aggregated results.
	filter := &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{periodType},
		PeriodStartAfter: &bucketStart,
	}

	res, err := s.dbSvc.ListResults(ctx, filter)
	if err != nil {
		return "", err
	}

	// 6. Build map of period_start → availability_pct.
	availMap := make(map[time.Time]float64, len(res.Results))
	for _, r := range res.Results {
		if r.AvailabilityPct != nil {
			availMap[r.PeriodStart.UTC().Truncate(bucketDuration)] = *r.AvailabilityPct
		}
	}

	// 7. Iterate N buckets and resolve colors.
	segments := make([]string, n)
	for i := range n {
		t := bucketStart.Add(time.Duration(i) * bucketDuration)
		if pct, ok := availMap[t]; ok {
			segments[i] = uptimeBarColor(pct)
		} else {
			segments[i] = ColorGray
		}
	}

	return GenerateUptimeBarSVG(segments, opts.Width, opts.Height, opts.Style), nil
}

func parsePeriod(period string) time.Duration {
	switch period {
	case "1h":
		return time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default: // "24h"
		return 24 * time.Hour
	}
}

func calculateAvailability(results []*models.Result) float64 {
	if len(results) == 0 {
		return 0
	}

	var upCount, total int

	for _, result := range results {
		if result.Status != nil {
			status := models.ResultStatus(*result.Status)
			if status == models.ResultStatusCreated || status == models.ResultStatusRunning {
				continue
			}

			total++

			if *result.Status == int(models.ResultStatusUp) {
				upCount++
			}
		}
	}

	if total == 0 {
		return 0
	}

	return float64(upCount) / float64(total) * 100
}

func availabilityColor(pct float64) string {
	switch {
	case pct >= 99.9:
		return ColorGreen
	case pct >= 99:
		return ColorYellow
	case pct >= 98:
		return ColorOrange
	default:
		return ColorRed
	}
}

func formatAvailability(pct float64) string {
	if pct >= 99.99 {
		return fmt.Sprintf("%.2f%%", pct)
	}

	return fmt.Sprintf("%.1f%%", pct)
}

func formatDuration(duration time.Duration) string {
	switch {
	case duration >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(duration.Hours()/24))
	case duration >= time.Hour:
		return fmt.Sprintf("%dh", int(duration.Hours()))
	default:
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	}
}
