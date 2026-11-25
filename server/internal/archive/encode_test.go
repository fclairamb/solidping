package archive_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/archive"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func ptr[T any](v T) *T { return &v }

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	raw := &models.Result{
		UID: "b-raw", CheckUID: "check-1", PeriodType: models.PeriodTypeRaw,
		PeriodStart: start.Add(2 * time.Minute), Region: ptr("eu-west"),
		WorkerUID: ptr("worker-1"), Status: ptr(int(models.ResultStatusDown)), Duration: ptr(float32(12.5)),
		Metrics:     models.JSONMap{"ttfb": 3.5},
		Output:      models.JSONMap{"error": "connection refused", "nested": map[string]any{"code": 7.0}},
		Maintenance: true,
		CreatedAt:   start.Add(2*time.Minute + time.Second),
	}
	// A rollup row with every raw field null and the counters set.
	rollup := &models.Result{
		UID: "a-rollup", CheckUID: "check-1", PeriodType: models.PeriodTypeHour,
		PeriodStart: start, PeriodEnd: ptr(start.Add(time.Hour - time.Millisecond)),
		Status: ptr(int(models.ResultStatusUp)), TotalChecks: ptr(60), SuccessfulChecks: ptr(59),
		MaintenanceChecks: ptr(0), MaintenanceSuccessfulChecks: ptr(0),
		DurationMin: ptr(float32(1)), DurationMax: ptr(float32(9)), DurationP95: ptr(float32(8)),
		DurationAvg: ptr(float32(4)), CreatedAt: start.Add(time.Hour),
	}

	rows := make([]archive.Row, 0, 2)

	for _, res := range []*models.Result{raw, rollup} {
		row, err := archive.RowFromResult(res)
		r.NoError(err)

		rows = append(rows, row)
	}

	data, err := archive.Encode(rows)
	r.NoError(err)
	r.NotEmpty(data)

	decoded, err := archive.Decode(t.Context(), data)
	r.NoError(err)
	r.Len(decoded, 2)

	// Sorted by period_start: the rollup (10:00) before the raw row (10:02).
	r.Equal("a-rollup", decoded[0].UID)
	r.Equal("b-raw", decoded[1].UID)

	got := decoded[1]
	r.Equal("check-1", got.CheckUID)
	r.Equal(models.PeriodTypeRaw, got.PeriodType)
	r.True(got.PeriodStart.Equal(raw.PeriodStart))
	r.False(got.PeriodEnd.Valid)
	r.Equal("eu-west", got.Region.V)
	r.Equal("worker-1", got.WorkerUID.V)
	r.Equal(int64(models.ResultStatusDown), got.Status.V)
	r.InDelta(12.5, got.Duration.V, 0.0001)
	r.True(got.Maintenance)
	r.False(got.TotalChecks.Valid)
	r.True(got.CreatedAt.Equal(raw.CreatedAt))

	var output map[string]any
	r.True(got.Output.Valid)
	r.NoError(json.Unmarshal([]byte(got.Output.V), &output))
	r.Equal("connection refused", output["error"])
	r.Equal(map[string]any{"code": 7.0}, output["nested"])

	agg := decoded[0]
	r.False(agg.Region.Valid)
	r.False(agg.WorkerUID.Valid)
	r.False(agg.Duration.Valid)
	r.False(agg.Metrics.Valid)
	r.False(agg.Output.Valid)
	r.False(agg.DurationP50.Valid)
	r.True(agg.PeriodEnd.V.Equal(*rollup.PeriodEnd))
	r.Equal(int64(60), agg.TotalChecks.V)
	r.Equal(int64(59), agg.SuccessfulChecks.V)
	r.InDelta(8, agg.DurationP95.V, 0.0001)
}

func TestMergeKeepsUnionByUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	existing := []archive.Row{
		{UID: "1", PeriodStart: base},
		{UID: "2", PeriodStart: base.Add(time.Minute)},
	}
	incoming := []archive.Row{
		{UID: "2", PeriodStart: base.Add(time.Minute), Maintenance: true},
		{UID: "0", PeriodStart: base.Add(-time.Minute)},
	}

	merged := archive.Merge(existing, incoming)
	r.Len(merged, 3)
	r.Equal([]string{"0", "1", "2"}, []string{merged[0].UID, merged[1].UID, merged[2].UID})
	r.True(merged[2].Maintenance, "the incoming row replaces the stored one")
}
