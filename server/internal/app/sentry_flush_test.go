package app

import (
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

// TestFlushSentryWithoutDSNIsSilent pins that a server with Sentry disabled
// (no DSN, so initSentry never ran sentry.Init) shuts down without the
// "Sentry flush timed out" warning: there is no queue to flush.
//
//nolint:paralleltest // captureLogs swaps the process-wide slog default (see its comment)
func TestFlushSentryWithoutDSNIsSilent(t *testing.T) {
	r := require.New(t)
	r.False(sentry.CurrentHub().Client().IsEnabled(), "no test in this package initializes Sentry")

	buf := captureLogs(t)
	flushSentry(t.Context())

	r.Empty(buf.String())
}
