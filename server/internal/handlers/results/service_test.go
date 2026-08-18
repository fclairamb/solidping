package results

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
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
			expectedInts: []int{int(models.ResultStatusUp)},
		},
		{
			name:         "down status maps to multiple",
			statusStrs:   []string{"down"},
			expectedInts: []int{int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError)},
		},
		{
			name:         "unknown status not mapped",
			statusStrs:   []string{"unknown"},
			expectedInts: []int{},
		},
		{
			name:         "multiple statuses",
			statusStrs:   []string{"up", "created"},
			expectedInts: []int{int(models.ResultStatusUp), int(models.ResultStatusCreated)},
		},
		{
			name:       "case insensitive",
			statusStrs: []string{"UP", "Down"},
			expectedInts: []int{
				int(models.ResultStatusUp), int(models.ResultStatusDown),
				int(models.ResultStatusTimeout), int(models.ResultStatusError),
			},
		},
		{
			name:         "invalid status",
			statusStrs:   []string{"invalid"},
			expectedInts: []int{},
		},
		{
			name:       "mixed valid and invalid",
			statusStrs: []string{"up", "invalid", "down"},
			expectedInts: []int{
				int(models.ResultStatusUp), int(models.ResultStatusDown),
				int(models.ResultStatusTimeout), int(models.ResultStatusError),
			},
		},
		{
			name:         "empty slice",
			statusStrs:   []string{},
			expectedInts: []int{},
		},
		{
			name:         "running status",
			statusStrs:   []string{"running"},
			expectedInts: []int{int(models.ResultStatusRunning)},
		},
		{
			name:       "all status types",
			statusStrs: []string{"up", "down", "running", "created"},
			expectedInts: []int{
				int(models.ResultStatusUp), int(models.ResultStatusDown),
				int(models.ResultStatusTimeout), int(models.ResultStatusError),
				int(models.ResultStatusRunning), int(models.ResultStatusCreated),
			},
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
		{"status 1 (created)", intPtr(int(models.ResultStatusCreated)), "created"},
		{"status 2 (running)", intPtr(int(models.ResultStatusRunning)), "running"},
		{"status 3 (up)", intPtr(int(models.ResultStatusUp)), "up"},
		{"status 4 (down)", intPtr(int(models.ResultStatusDown)), "down"},
		{"status 5 (timeout)", intPtr(int(models.ResultStatusTimeout)), "down"},
		{"status 6 (error)", intPtr(int(models.ResultStatusError)), "down"},
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

// float32Ptr is a helper to create float32 pointers for testing.
func float32Ptr(f float32) *float32 {
	return &f
}

func float64Ptr(f float64) *float64 {
	return &f
}

func TestApplyDurationFields(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name           string
		result         *models.Result
		with           []string
		wantDurationMs *float32
		wantMinMs      *float32
		wantMaxMs      *float32
		wantAvgMs      *float32
		wantP95Ms      *float32
	}{
		{
			name: "aggregated row requesting avg and p95 returns both",
			result: &models.Result{
				DurationAvg: float32Ptr(123.4),
				DurationP95: float32Ptr(456.7),
			},
			with:      []string{"durationAvgMs", "durationP95Ms"},
			wantAvgMs: float32Ptr(123.4),
			wantP95Ms: float32Ptr(456.7),
		},
		{
			name: "raw row has nil DurationAvg/DurationP95 so fields stay absent even when requested",
			result: &models.Result{
				Duration:    float32Ptr(50),
				DurationAvg: nil,
				DurationP95: nil,
			},
			with:           []string{"durationMs", "durationAvgMs", "durationP95Ms"},
			wantDurationMs: float32Ptr(50),
			wantAvgMs:      nil,
			wantP95Ms:      nil,
		},
		{
			name: "aggregated row not requesting avg/p95 leaves them absent",
			result: &models.Result{
				DurationAvg: float32Ptr(123.4),
				DurationP95: float32Ptr(456.7),
			},
			with:      []string{},
			wantAvgMs: nil,
			wantP95Ms: nil,
		},
		{
			name: "existing durationMinMs/durationMaxMs tokens still work alongside new fields",
			result: &models.Result{
				DurationMin: float32Ptr(10),
				DurationMax: float32Ptr(999),
				DurationAvg: float32Ptr(200),
				DurationP95: float32Ptr(800),
			},
			with:      []string{"durationMinMs", "durationMaxMs", "durationAvgMs", "durationP95Ms"},
			wantMinMs: float32Ptr(10),
			wantMaxMs: float32Ptr(999),
			wantAvgMs: float32Ptr(200),
			wantP95Ms: float32Ptr(800),
		},
		{
			name: "case-insensitive with tokens",
			result: &models.Result{
				DurationAvg: float32Ptr(1),
				DurationP95: float32Ptr(2),
			},
			with:      []string{"DURATIONAVGMS", "DurationP95Ms"},
			wantAvgMs: float32Ptr(1),
			wantP95Ms: float32Ptr(2),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			resp := &ResultResponse{}
			withSet := buildWithSet(tc.with)
			s.applyDurationFields(resp, tc.result, withSet)

			r.Equal(tc.wantDurationMs, resp.DurationMs)
			r.Equal(tc.wantMinMs, resp.DurationMinMs)
			r.Equal(tc.wantMaxMs, resp.DurationMaxMs)
			r.Equal(tc.wantAvgMs, resp.DurationAvgMs)
			r.Equal(tc.wantP95Ms, resp.DurationP95Ms)
		})
	}
}

