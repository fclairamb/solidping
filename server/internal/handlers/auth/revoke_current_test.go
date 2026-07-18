package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestRevokeCurrentTokenRevokesOwnSession verifies DELETE /tokens/current drops
// the grant named by the caller's own Claims.RefreshUID: the session is dead
// immediately (the next refresh fails).
func TestRevokeCurrentTokenRevokesOwnSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("revcur-org", "RevCur Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("revcur@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := svc.Login(ctx, "revcur-org", "revcur@example.com", "testpass1234", Context{})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, loginResp.AccessToken)
	r.NoError(err)
	r.NotEmpty(claims.RefreshUID)

	handler := NewHandler(svc, &config.Config{})

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodDelete, "/api/v1/auth/tokens/current", http.NoBody)
	rec := httptest.NewRecorder()

	r.NoError(handler.RevokeCurrentToken(rec, bunrouter.Request{Request: httpReq}))
	r.Equal(http.StatusNoContent, rec.Code)

	// The grant is dead: refreshing with the now-revoked token fails immediately.
	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)
}

// TestRevokeCurrentTokenRejectsCredentialWithoutGrant verifies a credential with
// no backing grant row (PAT / 2FA temp token → empty RefreshUID) gets a 400
// rather than a misleading not-found.
func TestRevokeCurrentTokenRejectsCredentialWithoutGrant(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	claims := &Claims{UserUID: "user-uid"} // no RefreshUID
	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodDelete, "/api/v1/auth/tokens/current", http.NoBody)
	rec := httptest.NewRecorder()

	r.NoError(handler.RevokeCurrentToken(rec, bunrouter.Request{Request: httpReq}))
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}
