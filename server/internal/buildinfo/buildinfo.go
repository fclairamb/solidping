// Package buildinfo exposes build-time facts that change the process's memory
// shape: whether cgo is enabled, which SQLite driver was linked, and the Go
// version.
//
// This matters for memory analysis. The mattn SQLite driver allocates through C
// malloc, off the Go heap and invisible to pprof heap profiles; the pure-Go
// modernc driver allocates on-heap, where profiles do see it. Which one is
// linked is the difference between attributing the `RSS − go_heap_inuse` gap to
// a real off-heap consumer and chasing a phantom leak.
//
// **cgo does not decide that**, contrary to what this package claimed until
// spec 2026-09-04-01 measured it. The driver choice is made by build tags that
// select modernc on every platform SolidPing ships to unless `-tags cgosqlite`
// is passed — cgo on or off. So a cgo-enabled release build uses **modernc**,
// and its off-heap gap is not a C arena. SQLiteDriver now reports what was
// actually linked rather than inferring it from CGOEnabled.
package buildinfo

import (
	"runtime"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// CGOEnabled reports whether the binary was built with cgo. Its value is set by
// the build-tagged cgo.go / nocgo.go files in this package.
//
//nolint:gochecknoglobals // build-tag-selected constant, intentionally package-level
var CGOEnabled = cgoEnabled

// SQLiteDriver returns the SQLite driver actually linked into this build:
// "mattn" (C, off-heap), "modernc" (pure Go, on-heap), or "none" on a platform
// where neither is available. It reads the linked driver's own constant rather
// than guessing from CGOEnabled — guessing is what made this function wrong.
func SQLiteDriver() string {
	return sqlitedriver.Flavor
}

// GoVersion returns the Go runtime version the binary was built with.
func GoVersion() string {
	return runtime.Version()
}
