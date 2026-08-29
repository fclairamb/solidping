package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// decodeErrorResponse decodes the standard {title, code, detail} error body.
func decodeErrorResponse(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()

	var payload struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	return payload.Code, payload.Detail
}

// TestRegisterHandlerTooShortPasswordReturnsValidationError is the regression
// test for the bug: POST /auth/register with a password shorter than
// minPasswordLength used to fall through handleRegistrationError's default
// case into WriteInternalError, answering 500 INTERNAL_ERROR (and paging
// Sentry) for what is really a 400 client validation error. It must now come
// back as 400 VALIDATION_ERROR through the real Register HTTP handler.
func TestRegisterHandlerTooShortPasswordReturnsValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	// Registration is disabled by default in the test fixture (empty
	// RegistrationEmailPattern); enable it via the overlay path so Register
	// reaches the password-length check instead of ErrRegistrationDisabled.
	svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	handler := NewHandler(svc, &config.Config{})

	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"name":"New User","email":"newuser@example.com","password":"short"}`))
	rec := httptest.NewRecorder()

	r.NoError(handler.Register(rec, httpReq))
	r.Equal(http.StatusBadRequest, rec.Code)

	code, detail := decodeErrorResponse(t, rec)
	r.Equal(string(base.ErrorCodeValidationError), code)
	r.Contains(detail, "8 characters")
}

// TestAcceptInviteHandlerTooShortPasswordReturnsValidationError pins the
// same gap in handleInvitationError: AcceptInvite for a brand-new user (no
// existing account, so it hits the same minPasswordLength check as Register)
// with a too-short password must return 400 VALIDATION_ERROR, not 500.
func TestAcceptInviteHandlerTooShortPasswordReturnsValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

	org := models.NewOrganization("invite-shortpw", "Invite Short PW")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	inviter := models.NewUser("inviter-shortpw@example.com")
	r.NoError(dbSvc.CreateUser(ctx, inviter))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, inviter.UID, models.MemberRoleAdmin)))

	inviteResp, err := svc.CreateInvitation(ctx, "invite-shortpw", inviter.UID, InviteRequest{
		Email:     "newinvitee@example.com",
		Role:      "user",
		ExpiresIn: "24h",
		App:       "dash0",
	})
	r.NoError(err)

	handler := NewHandler(svc, &config.Config{})

	body := `{"token":"` + inviteResp.Token + `","name":"New Invitee","password":"short"}`
	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/accept-invite",
		strings.NewReader(body))
	rec := httptest.NewRecorder()

	r.NoError(handler.AcceptInvite(rec, httpReq))
	r.Equal(http.StatusBadRequest, rec.Code)

	code, detail := decodeErrorResponse(t, rec)
	r.Equal(string(base.ErrorCodeValidationError), code)
	r.Contains(detail, "8 characters")
}

// TestHandleAuthErrorMapsUserNotFoundLikeInvalidCredentials pins the sweep
// fix: SwitchOrg can return ErrUserNotFound (the caller's own user row is
// gone), and handleAuthError must fold it into the same anti-enumeration
// 401 as ErrInvalidCredentials/ErrOrganizationNotFound rather than 500ing.
func TestHandleAuthErrorMapsUserNotFoundLikeInvalidCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/switch-org", nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.handleAuthError(rec, httpReq, ErrUserNotFound))
	r.Equal(http.StatusUnauthorized, rec.Code)

	code, _ := decodeErrorResponse(t, rec)
	r.Equal(string(base.ErrorCodeInvalidCredentials), code)
}

// TestHandleRefreshErrorMapsUserNotFoundToInvalidToken pins the sweep fix:
// Refresh can return ErrUserNotFound when the user behind an otherwise-valid
// refresh token was deleted; handleRefreshError must treat it like any other
// invalid token rather than 500ing.
func TestHandleRefreshErrorMapsUserNotFoundToInvalidToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/refresh", nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.handleRefreshError(rec, httpReq, ErrUserNotFound))
	r.Equal(http.StatusUnauthorized, rec.Code)

	code, _ := decodeErrorResponse(t, rec)
	r.Equal(string(base.ErrorCodeInvalidToken), code)
}

// TestHandle2FAErrorMapsOrganizationNotFound pins the sweep fix:
// completeLoginAfter2FA (reached via Verify2FA/Recovery2FA) can return
// ErrOrganizationNotFound when the temp token's org no longer resolves;
// handle2FAError must map it instead of falling through to 500.
func TestHandle2FAErrorMapsOrganizationNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)
	handler := NewHandler(svc, &config.Config{})

	httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/2fa/verify", nil)
	rec := httptest.NewRecorder()

	r.NoError(handler.handle2FAError(rec, httpReq, ErrOrganizationNotFound))
	r.Equal(http.StatusUnauthorized, rec.Code)

	code, _ := decodeErrorResponse(t, rec)
	r.Equal(string(base.ErrorCodeOrganizationNotFound), code)
}
