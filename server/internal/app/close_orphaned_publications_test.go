package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureDefaultLogger swaps slog's default logger for one writing to a buffer.
// Tests using it must not run in parallel.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buf
}

func TestCloseOrphanedStatusPagePublications(t *testing.T) {
	ctx := context.Background()

	t.Run("a healthy database sweeps silently, without a services registry", func(t *testing.T) {
		r := require.New(t)
		srv, _ := newRegionsSeedServer(ctx, t)
		logs := captureDefaultLogger(t)

		srv.CloseOrphanedStatusPagePublications(ctx)

		r.NotContains(logs.String(), "Failed to close status-page entries")
	})

	t.Run("a failing sweep is logged and never blocks the boot", func(t *testing.T) {
		r := require.New(t)
		srv, dbSvc := newRegionsSeedServer(ctx, t)
		r.NoError(dbSvc.Close())
		logs := captureDefaultLogger(t)

		srv.CloseOrphanedStatusPagePublications(ctx)

		r.Contains(logs.String(), "Failed to close status-page entries of deleted checks")
	})
}
