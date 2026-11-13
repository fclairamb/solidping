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
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// errorBody decodes the code of an error response.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())

	return body
}

// TestServiceErrorsMapToHTTP pins the status and machine code each handler
// family answers for the service errors that reach it. The API clients switch
// on these codes.
func TestServiceErrorsMapToHTTP(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, &config.Config{})

	type mapper func(http.ResponseWriter, *http.Request, error) error

	// The status of a token error is the caller's choice.
	tokenCreate := func(w http.ResponseWriter, r *http.Request, err error) error {
		return h.handleTokenError(w, r, err, http.StatusForbidden)
	}
	emailChange := func(w http.ResponseWriter, r *http.Request, err error) error {
		handled, writeErr := h.handleEmailChangeError(w, r, err)
		if !handled {
			w.WriteHeader(http.StatusTeapot)
		}

		return writeErr
	}

	tests := []struct {
		name     string
		mapper   mapper
		err      error
		want     int
		wantCode base.ErrorCode
	}{
		{
			"login: personal access token refused", h.handleAuthError, ErrPATForbidden,
			http.StatusForbidden, base.ErrorCodePATForbidden,
		},
		{
			"login: unexpected", h.handleAuthError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"logout: user gone", h.handleLogoutError, ErrUserNotFound,
			http.StatusNotFound, base.ErrorCodeUserNotFound,
		},
		{
			"logout: organization gone", h.handleLogoutError, ErrOrganizationNotFound,
			http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
		},
		{
			"logout: unexpected", h.handleLogoutError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"refresh: organization gone", h.handleRefreshError, ErrOrganizationNotFound,
			http.StatusUnauthorized, base.ErrorCodeOrganizationNotFound,
		},
		{
			"refresh: unexpected", h.handleRefreshError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"me: organization gone", h.handleUserInfoError, ErrOrganizationNotFound,
			http.StatusUnauthorized, base.ErrorCodeOrganizationNotFound,
		},
		{
			"tokens: user gone", tokenCreate, ErrUserNotFound,
			http.StatusForbidden, base.ErrorCodeUserNotFound,
		},
		{
			"revoke: user gone", h.handleRevokeError, ErrUserNotFound,
			http.StatusNotFound, base.ErrorCodeUserNotFound,
		},
		{
			"revoke: organization gone", h.handleRevokeError, ErrOrganizationNotFound,
			http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
		},
		{
			"change password: user gone", h.handleChangePasswordError, ErrUserNotFound,
			http.StatusNotFound, base.ErrorCodeUserNotFound,
		},
		{
			"change password: unexpected", h.handleChangePasswordError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"reset password: demo account", h.handlePasswordResetError, ErrDemoAccountNotResettable,
			http.StatusForbidden, base.ErrorCodeDemoReadOnly,
		},
		{
			"reset password: password too short", h.handlePasswordResetError, ErrInvalidCredentials,
			http.StatusBadRequest, base.ErrorCodeValidationError,
		},
		{
			"reset password: unexpected", h.handlePasswordResetError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"organization profile: invalid name", h.writeOrgProfileError, ErrInvalidOrgName,
			http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
		},
		{
			"organization settings: foreign escalation policy", h.writeUpdateOrgSettingsError, ErrInvalidEscalationPolicy,
			http.StatusBadRequest, base.ErrorCodeValidationError,
		},
		{
			"organization settings: invalid sealing mode", h.writeUpdateOrgSettingsError, sealing.ErrInvalidMode,
			http.StatusBadRequest, base.ErrorCodeValidationError,
		},
		{
			"organization settings: invalid reminder interval", h.writeUpdateOrgSettingsError, ErrInvalidReminderInterval,
			http.StatusBadRequest, base.ErrorCodeValidationError,
		},
		{
			"registration: address not allowed", h.handleRegistrationError, ErrEmailNotAllowed,
			http.StatusForbidden, base.ErrorCodeEmailNotAllowed,
		},
		{
			"registration: address taken", h.handleRegistrationError, ErrEmailAlreadyTaken,
			http.StatusConflict, base.ErrorCodeConflict,
		},
		{
			"registration: unexpected", h.handleRegistrationError, errAcmeExploded,
			http.StatusInternalServerError, base.ErrorCodeInternalError,
		},
		{
			"invitation: expired", h.handleInvitationError, ErrInvitationExpired,
			http.StatusGone, base.ErrorCodeInvitationExpired,
		},
		{
			"invitation: too many pending", h.handleInvitationError, ErrTooManyInvitations,
			http.StatusConflict, base.ErrorCodeTooManyInvitations,
		},
		{
			"invitation: seat cap reached", h.handleInvitationError, entitlements.ErrEntitlementExceeded,
			http.StatusForbidden, base.ErrorCodeEntitlementExceeded,
		},
		{
			"2FA: wrong recovery code", h.handle2FAError, ErrInvalidRecoveryCode,
			http.StatusUnauthorized, base.ErrorCodeInvalidRecoveryCode,
		},
		{
			"email change: rate limited", emailChange, ErrRateLimited,
			http.StatusTooManyRequests, base.ErrorCodeRateLimited,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			rec := httptest.NewRecorder()

			require.NoError(t, tt.mapper(rec, req, tt.err))
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			require.Equal(t, string(tt.wantCode), errorBody(t, rec)["code"])
		})
	}
}

