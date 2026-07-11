package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/oauth"
)

// newCLISeedServer builds a minimal Server around an in-memory SQLite DB —
// SeedCLIOAuthClient only touches dbService.
func newCLISeedServer(ctx context.Context, t *testing.T) (*Server, *sqlite.Service) {
	t.Helper()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return &Server{dbService: dbSvc}, dbSvc
}

// TestSeedCLIOAuthClient covers the boot seeding of the first-party
// `solidping-cli` OAuth client: the client is created with the expected
// public/loopback shape, and re-seeding is a no-op (no duplicate, no error).
func TestSeedCLIOAuthClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := require.New(t)

	srv, dbSvc := newCLISeedServer(ctx, t)

	// Absent before seeding.
	_, err := dbSvc.GetOAuthClientByClientID(ctx, oauth.CLIClientID)
	r.Error(err)

	// First seed creates the client.
	r.NoError(srv.SeedCLIOAuthClient(ctx))

	client, err := dbSvc.GetOAuthClientByClientID(ctx, oauth.CLIClientID)
	r.NoError(err)
	r.Equal(oauth.CLIClientID, client.ClientID)
	r.True(client.IsPublic)
	r.Nil(client.SecretHash)
	r.Equal([]string{oauth.CLIRedirectURI}, client.RedirectURIs)
	r.Contains(client.GrantTypes, oauth.GrantAuthorizationCode)
	r.Contains(client.Scopes, oauth.ScopeMCP)

	// Re-seeding is idempotent: no error and the same single row (same UID).
	r.NoError(srv.SeedCLIOAuthClient(ctx))

	again, err := dbSvc.GetOAuthClientByClientID(ctx, oauth.CLIClientID)
	r.NoError(err)
	r.Equal(client.UID, again.UID, "re-seed must not replace the existing client")
}
