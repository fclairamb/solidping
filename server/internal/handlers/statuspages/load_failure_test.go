package statuspages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

var errDiskIO = errors.New("disk I/O error")

// captureLogs routes the default logger to a buffer for the test. Not parallel:
// slog's default logger is process-wide.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buf
}

// TestLogLoadFailureSkipsVisitorCancellation: a status page loader failing
// because the visitor went away logs nothing, while every other failure keeps
// its level.
func TestLogLoadFailureSkipsVisitorCancellation(t *testing.T) { //nolint:paralleltest // slog.SetDefault
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	wrapped := fmt.Errorf("aggregate response time bins: %w", context.Canceled)

	tests := []struct {
		name    string
		ctx     context.Context //nolint:containedctx // table input
		err     error
		wantLog bool
	}{
		{"visitor went away", canceled, wrapped, false},
		{"canceled error on a live request", context.Background(), wrapped, true},
		{"real failure on a canceled request", canceled, errDiskIO, true},
		{"deadline exceeded", canceled, context.DeadlineExceeded, true},
		{"real failure", context.Background(), errDiskIO, true},
	}

	for _, tc := range tests { //nolint:paralleltest // slog.SetDefault
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)

			logLoadFailure(tc.ctx, slog.LevelError, "Failed to load acme loader", tc.err, "orgUID", "acme-org")

			if !tc.wantLog {
				require.Empty(t, buf.String())

				return
			}

			require.Contains(t, buf.String(), "level=ERROR")
			require.Contains(t, buf.String(), "Failed to load acme loader")
			require.Contains(t, buf.String(), "orgUID=acme-org")
		})
	}
}

// TestLoadRecentUpdatesCanceledRequestLogsNothing drives a real loader: the
// updates query of a request whose visitor has gone fails with
// context.Canceled, and the page load must not log it as a server error.
func TestLoadRecentUpdatesCanceledRequestLogsNothing(t *testing.T) { //nolint:paralleltest // slog.SetDefault
	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	svc := NewService(dbService, &config.Config{}, nil)
	page := &models.StatusPage{UID: "acme-page", HistoryDays: 90}

	reqCtx, cancel := context.WithCancel(ctx)
	cancel()

	buf := captureLogs(t)

	r.Nil(svc.loadRecentUpdates(reqCtx, org.UID, page, 0))
	r.NotContains(buf.String(), "Failed to load public status updates")
}