// handlerWorld puts the HTTP handlers in front of an edgeWorld. Its users
// hold sessions without a password, which keeps the fixture cheap.
type handlerWorld struct {
	*edgeWorld

	http *coverFixture
}

func newHandlerWorld(t *testing.T) *handlerWorld {
	t.Helper()

	w := newEdgeWorld(t)

	return &handlerWorld{
		edgeWorld: w,
		http:      &coverFixture{svc: w.svc, h: NewHandler(w.svc, &config.Config{}), ctx: w.ctx},
	}
}

// signedInClaims seeds a member and returns their slug and session claims.
func (w *handlerWorld) signedInClaims(t *testing.T, role models.MemberRole) (string, *Claims) {
	t.Helper()

	slug, _, session := w.signedIn(t, role)

	claims, err := w.svc.ValidateToken(w.ctx, session.AccessToken)
	require.NoError(t, err)

	return slug, claims
}

// TestLogoutVariants covers the two bulk forms of POST /auth/logout.
func TestLogoutVariants(t *testing.T) {
	t.Parallel()

	w := newHandlerWorld(t)
	f := w.http
	slug, caller := w.signedInClaims(t, models.MemberRoleAdmin)
	ghost := &Claims{UserUID: uuidV7(t), RefreshUID: uuidV7(t)}

	// Sign out everywhere, user gone.
	{
		rec := f.do(t, coverCall{fn: f.h.Logout, claims: ghost, body: `{"deleteAllTokens":true}`})
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, string(base.ErrorCodeUserNotFound), errorBody(t, rec)["code"])
	}

	// Sign out other sessions, user gone.
	{
		rec := f.do(t, coverCall{fn: f.h.Logout, claims: ghost, body: `{"signOutOthers":true}`})
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, string(base.ErrorCodeUserNotFound), errorBody(t, rec)["code"])
	}

	// Sign out other sessions keeps the caller's.
	{
		r := require.New(t)

		// A second session of the same user, on another device.
		_, err := w.svc.SwitchOrg(w.ctx, caller.UserUID, slug, edgeAuthContext)
		r.NoError(err)
		r.Equal(2, w.sessionCount(t, caller.UserUID))

		rec := f.do(t, coverCall{fn: f.h.Logout, claims: caller, body: `{"signOutOthers":true}`})
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())
		r.InDelta(1, errorBody(t, rec)["tokensDeleted"], 0)
		r.Empty(rec.Header().Get("Set-Cookie"), "the caller's cookie is left alone")

		sessions, err := w.dbSvc.ListUserTokensByType(w.ctx, caller.UserUID, models.TokenTypeRefresh)
		r.NoError(err)
		r.Len(sessions, 1)
		r.Equal(caller.RefreshUID, sessions[0].UID)
	}
}

