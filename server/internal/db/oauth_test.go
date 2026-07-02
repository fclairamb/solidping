package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// testOAuthRepos is the cross-engine (Postgres + SQLite) parity guard for the
// MCP OAuth storage (spec 2026-06-20-03): client persistence, and refresh
// grants as user_tokens rows of type oauth_refresh — including that the
// widened type check admits the new type on both engines and that the
// soft-delete compare-and-set backs the rotation replay guard. It runs under
// the shared testService harness so both dialects exercise the same
// assertions. (Authorization codes are state_entries records — see the
// "Delete" compare-and-set coverage in service_test.go and the oauth
// package's ExchangeAuthCode tests.)
func testOAuthRepos(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("oauth-repos-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("oauth-repos@example.com")
	r.NoError(svc.CreateUser(ctx, user))

	now := time.Now()

	// --- Client round-trip ---
	secret := "hashed-secret"
	client := &models.OAuthClient{
		UID:          uuid.New().String(),
		ClientID:     "client-" + uuid.New().String(),
		SecretHash:   &secret,
		ClientName:   "Test MCP Client",
		RedirectURIs: []string{"http://127.0.0.1:1234/callback"},
		GrantTypes:   []string{"authorization_code", "refresh_token"},
		Scopes:       []string{"mcp"},
		IsPublic:     false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	r.NoError(svc.CreateOAuthClient(ctx, client))

	gotClient, err := svc.GetOAuthClientByClientID(ctx, client.ClientID)
	r.NoError(err)
	r.Equal(client.ClientID, gotClient.ClientID)
	r.Equal([]string{"http://127.0.0.1:1234/callback"}, gotClient.RedirectURIs)
	r.NotNil(gotClient.SecretHash)
	r.False(gotClient.IsPublic)

	_, err = svc.GetOAuthClientByClientID(ctx, "does-not-exist")
	r.Error(err, "missing client must error")

	// --- Refresh grant: the widened type check + rotation compare-and-set ---
	refresh := models.NewUserToken(
		user.UID, &org.UID, "refresh-"+uuid.New().String(), models.TokenTypeOAuthRefresh,
	)
	expiry := now.Add(time.Hour)
	refresh.ExpiresAt = &expiry
	refresh.Properties = models.JSONMap{
		"client_id": client.ClientID,
		"scope":     "mcp",
		"resource":  "https://example.com/api/v1/mcp",
	}
	r.NoError(svc.CreateUserToken(ctx, refresh),
		"the widened user_tokens type check must admit oauth_refresh")

	got, err := svc.GetUserTokenByToken(ctx, refresh.Token)
	r.NoError(err)
	r.Equal(models.TokenTypeOAuthRefresh, got.Type)
	r.Equal(client.ClientID, got.Properties["client_id"], "grant bindings round-trip through properties")
	r.Equal("mcp", got.Properties["scope"])

	revoked, err := svc.DeleteUserToken(ctx, refresh.UID)
	r.NoError(err)
	r.True(revoked, "first revoke wins")

	revokedAgain, err := svc.DeleteUserToken(ctx, refresh.UID)
	r.NoError(err)
	r.False(revokedAgain, "rotating an already-revoked grant loses the race")

	_, err = svc.GetUserTokenByToken(ctx, refresh.Token)
	r.Error(err, "a revoked (soft-deleted) grant is no longer resolvable by token value")
}
