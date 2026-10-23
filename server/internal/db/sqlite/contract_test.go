package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbcontract"
)

// TestDialectContract runs the shared behavior contract on SQLite; the same
// contract runs on Postgres in internal/db/postgres.
func TestDialectContract(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(ctx))

	dbcontract.Run(t, svc)
}

// TestDialectContract_ClosedDatabase checks that every Service method reports
// a closed connection pool as an error.
func TestDialectContract_ClosedDatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, svc.Initialize(ctx))

	dbcontract.RunClosed(t, svc)
}
