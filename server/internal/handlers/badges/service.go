package badges

import (
	"context"
	"errors"
	"fmt"
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

// BadgeOptions contains options for badge generation.
type BadgeOptions struct {
	Period string // "1h", "24h", "7d", "30d"
	Label  string // Custom label (default: check name)
	Style  string // "flat", "flat-square"
}

// Service provides badge generation functionality.
type Service struct {
	dbSvc db.Service
}

// NewService creates a new badge service.
func NewService(dbSvc db.Service) *Service {
	return &Service{dbSvc: dbSvc}
}

// allowedComponents is the set of valid component tokens.
var allowedComponents = map[string]bool{
	"status":        true,
	"availability":  true,
	"duration":      true,
	"response-time": true,
}

// parseComponents validates and returns the ordered list of component tokens.
func parseComponents(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		token := strings.TrimSpace(part)
		if !allowedComponents[token] {
			return nil, ErrInvalidFormat
		}

		if seen[token] {
			return nil, ErrInvalidFormat
		}

		seen[token] = true
		result = append(result, token)
	}

	// At least one primary metric (status or availability) is required.
	if !seen["status"] && !seen["availability"] {
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

	// 5. Determine what data to fetch.
	needsPeriod := false

	for _, t := range tokens {
		if t == "availability" || t == "response-time" {
			needsPeriod = true

			break
		}
	}

	var results []*models.Result

	if needsPeriod {
		periodDuration := parsePeriod(opts.Period)
		startTime := time.Now().Add(-periodDuration)
		filter := &models.ListResultsFilter{
			OrganizationUID:  org.UID,
			CheckUIDs:        []string{check.UID},
			PeriodTypes:      []string{"raw"},
			PeriodStartAfter: &startTime,
		}

		res, ferr := s.dbSvc.ListResults(ctx, filter)
		if ferr != nil {
			return "", ferr
		}

		results = res.Results
	} else {
		// Only status / duration — fetch latest 1 raw result.
		filter := &models.ListResultsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			PeriodTypes:     []string{"raw"},
			Limit:           1,
		}

		res, ferr := s.dbSvc.ListResults(ctx, filter)
		if ferr != nil {
			return "", ferr
		}

		results = res.Results
	}

	// 6. Compose value substrings in URL order.
	parts := make([]string, 0, len(tokens))

	for _, token := range tokens {
		switch token {
		case "status":
			parts = append(parts, formatStatus(results))
		case "availability":
			parts = append(parts, formatAvailability(calculateAvailability(results)))
		case "duration":
			dur, isUp, ok := calculateStatusDuration(results)
			if ok {
				if isUp {
					parts = append(parts, "↑ "+formatDuration(dur))
				} else {
					parts = append(parts, "↓ "+formatDuration(dur))
				}
			}
			// When unknown (ok=false), omit the duration component.
		case "response-time":
			parts = append(parts, formatResponseTime(results))
		}
	}

	value := strings.Join(parts, " ")

	// 7. Resolve color.
	color := resolveColor(tokens, results)

	return GenerateSVG(opts.Label, value, color, opts.Style), nil
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
		return "unknown"
	}

	if results[0].Status == nil {
		return "unknown"
	}

	switch *results[0].Status {
	case int(models.ResultStatusUp):
		return "up"
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return "down"
	default:
		return "unknown"
	}
}

// resolveColor returns the badge color based on component precedence.
func resolveColor(tokens []string, results []*models.Result) string {
	hasStatus := false
	hasAvailability := false

	for _, t := range tokens {
		if t == "status" {
			hasStatus = true
		}

		if t == "availability" {
			hasAvailability = true
		}
	}

	if hasStatus {
		// Green/red/gray based on current status.
		s := formatStatus(results)
		switch s {
		case "up":
			return ColorGreen
		case "down":
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

	for _, r := range results {
		if r.Duration != nil {
			sum += float64(*r.Duration)
			count++
		}
	}

	if count == 0 {
		return "n/a"
	}

	mean := sum / float64(count)

	if mean < 1.0 {
		return fmt.Sprintf("%dms", int(mean*1000))
	}

	return fmt.Sprintf("%.1fs", mean)
}

// calculateStatusDuration returns the time since last status change, a
// boolean indicating whether the current status is up, and a third boolean
// indicating whether the status is known at all.
func calculateStatusDuration(results []*models.Result) (duration time.Duration, isUp bool, ok bool) {
	// Find the most recent actionable status.
	currentStatus := -1

	for _, r := range results {
		if r.Status == nil {
			continue
		}

		s := models.ResultStatus(*r.Status)
		if s == models.ResultStatusCreated || s == models.ResultStatusRunning {
			continue
		}

		currentStatus = *r.Status

		break
	}

	if currentStatus == -1 {
		return 0, false, false
	}

	isUp = currentStatus == int(models.ResultStatusUp)

	// Walk oldest-to-newest to find the last status-change boundary.
	// results is ordered newest-first; we iterate from the end.
	lastChangeTime := time.Time{}

	// Scan forward (newest→oldest), stopping at the first transition away from currentStatus.
	seenCount := 0

	for _, r := range results {
		if r.Status == nil {
			continue
		}

		s := models.ResultStatus(*r.Status)
		if s == models.ResultStatusCreated || s == models.ResultStatusRunning {
			continue
		}

		if *r.Status != currentStatus {
			// This is the first result that differs — the status changed at the NEXT result we saw.
			// lastChangeTime is set to the period_start of the previous result.
			break
		}

		lastChangeTime = r.PeriodStart
		seenCount++
	}

	if seenCount == 0 {
		return 0, isUp, false
	}

	return time.Since(lastChangeTime), isUp, true
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

// calculateUptimeDuration is kept for backward-compatibility with existing tests.
// Deprecated: use calculateStatusDuration instead.
func (s *Service) calculateUptimeDuration(results []*models.Result) time.Duration {
	dur, _, _ := calculateStatusDuration(results)

	return dur
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
