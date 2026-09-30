package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestEmailChangeRoutes drives the email-change surface of spec 2026-09-30-08
// through the real route table and middleware chain.
func TestEmailChangeRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)
	ctx := context.Background()

	hash, err := passwords.Hash("memberpass123")
	r.NoError(err)
	r.NoError(env.server.dbService.UpdateUser(ctx, env.member.UID, &models.UserUpdate{PasswordHash: &hash}))

	memberPath := "/api/v1/system/users/" + env.member.UID

	// A non-super-admin cannot reach the system endpoint.
	status, _ := env.do(http.MethodPatch, memberPath, env.memberToken, `{"email":"x@acme.com"}`)
	r.Equal(http.StatusForbidden, status)

	// An impersonation token cannot change the target's email.
	status, body := env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{"orgSlug":"impa"}`)
	r.Equal(http.StatusOK, status)
	impToken, ok := body["accessToken"].(string)
	r.True(ok)

	status, body = env.do(http.MethodPatch, "/api/v1/auth/me", impToken,
		`{"email":"hijack@acme.com","currentPassword":"memberpass123"}`)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])

	// The user changes their own email with their password.
	status, body = env.do(http.MethodPatch, "/api/v1/auth/me", env.memberToken,
		`{"email":"alice2@acme.com","currentPassword":"memberpass123"}`)
	r.Equal(http.StatusOK, status, body)

	status, _ = env.do(http.MethodPost, "/api/v1/auth/login", "",
		`{"org":"impa","email":"alice2@acme.com","password":"memberpass123"}`)
	r.Equal(http.StatusOK, status, "the new address signs in")

	status, _ = env.do(http.MethodPost, "/api/v1/auth/login", "",
		`{"org":"impa","email":"alice@acme.com","password":"memberpass123"}`)
	r.Equal(http.StatusUnauthorized, status, "the old address no longer signs in")

	// The super admin changes it back without the member's password.
	status, body = env.do(http.MethodPatch, memberPath, env.adminToken, `{"email":"alice3@acme.com"}`)
	r.Equal(http.StatusOK, status, body)
	r.Equal("alice3@acme.com", body["email"])

	// A duplicate address is a conflict.
	status, body = env.do(http.MethodPatch, memberPath, env.adminToken, `{"email":"Root@acme.com"}`)
	r.Equal(http.StatusConflict, status)
	r.Equal(string(base.ErrorCodeConflict), body["code"])
}
