// Package timeutils provides time-related utility functions.
package timeutils

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Errors returned by ISO 8601 duration parsing and formatting.
var (
	// ErrInvalidISO8601Format is returned when the ISO 8601 duration format is invalid.
	ErrInvalidISO8601Format = errors.New("invalid ISO 8601 duration format")

	// ErrInvalidDurationNumber is returned when a duration number is invalid.
	ErrInvalidDurationNumber = errors.New("invalid number in duration")

	// ErrUnknownDurationUnit is returned when a duration unit is unknown.
	ErrUnknownDurationUnit = errors.New("unknown duration unit")

	// ErrInvalidScanType is returned when scanning into Duration fails due to type mismatch.
	ErrInvalidScanType = errors.New("invalid type for Duration scan")

	// ErrInvalidDurationFormat is returned when the duration format cannot be parsed.
	ErrInvalidDurationFormat = errors.New("invalid duration format")

	// ErrInvalidPostgresInterval is returned when parsing PostgreSQL interval fails.
	ErrInvalidPostgresInterval = errors.New("invalid PostgreSQL interval")
)

// ParseISO8601Duration parses ISO 8601 duration strings like "PT1M", "PT30S", "PT1H".
// This is needed for cross-database compatibility (PostgreSQL and SQLite).
// Only supports hours, minutes, and seconds.
func ParseISO8601Duration(duration string) (time.Duration, error) {
	// Simple ISO 8601 duration parser for formats like PT1H, PT30M, PT45S
	if len(duration) < 3 || !strings.HasPrefix(duration, "PT") {
		return 0, fmt.Errorf("%w: %s", ErrInvalidISO8601Format, duration)
	}

	duration = duration[2:] // Remove "PT" prefix

	var result time.Duration

	// Parse H, M, S components
	for len(duration) > 0 {
		// Find the next unit marker (H, M, or S)
		i := strings.IndexAny(duration, "HMS")
		if i == -1 {
			break
		}

		if i == 0 || i >= len(duration) {
			return 0, fmt.Errorf("%w: PT%s", ErrInvalidISO8601Format, duration)
		}

		valueStr := duration[:i]
		unit := duration[i]

		value, err := strconv.Atoi(valueStr)
		if err != nil {
			return 0, fmt.Errorf("%w: %s", ErrInvalidDurationNumber, valueStr)
		}

		switch unit {
		case 'H':
			result += time.Duration(value) * time.Hour
		case 'M':
			result += time.Duration(value) * time.Minute
		case 'S':
			result += time.Duration(value) * time.Second
		default:
			return 0, fmt.Errorf("%w: %c", ErrUnknownDurationUnit, unit)
		}

		duration = duration[i+1:]
	}

	return result, nil
}

// FormatISO8601Duration converts a time.Duration to an ISO 8601 duration string like "PT1M", "PT30S", "PT1H30M".
// This is needed for cross-database compatibility (PostgreSQL and SQLite).
// Only supports hours, minutes, and seconds.
func FormatISO8601Duration(duration time.Duration) string {
	// Build ISO 8601 duration string for formats like PT1H, PT30M, PT45S, PT1H30M45S
	if duration == 0 {
		return "PT0S"
	}

	var result strings.Builder
	result.WriteString("PT")

	remaining := duration

	// Extract hours
	hours := int(remaining.Hours())
	if hours > 0 {
		result.WriteString(strconv.Itoa(hours))
		result.WriteString("H")
		remaining -= time.Duration(hours) * time.Hour
	}

	// Extract minutes
	minutes := int(remaining.Minutes())
	if minutes > 0 {
		result.WriteString(strconv.Itoa(minutes))
		result.WriteString("M")
		remaining -= time.Duration(minutes) * time.Minute
	}

	// Extract seconds
	seconds := int(remaining.Seconds())
	if seconds > 0 {
		result.WriteString(strconv.Itoa(seconds))
		result.WriteString("S")
	}

	return result.String()
}

// formatDurationAsInterval converts a time.Duration to HH:MM:SS format.
// This format is compatible with both PostgreSQL intervals and SQLite storage.
func formatDurationAsInterval(duration time.Duration) string {
	hours := int(duration.Hours())
	remaining := duration - time.Duration(hours)*time.Hour
	minutes := int(remaining.Minutes())
	remaining -= time.Duration(minutes) * time.Minute
	seconds := int(remaining.Seconds())

	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

// Duration wraps time.Duration for database storage with cross-database compatibility.
// It stores durations in HH:MM:SS format for both SQLite and PostgreSQL.
type Duration time.Duration

// Value implements the driver.Valuer interface for database storage.
func (d Duration) Value() (driver.Value, error) {
	return formatDurationAsInterval(time.Duration(d)), nil
}

// Scan implements the sql.Scanner interface for database retrieval.
func (d *Duration) Scan(value any) error {
	if value == nil {
		*d = Duration(time.Minute) // default to 1 minute
		return nil
	}

	var str string
	switch v := value.(type) {
	case []byte:
		str = string(v)
	case string:
		str = v
	default:
		return fmt.Errorf("%w: %T", ErrInvalidScanType, value)
	}

	// Try parsing as ISO 8601 duration (PT1S, PT1M, PT90M)
	if duration, err := ParseISO8601Duration(str); err == nil {
		*d = Duration(duration)
		return nil
	}

	// Try parsing as PostgreSQL interval format (HH:MM:SS)
	if duration, err := parsePostgresInterval(str); err == nil {
		*d = Duration(duration)
		return nil
	}

	return fmt.Errorf("%w: %s", ErrInvalidDurationFormat, str)
}

// parsePostgresInterval parses PostgreSQL interval format (HH:MM:SS).
func parsePostgresInterval(s string) (time.Duration, error) {
	re := regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})(?:\.(\d+))?$`)
	matches := re.FindStringSubmatch(s)

	if matches == nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidPostgresInterval, s)
	}

	hours, _ := strconv.Atoi(matches[1])
	minutes, _ := strconv.Atoi(matches[2])
	seconds, _ := strconv.Atoi(matches[3])

	duration := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second

	return duration, nil
}

// FormatHumanReadable formats a duration in a human-readable short form.
// Examples: "1m", "5s", "1h", "2d", "< 1m".
func FormatHumanReadable(dur time.Duration) string {
	if dur < 0 {
		dur = -dur
	}

	days := int(dur.Hours() / 24)
	hours := int(dur.Hours()) % 24
	minutes := int(dur.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return "< 1m"
}

// FormatPeriod formats a period string (e.g., "00:01:00" or "1m") to a human-readable short form.
// It attempts to parse the input as a Go duration first, then as PostgreSQL interval format.
// Returns the original string if parsing fails.
func FormatPeriod(period string) string {
	// Try parsing as Go duration (e.g., "1m", "5s", "1h30m")
	if dur, err := time.ParseDuration(period); err == nil {
		return FormatHumanReadable(dur)
	}

	// Try parsing as PostgreSQL interval (e.g., "00:01:00")
	if dur, err := parsePostgresInterval(period); err == nil {
		return FormatHumanReadable(dur)
	}

	// Try parsing as ISO 8601 (e.g., "PT1M")
	if dur, err := ParseISO8601Duration(period); err == nil {
		return FormatHumanReadable(dur)
	}

	// Return as-is if all parsing attempts fail
	return period
}
