package sqlite

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVacuumLogLevel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(slog.LevelDebug, vacuumLogLevel(600*time.Microsecond), "a fast vacuum is not news")
	r.Equal(slog.LevelInfo, vacuumLogLevel(slowVacuum))
	r.Equal(slog.LevelInfo, vacuumLogLevel(42*time.Second))
}

func TestDatabaseFilename(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("solidping.db", DatabaseFilename(""))
	r.Equal("solidping-test.db", DatabaseFilename("test"))
	r.Equal("solidping-demo.db", DatabaseFilename("demo"))
}
