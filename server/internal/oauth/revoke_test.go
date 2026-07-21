package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// mintGrant runs the auth-code flow for the fixture's default user + client and
// returns the resulting token pair (an oauth_refresh grant is now persisted).
func (f oauthFixture) mintGrant(t *testing.T) *TokenResult {
	t.Helper()

	code, err := f.svc.IssueAuthCode(f.ctx, f.grant(challengeFor(testVerifier), ScopeMCP))
	require.NoError(t, err)

	res, err := f.svc.ExchangeAuthCode(f.ctx, code, f.client.ClientID, testRedirectURI, testVerifier)
	require.NoError(t, err)

	return res
}

// TestRevokeGrantRevokesOwnGrant is the positive path: a client revoking the
// grant backing its own credentials kills the refresh token immediately, and no
// outstanding access token for that grant can be traded for a new pair.
func TestRevokeGrantRevokesOwnGrant(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	res := f.mintGrant(t)

	deleted, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
	require.True(t, deleted, "revoking own grant must delete the row")

	// Dead immediately: the very next rotation attempt fails.
	_, err = f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.ErrorIs(t, err, errInvalidGrant,
		"a revoked refresh token must not be redeemable")

	// Revoking again is a silent no-op (already gone), still not an error.
	deletedAgain, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
	require.False(t, deletedAgain, "an already-revoked grant deletes nothing")
}

// TestRevokeGrantWrongClientIsNoOp is a negative control: presenting a valid
// token under a different client_id returns success but deletes nothing, so the
// grant stays usable. The endpoint must never let one client drop another's.
func TestRevokeGrantWrongClientIsNoOp(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	res := f.mintGrant(t)

	deleted, err := f.svc.RevokeGrant(f.ctx, res.RefreshToken, "some-other-client")
	require.NoError(t, err)
	require.False(t, deleted, "a token bound to another client must not be deleted")

	// The grant is untouched — it still rotates.
	_, err = f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err, "the grant must survive a wrong-client revoke attempt")
}

// TestRevokeGrantOtherUserIsNoOp is the cross-user negative control: a grant
// belonging to another user (via another registered client) is not deletable by
// a different client, and RFC 7009 still answers success (no oracle).
func TestRevokeGrantOtherUserIsNoOp(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	// A second user with their own registered client and grant.
	otherUser := models.NewUser("oauth-other@example.com")
	require.NoError(t, f.db.CreateUser(f.ctx, otherUser))

	otherClient, _, err := f.svc.RegisterClient(
		f.ctx, "Other Client", []string{"http://127.0.0.1:2/cb"},
		[]string{GrantAuthorizationCode, GrantRefreshToken}, []string{ScopeMCP}, true,
	)
	require.NoError(t, err)

	otherGrant := &AuthCodeGrant{
		ClientID:            otherClient.ClientID,
		UserUID:             otherUser.UID,
		OrgUID:              f.org.UID,
		OrgSlug:             f.org.Slug,
		RedirectURI:         "http://127.0.0.1:2/cb",
		Scope:               ScopeMCP,
		Resource:            oauthTestIssuer + "/api/v1/mcp",
		CodeChallenge:       challengeFor(testVerifier),
		CodeChallengeMethod: CodeChallengeMethodS256,
	}

	code, err := f.svc.IssueAuthCode(f.ctx, otherGrant)
	require.NoError(t, err)

	otherRes, err := f.svc.ExchangeAuthCode(f.ctx, code, otherClient.ClientID, "http://127.0.0.1:2/cb", testVerifier)
	require.NoError(t, err)

	// The fixture's default client tries to revoke the other user's grant.
	deleted, err := f.svc.RevokeGrant(f.ctx, otherRes.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
	require.False(t, deleted, "another user's grant must not be deletable by a different client")

	_, err = f.svc.ExchangeRefreshToken(f.ctx, otherRes.RefreshToken, otherClient.ClientID)
	require.NoError(t, err, "the other user's grant must survive")
}

// TestRevokeGrantRejectsNonOAuthTokens is the token-type negative control: a PAT
// or a session refresh token presented at /oauth/revoke is NOT deletable through
// this path, even when its client_id property happens to match — the wrong
// token type is refused before the client check.
func TestRevokeGrantRejectsNonOAuthTokens(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)

	for _, tokType := range []models.TokenType{models.TokenTypePAT, models.TokenTypeRefresh} {
		row := models.NewUserToken(f.user.UID, &f.org.UID, "value-"+string(tokType), tokType)
		// A matching client_id would delete an oauth_refresh row — prove the
		// type gate blocks these regardless.
		row.Properties = models.JSONMap{"client_id": f.client.ClientID}
		require.NoError(t, f.db.CreateUserToken(f.ctx, row))

		deleted, err := f.svc.RevokeGrant(f.ctx, row.Token, f.client.ClientID)
		require.NoError(t, err)
		require.False(t, deleted, "a %s token must not be revocable through /oauth/revoke", tokType)

		// The row is still live.
		got, err := f.db.GetUserTokenByToken(f.ctx, row.Token)
		require.NoError(t, err)
		require.Equal(t, row.UID, got.UID)
	}
}

