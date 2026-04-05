package badges

import (
	"context"
	"errors"
	"fmt"
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

// GenerateBadge generates an SVG badge for a check.
func (s *Service) GenerateBadge(
	ctx context.Context, orgSlug, checkIdentifier, format string, opts BadgeOptions,
) (string, error) {
	// 1. Resolve organization
	org, err := s.dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return "", ErrOrganizationNotFound
	}

	// 2. Resolve check by UID or slug (auto-detected)
	check, err := s.dbSvc.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return "", ErrCheckNotFound
	}

	// 3. Set defaults
	opts = s.applyDefaults(opts, check)

	// 4. Generate badge based on format
	switch format {
	case "status":
		return s.generateStatusBadge(ctx, check, opts)
	case "availability":
		return s.generateAvailabilityBadge(ctx, org.UID, check, opts, false)
	case "availability-duration":
		return s.generateAvailabilityBadge(ctx, org.UID, check, opts, true)
	default:
		return "", ErrInvalidFormat
	}
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

func (s *Service) generateStatusBadge(
	ctx context.Context, check *models.Check, opts BadgeOptions,
) (string, error) {
	filter := &models.ListResultsFilter{
		OrganizationUID: check.OrganizationUID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{"raw"},
		Limit:           1,
	}

	results, err := s.dbSvc.ListResults(ctx, filter)
	if err != nil {
		return "", err
	}

	status := "unknown"
	color := ColorGray

	if len(results.Results) > 0 && results.Results[0].Status != nil {
		switch *results.Results[0].Status {
		case int(models.ResultStatusUp):
			status = "up"
			color = ColorGreen
		case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
			status = "down"
			color = ColorRed
		}
	}

	return GenerateSVG(opts.Label, status, color, opts.Style), nil
}

func (s *Service) generateAvailabilityBadge(
	ctx context.Context, orgUID string, check *models.Check, opts BadgeOptions, showDuration bool,
) (string, error) {
	periodDuration := parsePeriod(opts.Period)
	startTime := time.Now().Add(-periodDuration)

	filter := &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{"raw"},
		PeriodStartAfter: &startTime,
	}

	results, err := s.dbSvc.ListResults(ctx, filter)
	if err != nil {
		return "", err
	}

	availability := calculateAvailability(results.Results)
	color := availabilityColor(availability)

	value := formatAvailability(availability)
	if showDuration {
		duration := s.calculateUptimeDuration(results.Results)
		value = value + " ↑ " + formatDuration(duration)
	}

	return GenerateSVG(opts.Label, value, color, opts.Style), nil
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
			if models.ResultStatus(*result.Status) == models.ResultStatusCreated {
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

func (s *Service) calculateUptimeDuration(results []*models.Result) time.Duration {
	// Iterate from newest to oldest, find first non-up status
	seenCount := 0
	for _, result := range results {
		if result.Status != nil && models.ResultStatus(*result.Status) == models.ResultStatusCreated {
			continue
		}
		if result.Status != nil && *result.Status != int(models.ResultStatusUp) {
			if seenCount == 0 {
				return 0 // Currently down
			}

			return time.Since(result.PeriodStart)
		}
		seenCount++
	}
	// All results are up
	if len(results) > 0 {
		return time.Since(results[len(results)-1].PeriodStart)
	}

	return 0
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
