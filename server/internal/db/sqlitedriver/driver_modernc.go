//go:build !cgosqlite && ((darwin && amd64) || (darwin && arm64) || (linux && 386) || (linux && amd64) || (linux && arm) || (linux && arm64) || (windows && amd64))

package sqlitedriver

// The pure-Go driver, and nothing else — in particular not mattn/go-sqlite3.
//
//nolint:revive // blank import: linking the driver IS this file's purpose
import _ "modernc.org/sqlite"

// Name is the registered driver name for sql.Open.
const Name = "sqlite"

// Flavor names the driver for logs and the memory snapshot. modernc allocates
// on the Go heap, so its memory DOES show up in pprof heap profiles and the
// RSS − Go-heap gap should stay small.
const Flavor = "modernc"
