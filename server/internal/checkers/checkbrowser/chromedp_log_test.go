package checkbrowser

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogChromedpError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logChromedpError(logger, "could not retrieve document root for ABC123: context deadline exceeded")
	require.Contains(t, buf.String(), "level=DEBUG")
	require.Contains(t, buf.String(), `msg="chromedp error"`)

	buf.Reset()
	logChromedpError(logger, "ERROR: something else broke")
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), `msg="chromedp error"`)
	require.Contains(t, buf.String(), "something else broke")
}

func TestChromedpLogOptions(t *testing.T) {
	t.Parallel()

	require.Len(t, chromedpLogOptions(slog.Default()), 2)
}
