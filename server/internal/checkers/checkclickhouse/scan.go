package checkclickhouse

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// executeQuery runs the health-check query and renders the first cell of the
// first row. The native protocol is strictly typed — a `SELECT 1` column
// arrives as UInt8, not as text — so the destination is allocated from the
// column's own scan type rather than assumed to be a string.
func executeQuery(ctx context.Context, conn driver.Conn, query string) (string, error) {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var result string

	if rows.Next() {
		if result, err = scanFirstColumn(rows); err != nil {
			return "", err
		}
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration error: %w", err)
	}

	return result, nil
}

// scanFirstColumn scans the current row and renders its first column. Extra
// columns are still scanned (the driver requires a destination per column) but
// only the first is reported, matching the other SQL checkers.
func scanFirstColumn(rows driver.Rows) (string, error) {
	columnTypes := rows.ColumnTypes()
	if len(columnTypes) == 0 {
		return "", nil
	}

	dest := make([]any, len(columnTypes))
	for idx, columnType := range columnTypes {
		dest[idx] = newScanTarget(columnType)
	}

	if err := rows.Scan(dest...); err != nil {
		return "", fmt.Errorf("failed to scan result: %w", err)
	}

	return renderValue(dest[0]), nil
}

// newScanTarget allocates a destination the driver can scan a column into.
// ColumnType.ScanType is the Go type the driver decodes that ClickHouse type
// into, so a freshly allocated pointer to it always satisfies Scan. A driver
// that reports no scan type (unknown/experimental column type) falls back to
// `any`, which the driver fills with whatever it decoded.
func newScanTarget(columnType driver.ColumnType) any {
	scanType := columnType.ScanType()
	if scanType == nil {
		var fallback any

		return &fallback
	}

	return reflect.New(scanType).Interface()
}

// renderValue dereferences a scan target and renders it as text. Pointer
// columns (Nullable) are dereferenced once more; a nil one renders as the empty
// string, the same as a missing row.
func renderValue(target any) string {
	value := reflect.ValueOf(target)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return ""
		}

		value = value.Elem()
	}

	if !value.IsValid() {
		return ""
	}

	return fmt.Sprint(value.Interface())
}
