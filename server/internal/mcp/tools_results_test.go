package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFinestPeriodType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tests := []struct {
		name   string
		in     []string
		expect string
	}{
		{"empty defaults to hour", []string{}, periodTypeHour},
		{"single raw", []string{periodTypeRaw}, periodTypeRaw},
		{"single hour", []string{periodTypeHour}, periodTypeHour},
		{"raw and hour → raw", []string{periodTypeRaw, periodTypeHour}, periodTypeRaw},
		{"day and hour → hour", []string{periodTypeDay, periodTypeHour}, periodTypeHour},
		{"unknown ignored", []string{"junk"}, periodTypeHour},
		{"unknown alongside raw → raw", []string{"junk", periodTypeRaw}, periodTypeRaw},
		{"month only", []string{periodTypeMonth}, periodTypeMonth},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r.Equal(tc.expect, finestPeriodType(tc.in))
		})
	}
}

func TestDefaultWindowFor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(rawDefaultWindow, defaultWindowFor(periodTypeRaw))
	r.Equal(hourlyDefaultWindow, defaultWindowFor(periodTypeHour))
	r.Equal(dailyDefaultWindow, defaultWindowFor(periodTypeDay))
	r.Equal(monthlyDefaultWindow, defaultWindowFor(periodTypeMonth))
	r.Equal(hourlyDefaultWindow, defaultWindowFor("unknown")) // safe fallback
}

func TestBuildListResultsOptions_DefaultsAppliedWhenAbsent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	opts := buildListResultsOptions(map[string]any{}, now)

	r.Equal([]string{periodTypeHour}, opts.PeriodTypes)
	r.NotNil(opts.PeriodStartAfter)
	r.Equal(now.Add(-hourlyDefaultWindow), *opts.PeriodStartAfter)
	r.Nil(opts.PeriodEndBefore)
	r.Equal(defaultListResultsSize, opts.Size)
}

func TestBuildListResultsOptions_RawNarrowsWindowTo1h(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	opts := buildListResultsOptions(map[string]any{"periodType": "raw"}, now)

	r.Equal([]string{periodTypeRaw}, opts.PeriodTypes)
	r.Equal(now.Add(-rawDefaultWindow), *opts.PeriodStartAfter)
}

func TestBuildListResultsOptions_RawHourMixUsesFinest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	opts := buildListResultsOptions(map[string]any{"periodType": "raw,hour"}, now)

	r.Equal([]string{periodTypeRaw, periodTypeHour}, opts.PeriodTypes)
	r.Equal(now.Add(-rawDefaultWindow), *opts.PeriodStartAfter)
}

func TestBuildListResultsOptions_ExplicitValuesNotOverridden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	explicit := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	opts := buildListResultsOptions(map[string]any{
		"periodType":       "day",
		"periodStartAfter": explicit.Format(time.RFC3339),
	}, now)

	r.Equal([]string{periodTypeDay}, opts.PeriodTypes)
	r.NotNil(opts.PeriodStartAfter)
	r.Equal(explicit, *opts.PeriodStartAfter)
}

func TestBuildListResultsOptions_PeriodEndBeforeAloneStillDefaultsStart(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	end := time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	opts := buildListResultsOptions(map[string]any{
		"periodEndBefore": end.Format(time.RFC3339),
	}, now)

	r.NotNil(opts.PeriodStartAfter)
	r.Equal(now.Add(-hourlyDefaultWindow), *opts.PeriodStartAfter)
	r.NotNil(opts.PeriodEndBefore)
	r.Equal(end, *opts.PeriodEndBefore)
}

func TestBuildListResultsOptions_SizeClamps(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()

	opts := buildListResultsOptions(map[string]any{"size": float64(0)}, now)
	r.Equal(1, opts.Size)

	opts = buildListResultsOptions(map[string]any{"size": float64(99999)}, now)
	r.Equal(maxListResultsSize, opts.Size)

	opts = buildListResultsOptions(map[string]any{"size": float64(50)}, now)
	r.Equal(50, opts.Size)
}

func TestListResultsResponseWithFilter_AlwaysIncludesEffectiveFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC)
	wrapped := ListResultsResponseWithFilter{
		ListResultsResponse: nil,
		EffectiveFilter: EffectiveResultsFilter{
			PeriodType:       []string{periodTypeHour},
			PeriodStartAfter: &start,
		},
	}
	raw, err := json.Marshal(wrapped)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))

	filter, ok := decoded["effectiveFilter"].(map[string]any)
	r.True(ok)
	r.Contains(filter, "periodType")
	r.Contains(filter, "periodStartAfter")
}
