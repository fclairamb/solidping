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

// Row is the schema of an archive file: one row per raw result, the columns
// of models.Result that a raw result fills (the rollup counters and duration
// stats, the scan-only check info and the transient diagnostics are left out).
// An archive only ever holds raw results: period_type is always "raw", kept so
// a merge can drop the summary rows of a day file written by v0.55/v0.56.
// Nullable columns are sql.Null, the JSON columns are UTF-8 strings. The tags
// are the column names external tools see. Decoding reads only these columns,
// so a file written with the older, wider schema still decodes.
type Row struct {
	UID         string            `vortex:"uid"`
	CheckUID    string            `vortex:"check_uid"`
	Region      sql.Null[string]  `vortex:"region"`
	PeriodType  string            `vortex:"period_type"`
	PeriodStart time.Time         `vortex:"period_start"`
	WorkerUID   sql.Null[string]  `vortex:"worker_uid"`
	Status      sql.Null[int64]   `vortex:"status"`
	Duration    sql.Null[float32] `vortex:"duration"`
	Metrics     sql.Null[string]  `vortex:"metrics"`
	Output      sql.Null[string]  `vortex:"output"`
	Maintenance bool              `vortex:"maintenance"`
	CreatedAt   time.Time         `vortex:"created_at"`
}

// RowFromResult converts a raw result to its archive row.
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
		UID:         res.UID,
		CheckUID:    res.CheckUID,
		Region:      nullOf(res.Region),
		PeriodType:  res.PeriodType,
		PeriodStart: res.PeriodStart.UTC(),
		WorkerUID:   nullOf(res.WorkerUID),
		Status:      nullInt(res.Status),
		Duration:    nullOf(res.Duration),
		Metrics:     metrics,
		Output:      output,
		Maintenance: res.Maintenance,
		CreatedAt:   res.CreatedAt.UTC(),
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

// Merge returns the raw rows of existing and incoming, deduplicated by uid: a
// row of incoming replaces the row of existing with the same uid. Any other
// row (the hour summaries of a day file written by v0.55/v0.56) is dropped,
// so an archive never holds a summary. The result is sorted like Encode sorts.
func Merge(existing, incoming []Row) []Row {
	byUID := make(map[string]int, len(existing)+len(incoming))
	merged := make([]Row, 0, len(existing)+len(incoming))

	for _, batch := range [][]Row{existing, incoming} {
		for i := range batch {
			if batch[i].PeriodType != models.PeriodTypeRaw {
				continue
			}

			if idx, ok := byUID[batch[i].UID]; ok {
				merged[idx] = batch[i]

				continue
			}

			byUID[batch[i].UID] = len(merged)
			merged = append(merged, batch[i])
		}
	}

	SortRows(merged)

	return merged
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
