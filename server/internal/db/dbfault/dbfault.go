// Package dbfault classifies database driver errors as transient (worth
// retrying) or structural (never worth retrying), in one place, for every
// component that talks to the database.
//
// The distinction exists because retrying is not always harmless. A dropped
// connection, a deadlock or a saturated pool comes back on its own, so a runner
// should back off and try again. But when the *schema* is gone — the table, the
// column, the database itself — no number of retries will make it reappear:
// migrations only run at startup, so a live process can never repair it. One
// incident had a server spend seventeen hours logging
// `SQL logic error: no such table: jobs (1)` at full speed while presenting
// itself as a running service (see the reproduction in the sqlite package,
// TestSchemaVanishesWhenDatabaseFileIsDeleted, and
// wiki/conventions/database-faults.md).
//
// Classification lives here rather than in any single caller so it is written
// once and shared: jobworker and checkworker use it today, and it is
// deliberately free of any dependency on them.
package dbfault

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Class is the retry verdict for a database error.
type Class int

const (
	// ClassNone is the classification of a nil error.
	ClassNone Class = iota
	// ClassTransient means "retry": the database may answer correctly later.
	// This is the default for anything not positively identified as
	// structural, so an unrecognized error keeps today's retry behavior.
	ClassTransient
	// ClassStructural means "do not retry": the schema or database this
	// process needs is not there, and no amount of waiting will bring it back
	// inside this process's lifetime.
	ClassStructural
)

// String renders the class for logs.
func (c Class) String() string {
	switch c {
	case ClassNone:
		return "none"
	case ClassStructural:
		return "structural"
	case ClassTransient:
		return "transient"
	default:
		return "transient"
	}
}

// Backend names used in StructuralError.Backend.
const (
	BackendSQLite   = "sqlite"
	BackendPostgres = "postgres"
)

// StructuralError is the structural classification of a driver error: what the driver
// said, translated into a condition name that means the same thing on both
// backends. It wraps the original error, so errors.Is/As still reach it.
type StructuralError struct {
	// Backend is "sqlite" or "postgres".
	Backend string
	// Code is the driver's own code: a PostgreSQL SQLSTATE ("42P01") or a
	// SQLite result code ("1", "1032").
	Code string
	// Reason is the SQL-standard condition name for the fault
	// ("undefined_table", "invalid_catalog_name", "database_corrupt", …).
	Reason string

	err error
}

// Error implements error.
func (f *StructuralError) Error() string {
	return fmt.Sprintf("structural database fault: %s (%s %s): %v", f.Reason, f.Backend, f.Code, f.err)
}

// Unwrap exposes the driver error the fault was derived from.
func (f *StructuralError) Unwrap() error { return f.err }

// Classify returns the retry verdict for err.
func Classify(err error) Class {
	switch {
	case err == nil:
		return ClassNone
	case Describe(err) != nil:
		return ClassStructural
	default:
		return ClassTransient
	}
}

// IsStructural reports whether err (or anything it wraps) means the schema this
// process needs is gone.
func IsStructural(err error) bool { return Describe(err) != nil }

// Describe returns the structural fault carried by err, or nil when err is nil
// or merely transient. An error that is already a *StructuralError is returned
// as-is, so re-classifying a wrapped fault is idempotent.
func Describe(err error) *StructuralError {
	if err == nil {
		return nil
	}

	var existing *StructuralError
	if errors.As(err, &existing) {
		return existing
	}

	if fault := describePostgres(err); fault != nil {
		return fault
	}

	return describeSQLite(err)
}

// structuralSQLStates maps the PostgreSQL SQLSTATEs that mean "the object this
// process queries does not exist" to their standard condition names. Everything
// else — the 08xxx connection exceptions, 40xxx rollbacks, 53xxx resource
// exhaustion, 57xxx operator intervention — stays transient and keeps retrying,
// because those genuinely do come back.
// Deliberately absent: 55000 object_not_in_prerequisite_state ("the database
// system is starting up") and 57P03 cannot_connect_now, which are exactly the
// transient conditions a restart produces.
//
//nolint:gochecknoglobals // constant lookup table
var structuralSQLStates = map[string]string{
	"3D000": "invalid_catalog_name", // the database itself is gone
	"3F000": "invalid_schema_name",  // the schema is gone
	"42P01": "undefined_table",      // the incident's Postgres equivalent
	"42703": "undefined_column",     // schema older than this binary
	"42704": "undefined_object",     // a type/collation/… we depend on
	"42883": "undefined_function",   // e.g. a missing extension's function
}

