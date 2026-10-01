package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

func TestAggregationJobDefinition(t *testing.T) {
	t.Parallel()

	def := &AggregationJobDefinition{}
	require.Equal(t, jobdef.JobTypeAggregation, def.Type())

	tests := []struct {
		name    string
		config  json.RawMessage
		wantErr bool
	}{
		{name: "nil config", config: nil},
		{name: "empty object", config: json.RawMessage(`{}`)},
		{name: "invalid json", config: json.RawMessage(`{`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run, err := def.CreateJobRun(tt.config)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.IsType(t, &AggregationJobRun{}, run)
		})
	}
}

func TestToFloat64AndToInt64(t *testing.T) {
	t.Parallel()

	numerics := []any{
		float64(7), float32(7), int(7), int8(7), int16(7), int32(7), int64(7),
		uint(7), uint8(7), uint16(7), uint32(7), uint64(7),
	}

	for _, v := range numerics {
		t.Run("float64 from numeric", func(t *testing.T) {
			t.Parallel()

			got := toFloat64(v)
			require.NotNil(t, got)
			require.InDelta(t, 7, *got, 0)
		})

		t.Run("int64 from numeric", func(t *testing.T) {
			t.Parallel()

			got := toInt64(v)
			require.NotNil(t, got)
			require.Equal(t, int64(7), *got)
		})
	}

	require.Nil(t, toFloat64("7"))
	require.Nil(t, toFloat64(nil))
	require.Nil(t, toInt64("7"))
	require.Nil(t, toInt64(uint64(math.MaxUint64)), "an overflowing uint64 is rejected, not wrapped")
}

func TestAggregateMetricValuesFallbacks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(aggregateMetricValues(nil, metricAggSum))
	r.Equal("b", aggregateMetricValues([]any{"a", "b"}, metricAggDefault), "unknown types keep the last value")
	r.Equal("b", aggregateMetricValues([]any{"a", "b"}, metricAggregationType(99)))

	// Non-numeric inputs yield nil for min/max/avg and zero for sum/count.
	junk := []any{"x", nil}
	r.Nil(aggregateMetricValues(junk, metricAggMin))
	r.Nil(aggregateMetricValues(junk, metricAggMax))
	r.Nil(aggregateMetricValues(junk, metricAggAvg))
	r.InDelta(0, aggregateMetricValues(junk, metricAggSum), 0)
	r.Equal(int64(0), aggregateMetricValues(junk, metricAggCnt))
	r.Equal(map[string]int64{"x": 1}, aggregateMetricValues(junk, metricAggVal))
}

func TestParameterInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  models.JSONMap
		want   int
		wantOK bool
	}{
		{"missing value key", models.JSONMap{}, 0, false},
		{"float64", models.JSONMap{"value": float64(5)}, 5, true},
		{"int", models.JSONMap{"value": 6}, 6, true},
		{"int64", models.JSONMap{"value": int64(7)}, 7, true},
		{"numeric string", models.JSONMap{"value": " 8 "}, 8, true},
		{"bad string", models.JSONMap{"value": "eight"}, 0, false},
		{"unsupported type", models.JSONMap{"value": true}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parameterInt(&models.Parameter{Value: tt.value})
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAggregationLoggerFallback(t *testing.T) {
	t.Parallel()

	custom := slog.Default().With("k", "v")

	require.Equal(t, custom, aggregationLogger(&jobdef.JobContext{Logger: custom}))
	require.NotNil(t, aggregationLogger(nil))
	require.NotNil(t, aggregationLogger(&jobdef.JobContext{}))
}

func TestRetentionFromDBParam(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jctx := &jobdef.JobContext{DBService: dbSvc}
	log := slog.Default()

	// Absent, nil context and nil DB service all fall through.
	_, ok := retentionFromDBParam(ctx, jctx, systemconfig.KeyPerfAggRetentionRawHours, log)
	require.False(t, ok)
	_, ok = retentionFromDBParam(ctx, nil, systemconfig.KeyPerfAggRetentionRawHours, log)
	require.False(t, ok)
	_, ok = retentionFromDBParam(ctx, &jobdef.JobContext{}, systemconfig.KeyPerfAggRetentionRawHours, log)
	require.False(t, ok)

	tests := []struct {
		name   string
		key    systemconfig.ParameterKey
		value  any
		want   int
		wantOK bool
	}{
		{"valid", systemconfig.KeyPerfAggRetentionRawHours, 48, 48, true},
		{"zero is rejected", systemconfig.KeyPerfAggRetentionHourDays, 0, 0, false},
		{"garbage is rejected", systemconfig.KeyPerfAggRetentionDayMonths, "nope", 0, false},
	}

	for _, tt := range tests {
		require.NoError(t, dbSvc.SetSystemParameter(ctx, string(tt.key), tt.value, false), tt.name)

		got, ok := retentionFromDBParam(ctx, jctx, tt.key, log)
		require.Equal(t, tt.wantOK, ok, tt.name)
		require.Equal(t, tt.want, got, tt.name)
	}

	// The same layers drive the full resolver: the DB value wins over legacy/default.
	raw, hour, day := retentionFromConfig(ctx, jctx)
	require.Equal(t, 48, raw)
	require.Positive(t, hour)
	require.Positive(t, day)

	r, h, d := retentionFromConfig(ctx, nil)
	require.Positive(t, r)
	require.Positive(t, h)
	require.Positive(t, d)
}

//nolint:paralleltest // t.Setenv cannot be combined with t.Parallel
func TestRetentionFromEnv(t *testing.T) {
	const envVar = "SP_TEST_COVER_RETENTION"

	tests := []struct {
		name   string
		value  string
		want   int
		wantOK bool
	}{
		{"unset", "", 0, false},
		{"valid", " 12 ", 12, true},
		{"zero", "0", 0, false},
		{"negative", "-3", 0, false},
		{"not a number", "abc", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envVar, tt.value)

			got, ok := retentionFromEnv(context.Background(), envVar, slog.Default())
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

//nolint:paralleltest // t.Setenv cannot be combined with t.Parallel
func TestResolveRetentionTierPrecedence(t *testing.T) {
	ctx := context.Background()
	key := systemconfig.KeyPerfAggRetentionRawHours
	const envVar = "SP_TEST_COVER_TIER"

	t.Setenv(envVar, "")
	require.Equal(t, 24, resolveRetentionTier(ctx, nil, key, envVar, 0, 24), "default")
	require.Equal(t, 5, resolveRetentionTier(ctx, nil, key, envVar, 5, 24), "legacy beats default")

	t.Setenv(envVar, "9")
	require.Equal(t, 9, resolveRetentionTier(ctx, nil, key, envVar, 5, 24), "env beats everything")
}
