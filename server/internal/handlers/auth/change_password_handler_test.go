package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// changePasswordFixture seeds an org plus a password user that belongs to it,
// so handler tests can log in for real claims.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func changePasswordFixture(
	t *testing.T, ctx context.Context, dbSvc db.Service, slug, email, password string,
) {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization(slug, slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash(password)
	r.NoError(err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))
}

//nolint:revive // ctx-second matches the existing helpers in this package
func postChangePassword(
	t *testing.T, ctx context.Context, handler *Handler, claims *Claims, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx,
		http.MethodPost, "/api/v1/auth/change-password", strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.ChangePassword(rec, httpReq))

	return rec
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var payload struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	return payload.Code
}

// TestChangePasswordHandlerKeepsCallerSessionAndKillsOthers is the end-to-end
// proof of spec step 7: after rotating the password from session A, session A
// can still refresh and session B cannot.
func TestChangePasswordHandlerKeepsCallerSessionAndKillsOthers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	changePasswordFixture(t, ctx, dbSvc, "cp-sessions", "cp-sessions@example.com", "testpass1234")

	sessionA, err := svc.Login(ctx, "cp-sessions", "cp-sessions@example.com", "testpass1234", Context{})
	r.NoError(err)
	sessionB, err := svc.Login(ctx, "cp-sessions", "cp-sessions@example.com", "testpass1234", Context{})
	r.NoError(err)

	claimsA, err := svc.ValidateToken(ctx, sessionA.AccessToken)
	r.NoError(err)
	r.NotEmpty(claimsA.RefreshUID)

	handler := NewHandler(svc, &config.Config{})
	rec := postChangePassword(t, ctx, handler, claimsA,
		`{"currentPassword":"testpass1234","newPassword":"testpass5678"}`)
	r.Equal(http.StatusOK, rec.Code)

	// Caller's session survives.
	_, err = svc.Refresh(ctx, sessionA.RefreshToken)
	r.NoError(err)

	// The other session is gone.
	_, err = svc.Refresh(ctx, sessionB.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)

	// And the new password is the one that logs in now.
	_, err = svc.Login(ctx, "cp-sessions", "cp-sessions@example.com", "testpass5678", Context{})
	r.NoError(err)

	_, err = svc.Login(ctx, "cp-sessions", "cp-sessions@example.com", "testpass1234", Context{})
	r.ErrorIs(err, ErrInvalidCredentials)
}

func TestChangePasswordHandlerErrorMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{
			name:     "wrong current password maps to INVALID_CURRENT_PASSWORD",
			body:     `{"currentPassword":"wrongwrong","newPassword":"testpass5678"}`,
			wantCode: http.StatusUnauthorized,
			wantErr:  string(base.ErrorCodeInvalidCurrentPassword),
		},
		{
			name:     "missing current password maps to INVALID_CURRENT_PASSWORD",
			body:     `{"newPassword":"testpass5678"}`,
			wantCode: http.StatusUnauthorized,
			wantErr:  string(base.ErrorCodeInvalidCurrentPassword),
		},
		{
			name:     "short new password maps to VALIDATION_ERROR",
			body:     `{"currentPassword":"testpass1234","newPassword":"short"}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(base.ErrorCodeValidationError),
		},
		{
			name:     "unchanged password maps to VALIDATION_ERROR",
			body:     `{"currentPassword":"testpass1234","newPassword":"testpass1234"}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(base.ErrorCodeValidationError),
		},
		{
			name:     "empty new password is rejected before the service",
			body:     `{"currentPassword":"testpass1234","newPassword":""}`,
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  string(base.ErrorCodeValidationError),
		},
		{
			name:     "malformed JSON is rejected",
			body:     `{`,
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  string(base.ErrorCodeValidationError),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, ctx := setupAuthTestService(t)
			slug := "cp-" + strings.ReplaceAll(strings.Fields(tc.name)[0], " ", "")
			email := slug + "@example.com"
			changePasswordFixture(t, ctx, dbSvc, slug, email, "testpass1234")

			login, err := svc.Login(ctx, slug, email, "testpass1234", Context{})
			r.NoError(err)
			claims, err := svc.ValidateToken(ctx, login.AccessToken)
			r.NoError(err)

			handler := NewHandler(svc, &config.Config{})
			rec := postChangePassword(t, ctx, handler, claims, tc.body)
			r.Equal(tc.wantCode, rec.Code)
			r.Equal(tc.wantErr, decodeErrorCode(t, rec))
		})
	}
}

// TestChangePasswordHandlerRequiresAuthentication pins the unauthenticated
// path: no claims in context → 401, never a panic or a silent rotation.
func TestChangePasswordHandlerRequiresAuthentication(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/change-password",
		strings.NewReader(`{"currentPassword":"a","newPassword":"testpass5678"}`))
	rec := httptest.NewRecorder()

	r.NoError(handler.ChangePassword(rec, httpReq))
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestChangePasswordHandlerRateLimitMapsTo429 verifies the brute-force guard
// surfaces as a 429 RATE_LIMITED rather than another INVALID_CURRENT_PASSWORD.
func TestChangePasswordHandlerRateLimitMapsTo429(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	changePasswordFixture(t, ctx, dbSvc, "cp-rl", "cp-rl@example.com", "testpass1234")

	login, err := svc.Login(ctx, "cp-rl", "cp-rl@example.com", "testpass1234", Context{})
	r.NoError(err)
	claims, err := svc.ValidateToken(ctx, login.AccessToken)
	r.NoError(err)

	handler := NewHandler(svc, &config.Config{})

	for i := 0; i < changePasswordMaxPerUser; i++ {
		rec := postChangePassword(t, ctx, handler, claims,
			`{"currentPassword":"badguess1234","newPassword":"testpass5678"}`)
		r.Equal(http.StatusUnauthorized, rec.Code)
	}

	rec := postChangePassword(t, ctx, handler, claims,
		`{"currentPassword":"badguess1234","newPassword":"testpass5678"}`)
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal(string(base.ErrorCodeRateLimited), decodeErrorCode(t, rec))
}
