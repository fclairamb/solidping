package results

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeCursor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := &Service{}

	timestamp := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	uid := "test-uid-123"

	cursor := s.encodeCursor(timestamp, uid)

	// Decode to verify format
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	r.NoError(err)

	decodedStr := string(decoded)
	parts := strings.SplitN(decodedStr, "|", 2)

	r.Len(parts, 2, "cursor should have 2 parts")
	r.Equal(uid, parts[1])

	// Verify timestamp is in RFC3339Nano format
	parsedTime, err := time.Parse(time.RFC3339Nano, parts[0])
	r.NoError(err)
	r.True(parsedTime.Equal(timestamp))
}

func TestDecodeCursor(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name        string
		cursor      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid cursor",
			cursor:  base64.URLEncoding.EncodeToString([]byte("2024-01-15T10:30:00Z|test-uid")),
			wantErr: false,
		},
		{
			name:        "invalid base64",
			cursor:      "not-valid-base64!!!",
			wantErr:     true,
			errContains: "illegal base64",
		},
		{
			name:        "invalid format - no separator",
			cursor:      base64.URLEncoding.EncodeToString([]byte("invalid-format")),
			wantErr:     true,
			errContains: "invalid cursor format",
		},
		{
			name:        "invalid format - empty timestamp",
			cursor:      base64.URLEncoding.EncodeToString([]byte("|test-uid")),
			wantErr:     true,
			errContains: "invalid cursor format",
		},
		{
			name:        "invalid format - empty uid",
			cursor:      base64.URLEncoding.EncodeToString([]byte("2024-01-15T10:30:00Z|")),
			wantErr:     true,
			errContains: "invalid cursor format",
		},
		{
			name:        "invalid timestamp format",
			cursor:      base64.URLEncoding.EncodeToString([]byte("not-a-timestamp|test-uid")),
			wantErr:     true,
			errContains: "parsing time",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			timestamp, uid, err := s.decodeCursor(tc.cursor)

			if tc.wantErr {
				r.Error(err)
				if tc.errContains != "" {
					r.Contains(err.Error(), tc.errContains)
				}
			} else {
				r.NoError(err)
				r.False(timestamp.IsZero())
				r.NotEmpty(uid)
			}
		})
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := &Service{}

	originalTime := time.Date(2024, 6, 15, 14, 30, 45, 123456789, time.UTC)
	originalUID := "550e8400-e29b-41d4-a716-446655440000"

	// Encode
	cursor := s.encodeCursor(originalTime, originalUID)

	// Decode
	decodedTime, decodedUID, err := s.decodeCursor(cursor)
	r.NoError(err)

	// Verify round trip
	r.True(decodedTime.Equal(originalTime))
	r.Equal(originalUID, decodedUID)
}

func TestMapStatusStringsToInts(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name         string
		statusStrs   []string
		expectedInts []int
	}{
		{
			name:         "up status",
			statusStrs:   []string{"up"},
			expectedInts: []int{1},
		},
		{
			name:         "down status maps to multiple",
			statusStrs:   []string{"down"},
			expectedInts: []int{2, 3, 4},
		},
		{
			name:         "unknown status",
			statusStrs:   []string{"unknown"},
			expectedInts: []int{0},
		},
		{
			name:         "multiple statuses",
			statusStrs:   []string{"up", "unknown"},
			expectedInts: []int{1, 0},
		},
		{
			name:         "case insensitive",
			statusStrs:   []string{"UP", "Down", "UnKnOwN"},
			expectedInts: []int{1, 2, 3, 4, 0},
		},
		{
			name:         "invalid status",
			statusStrs:   []string{"invalid"},
			expectedInts: []int{},
		},
		{
			name:         "mixed valid and invalid",
			statusStrs:   []string{"up", "invalid", "down"},
			expectedInts: []int{1, 2, 3, 4},
		},
		{
			name:         "empty slice",
			statusStrs:   []string{},
			expectedInts: []int{},
		},
		{
			name:         "running status",
			statusStrs:   []string{"running"},
			expectedInts: []int{5},
		},
		{
			name:         "all status types",
			statusStrs:   []string{"up", "down", "unknown", "running"},
			expectedInts: []int{1, 2, 3, 4, 0, 5},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := s.mapStatusStringsToInts(tc.statusStrs)

			r.Len(result, len(tc.expectedInts))

			// Convert to map for easier comparison (order doesn't matter)
			expectedMap := make(map[int]bool)
			for _, code := range tc.expectedInts {
				expectedMap[code] = true
			}

			resultMap := make(map[int]bool)
			for _, code := range result {
				resultMap[code] = true
			}

			for code := range expectedMap {
				r.Contains(resultMap, code)
			}

			for code := range resultMap {
				r.Contains(expectedMap, code)
			}
		})
	}
}

func TestStatusIntToString(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name      string
		statusInt *int
		expected  string
	}{
		{"nil status", nil, "unknown"},
		{"status 0", intPtr(0), "unknown"},
		{"status 1 (up)", intPtr(1), "up"},
		{"status 2 (down)", intPtr(2), "down"},
		{"status 3 (timeout)", intPtr(3), "down"},
		{"status 4 (error)", intPtr(4), "down"},
		{"status 5 (running)", intPtr(5), "running"},
		{"invalid status -1", intPtr(-1), "unknown"},
		{"invalid status 99", intPtr(99), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := s.statusIntToString(tc.statusInt)
			r.Equal(tc.expected, result)
		})
	}
}

// intPtr is a helper to create int pointers for testing.
func intPtr(i int) *int {
	return &i
}

func TestNeedsCheckInfo(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name     string
		withOpts []string
		expected bool
	}{
		{
			name:     "empty options",
			withOpts: []string{},
			expected: false,
		},
		{
			name:     "checkName only",
			withOpts: []string{"checkName"},
			expected: true,
		},
		{
			name:     "checkSlug only",
			withOpts: []string{"checkSlug"},
			expected: true,
		},
		{
			name:     "both checkName and checkSlug",
			withOpts: []string{"checkName", "checkSlug"},
			expected: true,
		},
		{
			name:     "other options without check info",
			withOpts: []string{"region", "output", "metrics"},
			expected: false,
		},
		{
			name:     "mixed with check info",
			withOpts: []string{"region", "checkName", "output"},
			expected: true,
		},
		{
			name:     "case insensitive checkname",
			withOpts: []string{"CHECKNAME"},
			expected: true,
		},
		{
			name:     "case insensitive checkslug",
			withOpts: []string{"CheckSlug"},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := s.needsCheckInfo(tc.withOpts)
			r.Equal(tc.expected, result)
		})
	}
}
