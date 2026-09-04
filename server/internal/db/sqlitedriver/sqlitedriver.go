// Package sqlitedriver selects the SQLite driver at build time and links only
// the one selected.
//
// It replaces uptrace/bun/driver/sqliteshim, which makes the same choice but
// imports **both** drivers into every binary, so the unused one is linked and
// still runs its package init.
//
// The selection rule is copied from sqliteshim's build tags **exactly**, so
// which driver runs does not change:
//
//	modernc.org/sqlite ("sqlite")  — the default on darwin/amd64, darwin/arm64,
//	                                 linux/{386,amd64,arm,arm64} and
//	                                 windows/amd64. Pure Go: allocations are
//	                                 ON the Go heap and visible to pprof.
//	mattn/go-sqlite3   ("sqlite3") — only with the `cgosqlite` build tag, or on
//	                                 a platform modernc does not support. C
//	                                 allocations: OFF the Go heap and invisible
//	                                 to pprof.
//
// Note which way round that is, because the repository documented the opposite
// for months: **the shipped cgo build uses modernc, not mattn.** sqliteshim's
// modernc file is gated on `!cgosqlite` and the platform list — not on `!cgo` —
// so enabling cgo does not select mattn; only `-tags cgosqlite` does. Any
// reasoning about the off-heap gap that assumed "cgo on ⇒ mattn ⇒ off-heap" was
// reasoning about a build this project does not produce.
//
// [Name] is the driver name to pass to sql.Open, and [ForeignKeysDSNParam] the
// connection-string parameter that turns on foreign-key enforcement for it —
// the two things that actually differ between them.
package sqlitedriver

// ForeignKeysDSNParam returns the connection-string parameter that enables
// foreign-key enforcement for the linked driver. mattn reads `_foreign_keys=on`;
// modernc applies `_pragma=foreign_keys(1)` on every connection it opens.
//
// Getting this wrong is not cosmetic: without enforcement a hard delete leaves
// orphaned results rows and permanently halts aggregation (spec 2026-07-12-01 §4).
func ForeignKeysDSNParam() string {
	if Name == "sqlite3" {
		return "_foreign_keys=on"
	}

	return "_pragma=foreign_keys(1)"
}
