package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

//nolint:paralleltest // swaps the process-global slog default
func TestLogStarPrompt(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	run := func(enabled bool) string {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))

		srv := &Server{config: &config.Config{App: config.AppConfig{StarPrompt: enabled}}}
		srv.logStarPrompt(context.Background())

		return buf.String()
	}

	on := run(true)
	require.Equal(t, 1, strings.Count(on, "a star helps: https://github.com/fclairamb/solidping"))
	require.Contains(t, on, "level=INFO")
	require.Empty(t, run(false))
}
