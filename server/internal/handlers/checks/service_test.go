package checks

import (
	"errors"
	"testing"

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
			input:    "webhooks.stonal.io",
			expected: "webhooks-stonal-io",
		},
		{
			name:     "long slug truncated to 20 chars",
			input:    "this-is-a-very-long-slug-that-exceeds-twenty",
			expected: "this-is-a-very-long",
		},
		{
			name:     "trailing hyphen after truncation removed",
			input:    "abcdefghijklmnopqrs-tuvwxyz",
			expected: "abcdefghijklmnopqrs",
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
			r.LessOrEqual(len(result), 20, "sanitized slug %q exceeds 20 chars", result)
		})
	}
}
