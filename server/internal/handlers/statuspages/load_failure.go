package statuspages

import (
	"context"
	"errors"
	"log/slog"
)

// logLoadFailure logs the failure of one of the best-effort loaders behind a
// status page view (updates, incidents, response times, coverage…), each of
// which degrades the page rather than failing it.
//
// A visitor who closes the tab or navigates away cancels the request context,
// and every loader still running then fails with context.Canceled. That is not
// a server fault, so nothing is logged for it. A deadline, or a cancellation
// that did not come from the request (the context is still live), stays logged
// at level, like any real database failure.
func logLoadFailure(ctx context.Context, level slog.Level, msg string, err error, args ...any) {
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
		return
	}

	slog.Log(ctx, level, msg, append([]any{"error", err}, args...)...)
}
