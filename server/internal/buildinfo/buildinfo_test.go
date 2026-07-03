package buildinfo_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/buildinfo"
)

func TestSQLiteDriverMatchesCGO(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	if buildinfo.CGOEnabled {
		r.Equal("mattn", buildinfo.SQLiteDriver())
	} else {
		r.Equal("modernc", buildinfo.SQLiteDriver())
	}
}

func TestGoVersion(t *testing.T) {
	t.Parallel()

	require.Equal(t, runtime.Version(), buildinfo.GoVersion())
}
