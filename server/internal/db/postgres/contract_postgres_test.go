package postgres

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/dbcontract"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestDialectContract_Postgres runs the shared behavior contract on Postgres;
// the same contract runs on SQLite in internal/db/sqlite.
func TestDialectContract_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	dbcontract.Run(t, s)
}

// TestDialectContractClosedDatabase_Postgres checks that every Service method
// reports a closed connection pool as an error on Postgres.
func TestDialectContractClosedDatabase_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	dbcontract.RunClosed(t, s)
}
