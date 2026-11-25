package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	vortex "github.com/fclairamb/vortex-go"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Row is the schema of an archive file: one row per result, the columns of
// models.Result that carry data (the scan-only check info and the transient
// diagnostics are left out). Nullable columns are sql.Null, the JSON columns
// are UTF-8 strings. The tags are the column names external tools see.
type Row struct {
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

// RowFromResult converts a result to its archive row.
func RowFromResult(res *models.Result) (Row, error) {
	metrics, err := jsonColumn(res.Metrics)
	if err != nil {
		return Row{}, fmt.Errorf("metrics of %s: %w", res.UID, err)
	}

	output, err := jsonColumn(res.Output)
	if err != nil {
		return Row{}, fmt.Errorf("output of %s: %w", res.UID, err)
	}

	return Row{
		UID:                         res.UID,
		CheckUID:                    res.CheckUID,
		Region:                      nullOf(res.Region),
		PeriodType:                  res.PeriodType,
		PeriodStart:                 res.PeriodStart.UTC(),
		PeriodEnd:                   nullTime(res.PeriodEnd),
		WorkerUID:                   nullOf(res.WorkerUID),
		Status:                      nullInt(res.Status),
		Duration:                    nullOf(res.Duration),
		Metrics:                     metrics,
		Output:                      output,
		Maintenance:                 res.Maintenance,
		TotalChecks:                 nullInt(res.TotalChecks),
		SuccessfulChecks:            nullInt(res.SuccessfulChecks),
		MaintenanceChecks:           nullInt(res.MaintenanceChecks),
		MaintenanceSuccessfulChecks: nullInt(res.MaintenanceSuccessfulChecks),
		DurationMin:                 nullOf(res.DurationMin),
		DurationMax:                 nullOf(res.DurationMax),
		DurationP95:                 nullOf(res.DurationP95),
		DurationP50:                 nullOf(res.DurationP50),
		DurationAvg:                 nullOf(res.DurationAvg),
		CreatedAt:                   res.CreatedAt.UTC(),
	}, nil
}

// Encode writes rows as a Vortex file, sorted by period_start then uid, with
// the compression meant for long storage.
func Encode(rows []Row) ([]byte, error) {
	sorted := slices.Clone(rows)
	SortRows(sorted)

	var buf bytes.Buffer

	writer := vortex.NewRowWriter[Row](&buf, vortex.WriteOptions{Compression: vortex.Compact})

	if _, err := writer.Write(sorted); err != nil {
		return nil, fmt.Errorf("write vortex rows: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close vortex writer: %w", err)
	}

	return buf.Bytes(), nil
}

// Decode reads every row of a Vortex archive file.
func Decode(ctx context.Context, data []byte) ([]Row, error) {
	file, err := vortex.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open vortex file: %w", err)
	}

	defer func() { _ = file.Close() }()

	rows := make([]Row, 0, file.NumRows())

	for row, rowErr := range vortex.Rows[Row](ctx, file, vortex.Query{}) {
		if rowErr != nil {
			return nil, fmt.Errorf("read vortex rows: %w", rowErr)
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// Merge returns the union of existing and incoming: a row of incoming
// replaces the row of existing with the same key (see rowKey). The result is
// sorted like Encode sorts.
func Merge(existing, incoming []Row) []Row {
	byKey := make(map[string]int, len(existing)+len(incoming))
	merged := make([]Row, 0, len(existing)+len(incoming))

	for _, batch := range [][]Row{existing, incoming} {
		for i := range batch {
			key := rowKey(&batch[i])
			if idx, ok := byKey[key]; ok {
				merged[idx] = batch[i]

				continue
			}

			byKey[key] = len(merged)
			merged = append(merged, batch[i])
		}
	}

	SortRows(merged)

	return merged
}

// rowKey is the dedupe key of a row: its uid for a raw row, (region,
// period_type, period_start) for a rollup row, whose uid may change when the
// rollup is upserted again.
func rowKey(row *Row) string {
	if row.PeriodType == models.PeriodTypeRaw {
		return "uid:" + row.UID
	}

	region := "-"
	if row.Region.Valid {
		region = "=" + row.Region.V
	}

	return "rollup:" + region + "|" + row.PeriodType + "|" + row.PeriodStart.UTC().Format(time.RFC3339Nano)
}

// regionHour is the (region, hour) a row belongs to.
type regionHour struct {
	region sql.Null[string]
	hour   time.Time
}

func regionHourOf(row *Row) regionHour {
	return regionHour{region: row.Region, hour: row.PeriodStart.UTC().Truncate(time.Hour)}
}

// WithRollupFallback adds to rows (the raw rows of a day) the hour rollup of
// each (region, hour) that has no raw row, so an hour compacted before
// archiving was switched on is not silently missing from the day file. A
// rollup row of rows whose hour now has raw rows is dropped: the raw rows
// supersede it. The result is merged and sorted like Merge.
func WithRollupFallback(rows, rollups []Row) []Row {
	covered := make(map[regionHour]bool, len(rows))

	for i := range rows {
		if rows[i].PeriodType == models.PeriodTypeRaw {
			covered[regionHourOf(&rows[i])] = true
		}
	}

	kept := make([]Row, 0, len(rows))

	for i := range rows {
		if rows[i].PeriodType == models.PeriodTypeHour && covered[regionHourOf(&rows[i])] {
			continue
		}

		kept = append(kept, rows[i])
	}

	fallback := make([]Row, 0, len(rollups))

	for i := range rollups {
		if !covered[regionHourOf(&rollups[i])] {
			fallback = append(fallback, rollups[i])
		}
	}

	return Merge(kept, fallback)
}

// SortRows orders rows by period_start, then uid for a stable file.
func SortRows(rows []Row) {
	slices.SortFunc(rows, func(a, b Row) int {
		if c := a.PeriodStart.Compare(b.PeriodStart); c != 0 {
			return c
		}

		return strings.Compare(a.UID, b.UID)
	})
}

func jsonColumn(value models.JSONMap) (sql.Null[string], error) {
	if value == nil {
		return sql.Null[string]{}, nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return sql.Null[string]{}, err
	}

	return sql.Null[string]{V: string(data), Valid: true}, nil
}

func nullOf[T any](value *T) sql.Null[T] {
	if value == nil {
		return sql.Null[T]{}
	}

	return sql.Null[T]{V: *value, Valid: true}
}

func nullInt(value *int) sql.Null[int64] {
	if value == nil {
		return sql.Null[int64]{}
	}

	return sql.Null[int64]{V: int64(*value), Valid: true}
}

func nullTime(value *time.Time) sql.Null[time.Time] {
	if value == nil {
		return sql.Null[time.Time]{}
	}

	return sql.Null[time.Time]{V: value.UTC(), Valid: true}
}
