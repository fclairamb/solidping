package checks

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIsUUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid UUID v4",
			input:    "550e8400-e29b-41d4-a716-446655440000",
			expected: true,
		},
		{
			name:     "valid UUID lowercase",
			input:    "123e4567-e89b-12d3-a456-426614174000",
			expected: true,
		},
		{
			name:     "valid UUID uppercase",
			input:    "550E8400-E29B-41D4-A716-446655440000",
			expected: true,
		},
		{
			name:     "slug with hyphens",
			input:    "website-uptime",
			expected: false,
		},
		{
			name:     "slug without hyphens",
			input:    "apihealth",
			expected: false,
		},
		{
			name:     "not a UUID",
			input:    "not-a-uuid",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "UUID-like but invalid",
			input:    "550e8400-e29b-41d4-a716-44665544000",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := isUUID(tt.input)
			if result != tt.expected {
				t.Errorf("isUUID(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidateSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		slug      string
		shouldErr bool
	}{
		{
			name:      "valid slug",
			slug:      "website-uptime",
			shouldErr: false,
		},
		{
			name:      "valid slug with numbers",
			slug:      "api-health-check-123",
			shouldErr: false,
		},
		{
			name:      "empty slug (allowed)",
			slug:      "",
			shouldErr: false,
		},
		{
			name:      "UUID format slug",
			slug:      "550e8400-e29b-41d4-a716-446655440000",
			shouldErr: true,
		},
		{
			name:      "uppercase UUID",
			slug:      "550E8400-E29B-41D4-A716-446655440000",
			shouldErr: true,
		},
		{
			name:      "another valid UUID",
			slug:      uuid.New().String(),
			shouldErr: true,
		},
		{
			name:      "short slug (2 chars - too short)",
			slug:      "db",
			shouldErr: true,
		},
		{
			name:      "minimum valid slug (3 chars)",
			slug:      "abc",
			shouldErr: false,
		},
		{
			name:      "max-length valid slug (50 chars)",
			slug:      "a" + strings.Repeat("b", 49),
			shouldErr: false,
		},
		{
			name:      "max-length valid slug (100 chars)",
			slug:      "a" + strings.Repeat("b", 99),
			shouldErr: false,
		},
		{
			name:      "too long (101 chars)",
			slug:      "a" + strings.Repeat("b", 100),
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSlug(tt.slug)
			if (err != nil) != tt.shouldErr {
				t.Errorf("validateSlug(%q) error = %v, shouldErr %v", tt.slug, err, tt.shouldErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidSlugFormat) {
				t.Errorf("validateSlug(%q) returned unexpected error: %v", tt.slug, err)
			}
		})
	}
}

func TestSanitizeSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple lowercase",
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "uppercase converted",
			input:    "Hello-World",
			expected: "hello-world",
		},
		{
			name:     "dots replaced with hyphens",
			input:    "webhooks.acme.io",
			expected: "webhooks-acme-io",
		},
		{
			name:     "long slug truncated to 50 chars",
			input:    "this-is-a-very-long-slug-that-exceeds-fifty-characters-by-quite-a-bit",
			expected: "this-is-a-very-long-slug-that-exceeds-fifty-charac",
		},
		{
			name:     "trailing hyphen after truncation removed",
			input:    "aaaaaaaaaa-bbbbbbbbbb-cccccccccc-dddddddddd-eeeeee-zzz",
			expected: "aaaaaaaaaa-bbbbbbbbbb-cccccccccc-dddddddddd-eeeeee",
		},
		{
			name:     "starts with digit gets x prefix",
			input:    "123abc",
			expected: "x123abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := sanitizeSlug(tt.input)
			r.Equal(tt.expected, result)
			r.LessOrEqual(len(result), 50, "sanitized slug %q exceeds 50 chars", result)
		})
	}
}