// TestHandlersAnswer500OnAStoreFailure: a store failure is an INTERNAL_ERROR,
// not a validation error or an empty success.
func TestHandlersAnswer500OnAStoreFailure(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	w := newHandlerWorld(t)
	f, fault := w.http, w.fault
	slug, admin := w.signedInClaims(t, models.MemberRoleOwner)
	w.promote(t, admin.UserUID)
	_, member, _ := w.signedIn(t, models.MemberRoleUser)
	w.withPassword(t, member) // an account without a password gets no reset link

	tests := []struct {
		name  string
		call  coverCall
		fault sqlFault
	}{
		{
			"admin user update",
			coverCall{fn: f.h.AdminUpdateUser, claims: admin, params: map[string]string{"uid": member.UID}, body: `{}`},
			failingSQL(`FROM "users"`).after(1),
		},
		{
			"password reset request",
			coverCall{fn: f.h.RequestPasswordReset, body: `{"email":"` + member.Email + `"}`},
			failingSQL(`"state_entries"`, `'{"emailHash"`),
		},
		{
			"organization deletion",
			coverCall{
				fn: f.h.DeleteOrg, claims: admin, params: map[string]string{fieldOrg: slug}, body: `{"slug":"` + slug + `"}`,
			},
			failingSQL(`FROM "checks"`),
		},
	}
	for _, tt := range tests { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tt.name, func(t *testing.T) {
			var rec *httptest.ResponseRecorder

			fault.during(t, tt.fault, func() { rec = f.do(t, tt.call) })
			require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			require.Equal(t, string(base.ErrorCodeInternalError), errorBody(t, rec)["code"])
		})
	}

	// Deletion of an unknown organization is a 404.
	{
		rec := f.do(t, coverCall{
			fn: f.h.DeleteOrg, claims: admin, params: map[string]string{fieldOrg: "no-such-org"},
			body: `{"slug":"no-such-org"}`,
		})
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, string(base.ErrorCodeOrganizationNotFound), errorBody(t, rec)["code"])
	}
}

// TestPasswordResetRequestsAreRateLimitedPerAddress: past the per-IP budget
// the endpoint answers 429, whatever the email.
func TestPasswordResetRequestsAreRateLimitedPerAddress(t *testing.T) {
	t.Parallel()

	f := newHandlerWorld(t).http
	call := coverCall{
		fn: f.h.RequestPasswordReset, body: `{"email":"nobody@acme.com"}`,
		header: map[string]string{"X-Forwarded-For": "203.0.113.7"},
	}

	for range passwordResetMaxPerIP {
		require.Equal(t, http.StatusOK, f.do(t, call).Code)
	}

	rec := f.do(t, call)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, string(base.ErrorCodeRateLimited), errorBody(t, rec)["code"])
}

// TestSecondFactorEndpointsMintTheSessionCookie: both second-step endpoints
// answer the session and set the access-token cookie, like a password login.
func TestSecondFactorEndpointsMintTheSessionCookie(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(t *testing.T, w *twoFAWorld, h *Handler) coverCall
	}{
		{"TOTP code", func(t *testing.T, w *twoFAWorld, h *Handler) coverCall {
			t.Helper()

			return coverCall{fn: h.Verify2FA, body: `{"code":"` + w.code(t) + `"}`}
		}},
		{"recovery code", func(t *testing.T, _ *twoFAWorld, h *Handler) coverCall {
			t.Helper()

			return coverCall{fn: h.Recovery2FA, body: `{"recoveryCode":"recovery-one"}`}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			w := newTwoFAWorld(t)
			h := NewHandler(w.svc, &config.Config{})
			fixture := &coverFixture{svc: w.svc, h: h, ctx: w.ctx}

			call := tt.call(t, w, h)
			call.header = map[string]string{"Authorization": "Bearer " + w.temp}

			rec := fixture.do(t, call)
			r.Equal(http.StatusOK, rec.Code, rec.Body.String())

			var resp LoginResponse
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
			r.NotEmpty(resp.RefreshToken)
			r.Equal(w.slug, resp.Organization.Slug)
			r.Equal(resp.AccessToken, readSetCookie(t, rec).Value)
		})
	}
}