// describePostgres classifies a PostgreSQL driver error by SQLSTATE. Both
// drivers in this repository are covered: bun's pgdriver (the ORM) and lib/pq
// (the LISTEN/NOTIFY notifier and the postgres checker).
func describePostgres(err error) *StructuralError {
	state := sqlState(err)
	if state == "" {
		return nil
	}

	reason, ok := structuralSQLStates[state]
	if !ok {
		return nil
	}

	return &StructuralError{Backend: BackendPostgres, Code: state, Reason: reason, err: err}
}

// sqlState extracts the five-character SQLSTATE from whichever PostgreSQL
// driver produced err, or "" when err is not a PostgreSQL server error.
func sqlState(err error) string {
	var bunErr pgdriver.Error
	if errors.As(err, &bunErr) {
		return bunErr.Field('C')
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code)
	}

	return ""
}

// SQLite result codes. Unlike PostgreSQL, SQLite has no dedicated code for a
// missing table: "no such table" is reported as the generic SQLITE_ERROR (1),
// the same code as a syntax error. So the code narrows the error to a logic
// error and the message names the missing object — see missingSchemaObject.
const (
	sqliteError           = 1    // SQLITE_ERROR — generic SQL logic error
	sqliteCorrupt         = 11   // SQLITE_CORRUPT — malformed database image
	sqliteNotADB          = 26   // SQLITE_NOTADB — the file is not a database
	sqliteReadonlyDBMoved = 1032 // SQLITE_READONLY_DBMOVED — the file was moved/deleted under us
	sqlitePrimaryCodeMask = 0xFF // extended codes carry the primary code in the low byte
)

// sqliteCoder is the shape of modernc.org/sqlite's *Error, the pure-Go driver
// sqliteshim selects on every platform this project builds for. It is matched
// structurally rather than by import: importing the cgo driver
// (github.com/mattn/go-sqlite3, used only when the `cgosqlite` build tag is
// set) would drag a C compilation into every build. That driver exposes its
// code as a struct field rather than a method, so it falls through to the
// message rule below — which is why the message rule must stand on its own.
type sqliteCoder interface{ Code() int }

// describeSQLite classifies a SQLite driver error.
func describeSQLite(err error) *StructuralError {
	code, hasCode := sqliteCode(err)

	if hasCode {
		switch code {
		case sqliteCorrupt:
			return &StructuralError{Backend: BackendSQLite, Code: "11", Reason: "database_corrupt", err: err}
		case sqliteNotADB:
			return &StructuralError{Backend: BackendSQLite, Code: "26", Reason: "not_a_database", err: err}
		case sqliteReadonlyDBMoved:
			// The clearest signal there is: SQLite noticed the file it opened
			// has been unlinked or renamed.
			return &StructuralError{Backend: BackendSQLite, Code: "1032", Reason: "database_file_moved", err: err}
		}

		// Any other primary code (BUSY, LOCKED, IOERR, CANTOPEN, …) is an
		// operational condition that can clear on its own: keep retrying.
		if code&sqlitePrimaryCodeMask != sqliteError {
			return nil
		}
	}

	reason, ok := missingSchemaObject(err.Error())
	if !ok {
		return nil
	}

	codeStr := "1"
	if hasCode {
		codeStr = strconv.Itoa(code)
	}

	return &StructuralError{Backend: BackendSQLite, Code: codeStr, Reason: reason, err: err}
}

// sqliteCode returns the driver's result code when the error exposes one.
func sqliteCode(err error) (int, bool) {
	var coder sqliteCoder
	if errors.As(err, &coder) {
		return coder.Code(), true
	}

	return 0, false
}

// missingSchemaObjectPhrases maps SQLite's wording for a missing schema object
// to the SQLSTATE condition name PostgreSQL uses for the same thing, so callers
// see one vocabulary regardless of backend. The phrasing is stable across both
// SQLite drivers — the pure-Go one renders
// "SQL logic error: no such table: jobs (1)" and the cgo one
// "no such table: jobs" — and it is only ever consulted for an error the driver
// already reported as a logic error.
//
//nolint:gochecknoglobals // constant lookup table
var missingSchemaObjectPhrases = []struct {
	phrase string
	reason string
}{
	{"no such table:", "undefined_table"},
	{"no such column:", "undefined_column"},
	{"no such index:", "undefined_index"},
	{"no such view:", "undefined_view"},
	{"no such trigger:", "undefined_trigger"},
}

// missingSchemaObject reports whether a SQLite error message names a schema
// object that does not exist.
func missingSchemaObject(msg string) (string, bool) {
	lowered := strings.ToLower(msg)

	for i := range missingSchemaObjectPhrases {
		if strings.Contains(lowered, missingSchemaObjectPhrases[i].phrase) {
			return missingSchemaObjectPhrases[i].reason, true
		}
	}

	return "", false
}
