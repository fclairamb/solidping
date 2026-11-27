package archive_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	vortex "github.com/fclairamb/vortex-go"
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
	// A raw row with every nullable field null.
	bare := &models.Result{
		UID: "a-bare", CheckUID: "check-1", PeriodType: models.PeriodTypeRaw,
		PeriodStart: start, CreatedAt: start,
	}

	rows := make([]archive.Row, 0, 2)

	for _, res := range []*models.Result{raw, bare} {
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

	// Sorted by period_start: the bare row (10:00) before the full one (10:02).
	r.Equal("a-bare", decoded[0].UID)
	r.Equal("b-raw", decoded[1].UID)

	got := decoded[1]
	r.Equal("check-1", got.CheckUID)
	r.Equal(models.PeriodTypeRaw, got.PeriodType)
	r.True(got.PeriodStart.Equal(raw.PeriodStart))
	r.Equal("eu-west", got.Region.V)
	r.Equal("worker-1", got.WorkerUID.V)
	r.Equal(int64(models.ResultStatusDown), got.Status.V)
	r.InDelta(12.5, got.Duration.V, 0.0001)
	r.True(got.Maintenance)
	r.True(got.CreatedAt.Equal(raw.CreatedAt))

	var output map[string]any
	r.True(got.Output.Valid)
	r.NoError(json.Unmarshal([]byte(got.Output.V), &output))
	r.Equal("connection refused", output["error"])
	r.Equal(map[string]any{"code": 7.0}, output["nested"])

	empty := decoded[0]
	r.False(empty.Region.Valid)
	r.False(empty.WorkerUID.Valid)
	r.False(empty.Status.Valid)
	r.False(empty.Duration.Valid)
	r.False(empty.Metrics.Valid)
	r.False(empty.Output.Valid)
}

// legacyRow is the schema of the files written by v0.55/v0.56: the raw columns
// plus the rollup counters and duration stats.
type legacyRow struct {
	UID                         string              `vortex:"uid"`
	CheckUID                    string              `vortex:"check_uid"`
	Region                      sql.Null[string]    `vortex:"region"`
	PeriodType                  string              `vortex:"period_type"`
	PeriodStart                 time.Time           `vortex:"period_start"`
	PeriodEnd                   sql.Null[time.Time] `vortex:"period_end"`
	WorkerUID                   sql.Null[string]    `vortex:"worker_uid"`
	Status                      sql.Null[int64]     `vortex:"status"`
	Duration                    sql.Null[float32]   `vortex:"duration"`
	Metrics                     sql.Null[string]    `vortex:"metrics"`
	Output                      sql.Null[string]    `vortex:"output"`
	Maintenance                 bool                `vortex:"maintenance"`
	TotalChecks                 sql.Null[int64]     `vortex:"total_checks"`
	SuccessfulChecks            sql.Null[int64]     `vortex:"successful_checks"`
	MaintenanceChecks           sql.Null[int64]     `vortex:"maintenance_checks"`
	MaintenanceSuccessfulChecks sql.Null[int64]     `vortex:"maintenance_successful_checks"`
	DurationMin                 sql.Null[float32]   `vortex:"duration_min"`
	DurationMax                 sql.Null[float32]   `vortex:"duration_max"`
	DurationP95                 sql.Null[float32]   `vortex:"duration_p95"`
	DurationP50                 sql.Null[float32]   `vortex:"duration_p50"`
	DurationAvg                 sql.Null[float32]   `vortex:"duration_avg"`
	CreatedAt                   time.Time           `vortex:"created_at"`
}

func encodeLegacy(t *testing.T, rows []legacyRow) []byte {
	t.Helper()

	var buf bytes.Buffer

	writer := vortex.NewRowWriter[legacyRow](&buf, vortex.WriteOptions{Compression: vortex.Compact})
	_, err := writer.Write(rows)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return buf.Bytes()
}

// TestDecodeLegacySchema: the hour files written by v0.55/v0.56 carry more
// columns than Row; they still decode, the extra columns ignored.
func TestDecodeLegacySchema(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	data := encodeLegacy(t, []legacyRow{{
		UID: "raw-1", CheckUID: "check-1", Region: sql.Null[string]{V: "eu", Valid: true},
		PeriodType: models.PeriodTypeRaw, PeriodStart: at, Status: sql.Null[int64]{V: 3, Valid: true},
		CreatedAt: at,
	}})

	rows, err := archive.Decode(t.Context(), data)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal("raw-1", rows[0].UID)
	r.Equal("eu", rows[0].Region.V)
	r.Equal(int64(3), rows[0].Status.V)
}

func TestMergeKeepsUnionByUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	raw := models.PeriodTypeRaw

	existing := []archive.Row{
		{UID: "1", PeriodType: raw, PeriodStart: base},
		{UID: "2", PeriodType: raw, PeriodStart: base.Add(time.Minute)},
	}
	incoming := []archive.Row{
		{UID: "2", PeriodType: raw, PeriodStart: base.Add(time.Minute), Maintenance: true},
		{UID: "0", PeriodType: raw, PeriodStart: base.Add(-time.Minute)},
	}

	merged := archive.Merge(existing, incoming)
	r.Len(merged, 3)
	r.Equal([]string{"0", "1", "2"}, []string{merged[0].UID, merged[1].UID, merged[2].UID})
	r.True(merged[2].Maintenance, "the incoming row replaces the stored one")
}

// TestMergeDropsRollups: a summary row never survives a merge.
func TestMergeDropsRollups(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	merged := archive.Merge(
		[]archive.Row{{UID: "hour-1", PeriodType: models.PeriodTypeHour, PeriodStart: base}},
		[]archive.Row{{UID: "raw-1", PeriodType: models.PeriodTypeRaw, PeriodStart: base}},
	)
	require.Len(t, merged, 1)
	require.Equal(t, "raw-1", merged[0].UID)
}