// TestValidatePeriodForType covers the per-type period bounds (spec
// 2026-07-01-04 D1): registry MinPeriod for heavy types, the global 10s floor
// for types without one, and the internal / sleep / absent-period exemptions.
func TestValidatePeriodForType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		checkType string
		period    time.Duration
		internal  bool
		wantErr   string // empty = accepted
	}{
		{
			name: "browser below its 60s floor", checkType: "browser", period: 10 * time.Second,
			wantErr: "period for browser checks must be at least 60s",
		},
		{name: "browser at its 60s floor", checkType: "browser", period: time.Minute},
		{
			name: "js below its 30s floor", checkType: "js", period: 10 * time.Second,
			wantErr: "period for js checks must be at least 30s",
		},
		{name: "js at its 30s floor", checkType: "js", period: 30 * time.Second},
		{
			name: "http below the global 10s floor", checkType: "http", period: 5 * time.Second,
			wantErr: "period for http checks must be at least 10s",
		},
		{name: "http at the global 10s floor", checkType: "http", period: 10 * time.Second},
		{
			name: "ssl below its 1h floor", checkType: "ssl", period: 10 * time.Minute,
			wantErr: "period for ssl checks must be at least 1h",
		},
		{
			name: "dnsbl below its 15m floor", checkType: "dnsbl", period: time.Minute,
			wantErr: "period for dnsbl checks must be at least 15m",
		},
		{name: "sleep is exempt", checkType: "sleep", period: time.Second},
		{
			// domain has MinPeriod 6h and no MaxPeriod (0 = uncapped) — a
			// 2-week period must pass, proving long periods survive
			// validation for types the registry allows.
			name:      "domain accepts a 2-week period (uncapped MaxPeriod)",
			checkType: "domain", period: 14 * 24 * time.Hour,
		},
		{
			name: "domain below its 6h floor", checkType: "domain", period: time.Hour,
			wantErr: "period for domain checks must be at least 6h",
		},
		{name: "internal checks are exempt", checkType: "browser", period: time.Second, internal: true},
		{name: "absent period (0) passes — default applies", checkType: "browser", period: 0},
		{
			name: "unknown type falls back to the global floor", checkType: "no-such-type", period: 5 * time.Second,
			wantErr: "period for no-such-type checks must be at least 10s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := validatePeriodForType(tt.checkType, tt.period, tt.internal)
			if tt.wantErr == "" {
				r.NoError(err)
				return
			}
			r.Error(err)
			r.Equal(tt.wantErr, err.Error())

			var periodErr *periodBoundError
			r.ErrorAs(err, &periodErr, "period bound violations must be periodBoundError for the 400 mapping")
			r.True(isCheckFieldValidationError(err), "must surface as a 400 VALIDATION_ERROR")
		})
	}
}

// TestPeriodBoundErrorMaxMessage pins the "at most" direction of the message
// (no registry type declares a MaxPeriod today, so the branch is exercised
// directly).
func TestPeriodBoundErrorMaxMessage(t *testing.T) {
	t.Parallel()

	err := &periodBoundError{CheckType: "http", Bound: 6 * time.Hour, TooLong: true}
	require.Equal(t, "period for http checks must be at most 6h", err.Error())
}

// TestFormatPeriodBound pins the compact rendering: whole hours as "6h",
// whole minutes >= 10m as "15m", everything else in seconds so the common
// floors read exactly as the spec states them ("60s", "30s", "10s").
func TestFormatPeriodBound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("10s", formatPeriodBound(10*time.Second))
	r.Equal("30s", formatPeriodBound(30*time.Second))
	r.Equal("60s", formatPeriodBound(time.Minute))
	r.Equal("300s", formatPeriodBound(5*time.Minute))
	r.Equal("15m", formatPeriodBound(15*time.Minute))
	r.Equal("90s", formatPeriodBound(90*time.Second))
	r.Equal("1h", formatPeriodBound(time.Hour))
	r.Equal("6h", formatPeriodBound(6*time.Hour))
	r.Equal("90m", formatPeriodBound(90*time.Minute))
}