func TestConvertResultToResponse_DurationAvgP95(t *testing.T) {
	t.Parallel()

	s := &Service{}
	r := require.New(t)

	aggregated := &models.Result{
		UID:         "agg-1",
		CheckUID:    "check-1",
		PeriodType:  "hour",
		PeriodStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		DurationAvg: float32Ptr(111),
		DurationP95: float32Ptr(222),
	}
	respAgg := s.convertResultToResponse(aggregated, []string{"durationAvgMs", "durationP95Ms"})
	r.Equal(float32Ptr(111), respAgg.DurationAvgMs)
	r.Equal(float32Ptr(222), respAgg.DurationP95Ms)

	raw := &models.Result{
		UID:         "raw-1",
		CheckUID:    "check-1",
		PeriodType:  "raw",
		PeriodStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Duration:    float32Ptr(42),
		DurationAvg: nil,
		DurationP95: nil,
	}
	respRaw := s.convertResultToResponse(raw, []string{"durationAvgMs", "durationP95Ms"})
	r.Nil(respRaw.DurationAvgMs)
	r.Nil(respRaw.DurationP95Ms)

	respNotRequested := s.convertResultToResponse(aggregated, []string{})
	r.Nil(respNotRequested.DurationAvgMs)
	r.Nil(respNotRequested.DurationP95Ms)
}

