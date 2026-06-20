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
// MCP OAuth tables: client persistence, single-use authorization codes, and
// rotating refresh tokens (spec 2026-06-20-03). It runs under the shared
// testService harness so both dialects exercise the same assertions.
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

	// --- Authorization code: single-use compare-and-set ---
	code := &models.OAuthAuthCode{
		UID:                 uuid.New().String(),
		Code:                "code-" + uuid.New().String(),
		ClientID:            client.ClientID,
		UserUID:             user.UID,
		OrganizationUID:     org.UID,
		RedirectURI:         "http://127.0.0.1:1234/callback",
		Scope:               "mcp",
		Resource:            "https://example.com/api/v1/mcp",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		ExpiresAt:           now.Add(time.Minute),
		CreatedAt:           now,
	}
	r.NoError(svc.CreateOAuthAuthCode(ctx, code))

	gotCode, err := svc.GetOAuthAuthCode(ctx, code.Code)
	r.NoError(err)
	r.Equal(code.Resource, gotCode.Resource)
	r.Nil(gotCode.ConsumedAt, "fresh code is not consumed")

	won, err := svc.ConsumeOAuthAuthCode(ctx, code.Code, now)
	r.NoError(err)
	r.True(won, "first consume wins")

	wonAgain, err := svc.ConsumeOAuthAuthCode(ctx, code.Code, now)
	r.NoError(err)
	r.False(wonAgain, "replay of a consumed code loses the race (single-use)")

	// --- Refresh token: rotation + user-wide revocation ---
	refresh := &models.OAuthRefreshToken{
		UID:             uuid.New().String(),
		Token:           "refresh-" + uuid.New().String(),
		ClientID:        client.ClientID,
		UserUID:         user.UID,
		OrganizationUID: org.UID,
		Scope:           "mcp",
		Resource:        "https://example.com/api/v1/mcp",
		ExpiresAt:       now.Add(time.Hour),
		CreatedAt:       now,
	}
	r.NoError(svc.CreateOAuthRefreshToken(ctx, refresh))

	revoked, err := svc.RevokeOAuthRefreshToken(ctx, refresh.Token, now)
	r.NoError(err)
	r.True(revoked, "first revoke wins")

	revokedAgain, err := svc.RevokeOAuthRefreshToken(ctx, refresh.Token, now)
	r.NoError(err)
	r.False(revokedAgain, "rotating an already-revoked token loses the race")

	// A second active token, then a user-wide revoke (logout / PAT revoke).
	refresh2 := &models.OAuthRefreshToken{
		UID:             uuid.New().String(),
		Token:           "refresh2-" + uuid.New().String(),
		ClientID:        client.ClientID,
		UserUID:         user.UID,
		OrganizationUID: org.UID,
		Scope:           "mcp",
		Resource:        "https://example.com/api/v1/mcp",
		ExpiresAt:       now.Add(time.Hour),
		CreatedAt:       now,
	}
	r.NoError(svc.CreateOAuthRefreshToken(ctx, refresh2))

	r.NoError(svc.RevokeOAuthRefreshTokensForUser(ctx, user.UID, now))

	got2, err := svc.GetOAuthRefreshToken(ctx, refresh2.Token)
	r.NoError(err)
	r.NotNil(got2.RevokedAt, "user-wide revoke marks the active token revoked")
}
