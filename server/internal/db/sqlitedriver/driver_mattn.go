//go:build cgo && (cgosqlite || !((darwin && amd64) || (darwin && arm64) || (linux && 386) || (linux && amd64) || (linux && arm) || (linux && arm64) || (windows && amd64)))

package sqlitedriver

// The C driver, and nothing else — in particular not modernc.org/sqlite and its
// libc, whose package init allocates megabytes at startup.
//
//nolint:revive // blank import: linking the driver IS this file's purpose
import _ "github.com/mattn/go-sqlite3"

// Name is the registered driver name for sql.Open.
const Name = "sqlite3"

// Flavor names the driver for logs and the memory snapshot. mattn allocates
// through C malloc, so its memory is OFF the Go heap and invisible to pprof:
// an RSS − Go-heap gap under this driver is the C arena, not a Go leak.
const Flavor = "mattn"
