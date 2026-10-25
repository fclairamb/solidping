package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunRequiresKeys(t *testing.T) {
	r := require.New(t)
	logger := slog.New(slog.DiscardHandler)

	t.Setenv(keysEnv, "")
	r.ErrorIs(run("127.0.0.1:0", logger), errNoKeys)

	t.Setenv(keysEnv, "not json")
	r.ErrorIs(run("127.0.0.1:0", logger), errNoKeys)

	t.Setenv(keysEnv, `[{"id":"2026-10","secret":"c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0"}]`)
	r.ErrorContains(run("256.0.0.1:bad", logger), "serving")
}
