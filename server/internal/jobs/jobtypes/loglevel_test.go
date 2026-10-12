package jobtypes

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLevelForCount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(slog.LevelDebug, levelForCount(0), "nothing done: silent at INFO")
	r.Equal(slog.LevelInfo, levelForCount(1))
	r.Equal(slog.LevelInfo, levelForCount(int64(1234)))
	r.Equal(slog.LevelDebug, levelForCount(int64(0)))
}
