package mcp

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portStdioPrincipalPG is distinct from every other embedded-Postgres port
// claimed in the repo (see tunnel_postgres_test.go's comment for the
// convention).
const portStdioPrincipalPG = 15617

// TestResolveStdioPrincipalPostgres runs the principal table on Postgres,
// where users.uid is a uuid column: a --user that is neither an email nor a
// uid must still read "user not found", not a uuid syntax error. Self-skips
// under -short.
func TestResolveStdioPrincipalPostgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portStdioPrincipalPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runResolveStdioPrincipalCases(t, newStdioEnvOn(t, dbSvc))
}