// TestRevokeGrantEmptyInputsAreNoOp guards the guard clauses: a missing token or
// missing client_id never deletes anything (and never touches the DB in a way
// that could error).
func TestRevokeGrantEmptyInputsAreNoOp(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	res := f.mintGrant(t)

	deleted, err := f.svc.RevokeGrant(f.ctx, "", f.client.ClientID)
	require.NoError(t, err)
	require.False(t, deleted)

	deleted, err = f.svc.RevokeGrant(f.ctx, res.RefreshToken, "")
	require.NoError(t, err)
	require.False(t, deleted, "an empty client_id must not delete anything")

	// The grant is still live after both no-op calls.
	_, err = f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err)
}

// TestMintedAccessTokenCarriesGrantUID proves the grant-linking: the MCP access
// token minted alongside a grant carries that grant's row UID in
// Claims.RefreshUID (and rotation re-links to the new row).
func TestMintedAccessTokenCarriesGrantUID(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	res := f.mintGrant(t)

	row, err := f.db.GetUserTokenByToken(f.ctx, res.RefreshToken)
	require.NoError(t, err)

	claims, err := f.authSvc.ValidateToken(f.ctx, res.AccessToken)
	require.NoError(t, err)
	require.Equal(t, row.UID, claims.RefreshUID, "access token must be linked to its grant row")

	// Rotation mints a new row and re-links the new access token to it.
	rotated, err := f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err)

	newRow, err := f.db.GetUserTokenByToken(f.ctx, rotated.RefreshToken)
	require.NoError(t, err)

	rotatedClaims, err := f.authSvc.ValidateToken(f.ctx, rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, newRow.UID, rotatedClaims.RefreshUID, "rotation must re-link to the new grant row")
	require.NotEqual(t, row.UID, newRow.UID, "rotation must create a distinct row")
}

// TestBearerSelfRevocationKillsGrant covers the DELETE /tokens/current service
// path: revoking the row named by an access token's Claims.RefreshUID (as the
// bearer-only self-revocation handler does) kills the grant immediately.
func TestBearerSelfRevocationKillsGrant(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	res := f.mintGrant(t)

	claims, err := f.authSvc.ValidateToken(f.ctx, res.AccessToken)
	require.NoError(t, err)
	require.NotEmpty(t, claims.RefreshUID)

	// This is exactly what RevokeCurrentToken does: revoke claims.RefreshUID for
	// the authenticated user — works for a client that no longer holds the
	// refresh token itself.
	require.NoError(t, f.authSvc.RevokeToken(f.ctx, f.user.UID, claims.RefreshUID))

	_, err = f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.ErrorIs(t, err, errInvalidGrant, "the grant must be dead after bearer self-revocation")
}

// revokeHandler builds a Handler over the fixture's service for HTTP-surface
// tests.
func (f oauthFixture) revokeHandler() *Handler {
	return NewHandler(f.svc, &config.Config{
		Server: config.ServerConfig{BaseURL: oauthTestIssuer},
	})
}

// doRevoke posts a form-encoded body to the Revoke handler.
func doRevoke(t *testing.T, h *Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost, PathRevoke, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	require.NoError(t, h.Revoke(rec, req))

	return rec
}

// TestRevokeEndpointAlways200 asserts the RFC 7009 no-oracle guarantee at the
// HTTP layer: unknown, valid-then-revoked, and wrong-client tokens all return
// 200 with an empty body, indistinguishable from each other.
func TestRevokeEndpointAlways200(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	h := f.revokeHandler()
	res := f.mintGrant(t)

	// Unknown token → 200.
	rec := doRevoke(t, h, url.Values{"token": {"never-existed"}, "client_id": {f.client.ClientID}})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())

	// Wrong client → 200, grant untouched.
	rec = doRevoke(t, h, url.Values{"token": {res.RefreshToken}, "client_id": {"other"}})
	require.Equal(t, http.StatusOK, rec.Code)
	_, err := f.svc.ExchangeRefreshToken(f.ctx, res.RefreshToken, f.client.ClientID)
	require.NoError(t, err, "wrong-client HTTP revoke must not delete the grant")

	// Correct revoke → 200, then dead.
	res2 := f.mintGrant(t)
	rec = doRevoke(t, h, url.Values{"token": {res2.RefreshToken}, "client_id": {f.client.ClientID}})
	require.Equal(t, http.StatusOK, rec.Code)
	_, err = f.svc.ExchangeRefreshToken(f.ctx, res2.RefreshToken, f.client.ClientID)
	require.ErrorIs(t, err, errInvalidGrant)

	// Already revoked → still 200 (no oracle).
	rec = doRevoke(t, h, url.Values{"token": {res2.RefreshToken}, "client_id": {f.client.ClientID}})
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestRevokeEndpointExpiryHeaders confirms the revoke response is uncacheable
// (RFC 7009 responses, like token responses, must not be cached).
func TestRevokeEndpointExpiryHeaders(t *testing.T) {
	t.Parallel()

	f := setupOAuthService(t)
	h := f.revokeHandler()

	rec := doRevoke(t, h, url.Values{"token": {"x"}, "client_id": {"y"}})
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
