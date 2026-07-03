// Package buildinfo exposes build-time facts that change the process's memory
// shape — chiefly whether cgo is enabled and, by extension, which SQLite driver
// uptrace/bun/driver/sqliteshim selects.
//
// This matters for memory analysis: when cgo is on, sqliteshim selects the
// mattn driver, whose allocations live off the Go heap and are therefore
// invisible to pprof heap profiles. When cgo is off, sqliteshim selects the
// pure-Go modernc driver, whose allocations are on-heap and show up in profiles.
// Knowing CGOEnabled is the difference between attributing the
// `RSS − go_heap_inuse` gap to a real off-heap consumer versus chasing a
// phantom leak.
package buildinfo

import "runtime"

// CGOEnabled reports whether the binary was built with cgo. Its value is set by
// the build-tagged cgo.go / nocgo.go files in this package.
//
//nolint:gochecknoglobals // build-tag-selected constant, intentionally package-level
var CGOEnabled = cgoEnabled

// SQLiteDriver returns the underlying driver name that
// uptrace/bun/driver/sqliteshim selects for this build: "mattn" (CGO, off-heap)
// or "modernc" (pure-Go, on-heap). This mirrors sqliteshim's own CGO-based
// selection so a memory snapshot can report which one is in play without
// reaching into the driver.
func SQLiteDriver() string {
	if CGOEnabled {
		return "mattn"
	}
	return "modernc"
}

// GoVersion returns the Go runtime version the binary was built with.
func GoVersion() string {
	return runtime.Version()
}
