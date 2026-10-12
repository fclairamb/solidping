package checkredis

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// redisLogger routes go-redis's own log lines into slog at DEBUG. By default
// go-redis prints them straight to stderr ("redis: 2026/10/12 06:03:59
// pool.go:770: redis: connection pool: failed to dial after 5 attempts..."),
// outside the log format and level. The failure already reaches the check
// result, so the library's line is only diagnostic detail.
type redisLogger struct{}

func (redisLogger) Printf(ctx context.Context, format string, v ...any) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}

	slog.DebugContext(ctx, fmt.Sprintf(format, v...), "component", "go-redis")
}

//nolint:gochecknoinits // go-redis's logger is process-global: set it once, before any client exists
func init() {
	redis.SetLogger(redisLogger{})
}
