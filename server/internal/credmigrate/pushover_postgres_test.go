package credmigrate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPushoverBackfillPG is distinct from every other _postgres_test.go
// embedded-Postgres port in the repo.
const portPushoverBackfillPG = 15626

// TestNormalizePushoverSettings_Postgres is the Postgres twin of
// TestNormalizePushoverSettings_SQLiteEncrypted. Self-skips under -short.
//
//nolint:paralleltest // owns a fixed embedded-Postgres port
func TestNormalizePushoverSettings_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portPushoverBackfillPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	assertPushoverBackfill(ctx, t, dbSvc, creds)
}
