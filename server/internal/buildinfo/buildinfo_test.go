package buildinfo_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/buildinfo"
	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestSQLiteDriverReportsTheLinkedDriver pins the fix from spec 2026-09-04-01:
// the reported driver is the one that was actually linked, not one inferred
// from cgo. The old inference ("cgo on ⇒ mattn") was simply false — the build
// tags select modernc on every platform SolidPing ships to unless `cgosqlite`
// is passed — and the runbook's off-heap rule was built on it.
func TestSQLiteDriverReportsTheLinkedDriver(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(sqlitedriver.Flavor, buildinfo.SQLiteDriver())
	r.Contains([]string{"mattn", "modernc", "none"}, buildinfo.SQLiteDriver())
}

// TestSQLiteDriverIsNotInferredFromCGO is the regression guard proper: on a
// normal cgo-enabled build of a supported platform the driver must be modernc.
// If this ever reads "mattn" without the cgosqlite tag, the selection changed
// and every off-heap conclusion in wiki/runbooks/memory-profiling.md has to be
// re-derived.
func TestSQLiteDriverIsNotInferredFromCGO(t *testing.T) {
	t.Parallel()

	if !buildinfo.CGOEnabled {
		t.Skip("this guard is about the cgo build")
	}

	require.Equal(t, "modernc", buildinfo.SQLiteDriver(),
		"a cgo build without -tags cgosqlite links modernc; cgo alone never selected mattn")
}

func TestGoVersion(t *testing.T) {
	t.Parallel()

	require.Equal(t, runtime.Version(), buildinfo.GoVersion())
}
