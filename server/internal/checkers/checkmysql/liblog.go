package checkmysql

import (
	"context"
	"fmt"
	"log/slog"
)

// mysqlLogger routes go-sql-driver/mysql's own log lines into slog at DEBUG.
// By default the driver prints them straight to stderr ("[mysql] 2026/10/12
// 06:03:59 packets.go:58: unexpected EOF"), outside the log format and level.
// The failure already reaches the check result, so the driver's line is only
// diagnostic detail. Installed by this package's init.
type mysqlLogger struct{}

func (mysqlLogger) Print(v ...any) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	slog.Debug(fmt.Sprint(v...), "component", "go-sql-driver/mysql")
}