// TestDeviceFlowErrorsMapToHTTP pins the answers of the device authorization
// endpoints. The token endpoint speaks RFC 8628: its errors are OAuth error
// codes in a 400, which is what the CLI polling it switches on.
func TestDeviceFlowErrorsMapToHTTP(t *testing.T) {
	t.Parallel()

	h := NewHandler(nil, &config.Config{})

	tests := []struct {
		name   string
		mapper func(http.ResponseWriter, *http.Request, error) error
		err    error
		want   int
		field  string
		value  string
	}{
		{
			"token: denied", h.writeDeviceTokenError, ErrDeviceAccessDenied,
			http.StatusBadRequest, "error", "access_denied",
		},
		{
			"token: unexpected", h.writeDeviceTokenError, errAcmeExploded,
			http.StatusInternalServerError, "code", string(base.ErrorCodeInternalError),
		},
		{
			"consent: personal access token refused", h.writeDeviceConsentError, ErrPATForbidden,
			http.StatusForbidden, "code", string(base.ErrorCodePATForbidden),
		},
		{
			"consent: request gone", h.writeDeviceConsentError, ErrDeviceAuthNotFound,
			http.StatusNotFound, "code", string(base.ErrorCodeNotFound),
		},
		{
			"consent: organization gone", h.writeDeviceConsentError, ErrOrganizationNotFound,
			http.StatusNotFound, "code", string(base.ErrorCodeOrganizationNotFound),
		},
		{
			"consent: not a member of the organization", h.writeDeviceConsentError, ErrUserNotFound,
			http.StatusForbidden, "code", string(base.ErrorCodeForbidden),
		},
		{
			"consent: unexpected", h.writeDeviceConsentError, errAcmeExploded,
			http.StatusInternalServerError, "code", string(base.ErrorCodeInternalError),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			rec := httptest.NewRecorder()

			require.NoError(t, tt.mapper(rec, req, tt.err))
			require.Equal(t, tt.want, rec.Code, rec.Body.String())
			require.Equal(t, tt.value, errorBody(t, rec)[tt.field])
		})
	}
}

// TestStartDeviceAuthorizationRefusals covers the two ways the first step of
// the device flow fails.
func TestStartDeviceAuthorizationRefusals(t *testing.T) {
	t.Parallel()

	w := newHandlerWorld(t)
	f, fault := w.http, w.fault

	// Client name too long.
	{
		rec := f.do(t, coverCall{
			fn: f.h.StartDeviceAuthorization, body: `{"clientName":"` + strings.Repeat("a", 500) + `"}`,
		})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		require.Equal(t, string(base.ErrorCodeValidationError), errorBody(t, rec)["code"])
	}

	// Request cannot be stored.
	{
		var rec *httptest.ResponseRecorder

		fault.during(t, failingSQL(`"device_auth_requests"`), func() {
			rec = f.do(t, coverCall{fn: f.h.StartDeviceAuthorization, body: `{"clientName":"acme-cli"}`})
		})
		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		require.Equal(t, string(base.ErrorCodeInternalError), errorBody(t, rec)["code"])
	}

	// Control: a request is opened.
	{
		rec := f.do(t, coverCall{fn: f.h.StartDeviceAuthorization, body: `{"clientName":"acme-cli"}`})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotEmpty(t, errorBody(t, rec)["device_code"])
	}
}
