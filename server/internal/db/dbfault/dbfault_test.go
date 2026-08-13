package dbfault_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/lib/pq"
	"github.com/lib/pq/pqerror"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbfault"
)

// Static errors, so the table cases stay err113-clean.
var (
	// The cgo SQLite driver's wording, which carries no Code() method — the
	// message rule has to stand on its own for it.
	errCgoDriverNoSuchTable = errors.New("no such table: jobs")
	errConnectionRefused    = errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
)

// fakeSQLiteError has the shape of modernc.org/sqlite's *Error (a Code() int
// method), which is how dbfault reads SQLite result codes without importing a
// driver. The real driver's errors are exercised end-to-end against a real
// database by TestSchemaVanishesWhenDatabaseFileIsDeleted in the sqlite
// package; this stand-in is what makes the per-code table below exhaustive and
// hermetic.
type fakeSQLiteError struct {
	code int
	msg  string
}

func (e *fakeSQLiteError) Error() string { return e.msg }
func (e *fakeSQLiteError) Code() int     { return e.code }

func TestClassifyPostgresStructural(t *testing.T) {
	t.Parallel()

	// One case per classified SQLSTATE. lib/pq's *Error is constructible, so
	// these are real driver error values, not strings; the same SQLSTATE
	// arriving through bun's pgdriver is covered by the live-Postgres test in
	// the postgres package (dbfault_postgres_test.go).
	cases := []struct {
		state  pqerror.Code
		reason string
	}{
		{"42P01", "undefined_table"},
		{"42703", "undefined_column"},
		{"42704", "undefined_object"},
		{"42883", "undefined_function"},
		{"3D000", "invalid_catalog_name"},
		{"3F000", "invalid_schema_name"},
	}

	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := &pq.Error{Code: tc.state, Message: "boom"}

			r.Equal(dbfault.ClassStructural, dbfault.Classify(err))

			fault := dbfault.Describe(err)
			r.NotNil(fault)
			r.Equal(dbfault.BackendPostgres, fault.Backend)
			r.Equal(string(tc.state), fault.Code)
			r.Equal(tc.reason, fault.Reason)
			// The driver error stays reachable through the fault.
			r.ErrorIs(fault, err)
		})
	}
}

func TestClassifyPostgresTransient(t *testing.T) {
	t.Parallel()

	// The regression guard for the whole feature: these must keep retrying
	// exactly as before, because they recover on their own.
	cases := []struct {
		name  string
		state pqerror.Code
	}{
		{"connection_exception", "08000"},
		{"connection_does_not_exist", "08003"},
		{"connection_failure", "08006"},
		{"serialization_failure", "40001"},
		{"deadlock_detected", "40P01"},
		{"lock_not_available", "55P03"},
		{"too_many_connections", "53300"},
		{"admin_shutdown", "57P01"},
		{"cannot_connect_now", "57P03"},
		{"database_is_starting_up", "55000"},
		{"statement_timeout", "57014"},
		{"unique_violation", "23505"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := &pq.Error{Code: tc.state, Message: "boom"}

			r.Equal(dbfault.ClassTransient, dbfault.Classify(err))
			r.False(dbfault.IsStructural(err))
			r.Nil(dbfault.Describe(err))
		})
	}
}

func TestClassifySQLiteStructural(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		code   string
		reason string
	}{
		{
			// The exact error string from the seventeen-hour incident.
			name:   "no such table (pure-Go driver wording)",
			err:    &fakeSQLiteError{code: 1, msg: "SQL logic error: no such table: jobs (1)"},
			code:   "1",
			reason: "undefined_table",
		},
		{
			name:   "no such table (cgo driver wording)",
			err:    errCgoDriverNoSuchTable,
			code:   "1",
			reason: "undefined_table",
		},
		{
			name:   "no such column",
			err:    &fakeSQLiteError{code: 1, msg: "SQL logic error: no such column: severity (1)"},
			code:   "1",
			reason: "undefined_column",
		},
		{
			name:   "no such index",
			err:    &fakeSQLiteError{code: 1, msg: "SQL logic error: no such index: idx_jobs_status (1)"},
			code:   "1",
			reason: "undefined_index",
		},
		{
			name:   "database file moved out from under us",
			err:    &fakeSQLiteError{code: 1032, msg: "attempt to write a readonly database (1032)"},
			code:   "1032",
			reason: "database_file_moved",
		},
		{
			name:   "corrupt image",
			err:    &fakeSQLiteError{code: 11, msg: "database disk image is malformed (11)"},
			code:   "11",
			reason: "database_corrupt",
		},
		{
			name:   "not a database",
			err:    &fakeSQLiteError{code: 26, msg: "file is not a database (26)"},
			code:   "26",
			reason: "not_a_database",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(dbfault.ClassStructural, dbfault.Classify(tc.err))

			fault := dbfault.Describe(tc.err)
			r.NotNil(fault)
			r.Equal(dbfault.BackendSQLite, fault.Backend)
			r.Equal(tc.code, fault.Code)
			r.Equal(tc.reason, fault.Reason)
		})
	}
}

func TestClassifySQLiteTransient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{"busy", &fakeSQLiteError{code: 5, msg: "database is locked (5)"}},
		{"locked", &fakeSQLiteError{code: 6, msg: "database table is locked (6)"}},
		{"io error", &fakeSQLiteError{code: 266, msg: "disk I/O error (266)"}},
		{"cant open", &fakeSQLiteError{code: 14, msg: "unable to open database file (14)"}},
		{"interrupted", &fakeSQLiteError{code: 9, msg: "interrupted (9)"}},
		// A logic error that is NOT about a missing object: a query bug, but
		// not a reason to take the process down.
		{"syntax error", &fakeSQLiteError{code: 1, msg: `SQL logic error: near "SELCT": syntax error (1)`}},
		{"constraint", &fakeSQLiteError{code: 275, msg: "constraint failed: CHECK constraint failed (275)"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(dbfault.ClassTransient, dbfault.Classify(tc.err))
			r.False(dbfault.IsStructural(tc.err))
		})
	}
}

func TestClassifyNonDatabaseErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(dbfault.ClassNone, dbfault.Classify(nil))
	r.False(dbfault.IsStructural(nil))

	// Everything unrecognized keeps today's retry behavior.
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		driver.ErrBadConn,
		io.EOF,
		errConnectionRefused,
		&net.OpError{Op: "read", Err: io.ErrUnexpectedEOF},
	} {
		r.Equal(dbfault.ClassTransient, dbfault.Classify(err), "%v", err)
	}
}

func TestDescribeSeesThroughWrapping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Callers wrap: jobsvc does fmt.Errorf("failed to claim job: %w", err) and
	// the runner adds its own context on top. Classification must survive that.
	inner := &pq.Error{Code: "42P01", Message: `relation "jobs" does not exist`}
	wrapped := fmt.Errorf("processing job: %w", fmt.Errorf("failed to claim job: %w", inner))

	fault := dbfault.Describe(wrapped)
	r.NotNil(fault)
	r.Equal("undefined_table", fault.Reason)

	// Re-describing an already-described fault is idempotent.
	r.Same(fault, dbfault.Describe(fmt.Errorf("outer: %w", fault)))
}

func TestFaultErrorMessageNamesTheFault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fault := dbfault.Describe(&pq.Error{Code: "42P01", Message: `relation "jobs" does not exist`})
	r.NotNil(fault)
	r.Contains(fault.Error(), "structural database fault")
	r.Contains(fault.Error(), "undefined_table")
	r.Contains(fault.Error(), "42P01")
}