// TestApplyAggregationFields_DerivedAvailabilityPct pins the read-time
// derivation that replaced the dropped results.availability_pct column (spec
// 2026-07-24-02 §4): availabilityPct is successfulChecks / totalChecks × 100,
// and is absent (JSON null / omitted) whenever totalChecks is 0 or NULL —
// never 0%, which would read as "totally down".
func TestApplyAggregationFields_DerivedAvailabilityPct(t *testing.T) {
	t.Parallel()

	s := &Service{}

	tests := []struct {
		name        string
		result      *models.Result
		with        []string
		wantPct     *float64
		wantTotal   *int
		wantSuccess *int
	}{
		{
			name: "derives from the two counts",
			result: &models.Result{
				TotalChecks: intPtr(200), SuccessfulChecks: intPtr(199),
			},
			with:        []string{"availabilityPct", "totalChecks", "successfulChecks"},
			wantPct:     float64Ptr(99.5),
			wantTotal:   intPtr(200),
			wantSuccess: intPtr(199),
		},
		{
			name: "a perfect bucket is 100",
			result: &models.Result{
				TotalChecks: intPtr(60), SuccessfulChecks: intPtr(60),
			},
			with:    []string{"availabilityPct"},
			wantPct: float64Ptr(100),
		},
		{
			name: "a fully failed bucket is 0, not null",
			result: &models.Result{
				TotalChecks: intPtr(12), SuccessfulChecks: intPtr(0),
			},
			with:    []string{"availabilityPct"},
			wantPct: float64Ptr(0),
		},
		{
			name: "totalChecks == 0 yields null, not a division by zero",
			result: &models.Result{
				TotalChecks: intPtr(0), SuccessfulChecks: intPtr(0),
			},
			with:    []string{"availabilityPct"},
			wantPct: nil,
		},
		{
			name:    "a raw row (nil counts) yields null",
			result:  &models.Result{TotalChecks: nil, SuccessfulChecks: nil},
			with:    []string{"availabilityPct"},
			wantPct: nil,
		},
		{
			name: "a nil successfulChecks with a positive total counts as zero successes",
			result: &models.Result{
				TotalChecks: intPtr(4), SuccessfulChecks: nil,
			},
			with:    []string{"availabilityPct"},
			wantPct: float64Ptr(0),
		},
		{
			name: "not requested stays absent",
			result: &models.Result{
				TotalChecks: intPtr(10), SuccessfulChecks: intPtr(10),
			},
			with:    []string{},
			wantPct: nil,
		},
		{
			name: "case-insensitive with token",
			result: &models.Result{
				TotalChecks: intPtr(10), SuccessfulChecks: intPtr(5),
			},
			with:    []string{"AVAILABILITYPCT"},
			wantPct: float64Ptr(50),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			resp := &ResultResponse{}
			s.applyAggregationFields(resp, tc.result, buildWithSet(tc.with))

			if tc.wantPct == nil {
				r.Nil(resp.AvailabilityPct)
			} else {
				r.NotNil(resp.AvailabilityPct)
				r.InDelta(*tc.wantPct, *resp.AvailabilityPct, 0.0001)
			}

			r.Equal(tc.wantTotal, resp.TotalChecks)
			r.Equal(tc.wantSuccess, resp.SuccessfulChecks)
		})
	}
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

// TestConvertResultToResponse_CheckSlugCheckName pins the mapping half of the
// with=checkSlug,checkName fix (spec 2026-08-18-07): once models.Result
// carries the joined CheckSlug/CheckName (populated by the DB layer only when
// ListResultsFilter.IncludeCheckInfo joined checks), convertResultToResponse
// must copy them onto the response when requested, and never otherwise — the
// negative cases are the positive control that the assertions above aren't
// vacuous.
func TestConvertResultToResponse_CheckSlugCheckName(t *testing.T) {
	t.Parallel()

	s := &Service{}
	r := require.New(t)

	slug := "acme-http"
	name := "acme HTTP"
	withCheckInfo := &models.Result{
		UID:         "result-1",
		CheckUID:    "check-1",
		PeriodType:  "raw",
		PeriodStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		CheckSlug:   &slug,
		CheckName:   &name,
	}

	// Positive: requested and present on the model → populated on the response.
	resp := s.convertResultToResponse(withCheckInfo, []string{"checkSlug", "checkName"})
	r.Equal(&slug, resp.CheckSlug)
	r.Equal(&name, resp.CheckName)

	// Negative control 1: same model, but not requested via `with` → omitted.
	respNotRequested := s.convertResultToResponse(withCheckInfo, []string{"region"})
	r.Nil(respNotRequested.CheckSlug)
	r.Nil(respNotRequested.CheckName)

	// Negative control 2: requested, but the model never carries the joined
	// columns (e.g. GetResult's direct-by-UID lookup, or an orphaned
	// check_uid behind the LEFT JOIN) → still nil, never a stale/zero value.
	withoutCheckInfo := &models.Result{
		UID:         "result-2",
		CheckUID:    "check-2",
		PeriodType:  "raw",
		PeriodStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	respOrphan := s.convertResultToResponse(withoutCheckInfo, []string{"checkSlug", "checkName"})
	r.Nil(respOrphan.CheckSlug)
	r.Nil(respOrphan.CheckName)
}
