package postgres

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/dbcontract"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portDialectContract is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portDialectContract = 15900

// TestDialectContract_Postgres runs the shared behaviour contract on Postgres;
// the same contract runs on SQLite in internal/db/sqlite.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestDialectContract_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portDialectContract, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	dbcontract.Run(t, s)
}
