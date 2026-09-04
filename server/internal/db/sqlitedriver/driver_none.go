//go:build !cgo && !((darwin && amd64) || (darwin && arm64) || (linux && 386) || (linux && amd64) || (linux && arm) || (linux && arm64) || (windows && amd64))

package sqlitedriver

// Neither driver is available: cgo is off (so mattn cannot build) and this
// GOOS/GOARCH is not one modernc.org/sqlite supports. The binary still builds
// — every other backend works — and SQLite fails at open time with an
// actionable message instead of at link time with an undefined symbol.

// Name is empty, which callers must treat as "no SQLite driver in this build".
const Name = ""

// Flavor reports the absence for the memory snapshot and logs.
const Flavor = "none"
