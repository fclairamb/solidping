package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbfault"
)

// portDBFault is distinct from every other _postgres_test.go file's embedded-
// Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portDBFault = 15468

// newDBFaultServicePG starts embedded Postgres and returns a ready Service,
// self-skipping (like every embedded-postgres test in this package) when
// embedded Postgres is unavailable or under -short.
func newDBFaultServicePG(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portDBFault, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s
}

// TestRealPostgresErrorsAreClassified proves the classification against errors
// produced by the actual driver stack this server runs on (bun + pgdriver),
// not hand-built values: the SQLSTATE has to survive bun's wrapping for the
// fail-fast path to ever trigger in production.
func TestRealPostgresErrorsAreClassified(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newDBFaultServicePG(t)
	ctx := t.Context()

	// Sanity: the migrated schema answers.
	var n int
	r.NoError(svc.db.QueryRowContext(ctx, "SELECT count(*) FROM jobs").Scan(&n))

	// Structural: the table this process needs does not exist. This is the
	// Postgres shape of the incident.
	_, err := svc.db.ExecContext(ctx, "SELECT count(*) FROM jobs_that_do_not_exist")
	r.Error(err)

	fault := dbfault.Describe(err)
	r.NotNil(fault, "real pgdriver error must classify as structural: %v", err)
	r.Equal(dbfault.BackendPostgres, fault.Backend)
	r.Equal("42P01", fault.Code)
	r.Equal("undefined_table", fault.Reason)

	// Structural: a column that is not there (schema older than the binary).
	_, err = svc.db.ExecContext(ctx, "SELECT column_that_does_not_exist FROM jobs")
	r.Error(err)

	fault = dbfault.Describe(err)
	r.NotNil(fault)
	r.Equal("42703", fault.Code)
	r.Equal("undefined_column", fault.Reason)

	// Positive control on the other side: a real driver error that is NOT
	// structural must stay transient, or the classifier would just be
	// answering "yes" to everything.
	_, err = svc.db.ExecContext(ctx, "SELCT bogus")
	r.Error(err)
	r.Nil(dbfault.Describe(err), "a syntax error is not a schema fault: %v", err)
	r.Equal(dbfault.ClassTransient, dbfault.Classify(err))
}
