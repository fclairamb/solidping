package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Spec 2026-10-13-02: the forced first-login password screen also sets a real
// email while the seeded admin still signs in with defaults.Email.

const (
	firstLoginOrg         = "first-login"
	firstLoginNewPassword = "brandnewpassword"
)

type firstLoginFixture struct {
	svc      *Service
	db       db.Service
	handler  *Handler
	org      *models.Organization
	user     *models.User
	sessionA *LoginResponse
	sessionB *LoginResponse
	claimsA  *Claims
}

// newFirstLoginFixture seeds the shape the startup job seeds: a user with
// must_change_password on the given address, in one org, logged in twice. The
// service has a real job queue, so enqueued emails can be inspected.
func newFirstLoginFixture(t *testing.T, address string, mustChange bool) (*firstLoginFixture, context.Context) {
	t.Helper()
	r := require.New(t)

	fullCfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Email: config.EmailConfig{Enabled: true},
	}
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, fullCfg)
	changePasswordFixture(t, ctx, dbSvc, firstLoginOrg, address)

	user, err := dbSvc.GetUserByEmail(ctx, address)
	r.NoError(err)

	verifiedAt := time.Now()
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{
		EmailVerifiedAt: &verifiedAt, MustChangePassword: &mustChange,
	}))

	org, err := dbSvc.GetOrganizationBySlug(ctx, firstLoginOrg)
	r.NoError(err)

	sessionA, err := svc.Login(ctx, firstLoginOrg, address, emailChangePassword, Context{})
	r.NoError(err)
	sessionB, err := svc.Login(ctx, firstLoginOrg, address, emailChangePassword, Context{})
	r.NoError(err)

	claimsA, err := svc.ValidateToken(ctx, sessionA.AccessToken)
	r.NoError(err)

	user, err = dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)

	return &firstLoginFixture{
		svc: svc, db: dbSvc, handler: NewHandler(svc, fullCfg), org: org, user: user,
		sessionA: sessionA, sessionB: sessionB, claimsA: claimsA,
	}, ctx
}

// emailJobRecipients returns the recipients of every enqueued email job using
// the template.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func emailJobRecipients(t *testing.T, ctx context.Context, dbSvc db.Service, template string) []string {
	t.Helper()

	jobs, err := dbSvc.ListJobs(ctx, nil, 0)
	require.NoError(t, err)

	var recipients []string

	for _, job := range jobs {
		if job.Type != string(jobdef.JobTypeEmail) {
			continue
		}

		if got, _ := job.Config["template"].(string); got != template {
			continue
		}

		to, _ := job.Config["to"].([]any)
		for _, addr := range to {
			if s, ok := addr.(string); ok {
				recipients = append(recipients, s)
			}
		}
	}

	return recipients
}

//nolint:revive // ctx-second matches the existing helpers in this package
func httpGetMe(t *testing.T, ctx context.Context, handler *Handler, claims *Claims) *httptest.ResponseRecorder {
	t.Helper()

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()

	require.NoError(t, handler.Me(rec, httpReq))

	return rec
}

func TestMustChangeEmailSignal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		email      string
		mustChange bool
		want       bool
	}{
		{"seeded default address", defaults.Email, true, true},
		{"default address in another case", strings.ToUpper(defaults.Email), true, true},
		{"seed made with SP_ADMIN_EMAIL", "ops@acme.com", true, false},
		{"default address already rotated", defaults.Email, false, false},
		{"ordinary user", "alice@acme.com", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			user := &models.User{Email: tc.email, MustChangePassword: tc.mustChange}
			require.Equal(t, tc.want, mustChangeEmail(user))
			require.Equal(t, tc.want, newUserInfo(user, "owner").MustChangeEmail)
		})
	}

	require.False(t, mustChangeEmail(nil))
}

// TestMustChangeEmailOnLoginAndMe: the flag rides on the login response and on
// /auth/me for the seeded default, and is absent for an SP_ADMIN_EMAIL seed.
func TestMustChangeEmailOnLoginAndMe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		email string
		want  bool
	}{
		{"default seed", defaults.Email, true},
		{"SP_ADMIN_EMAIL seed", "ops@acme.com", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, ctx := newFirstLoginFixture(t, tc.email, true)
			r.True(f.sessionA.User.MustChangePassword)
			r.Equal(tc.want, f.sessionA.User.MustChangeEmail)

			me, err := f.svc.GetUserInfo(ctx, f.claimsA)
			r.NoError(err)
			r.Equal(tc.want, me.User.MustChangeEmail)

			rec := httpGetMe(t, ctx, f.handler, f.claimsA)
			r.Equal(http.StatusOK, rec.Code, rec.Body.String())

			if tc.want {
				r.Contains(rec.Body.String(), `"mustChangeEmail":true`)
			} else {
				r.NotContains(rec.Body.String(), `mustChangeEmail`)
			}
		})
	}
}

// TestFirstLoginEmailHappyPath: password, email and the cleared flag land
// together, and every side effect follows the new address.
func TestFirstLoginEmailHappyPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newFirstLoginFixture(t, defaults.Email, true)

	rec := postChangePassword(t, ctx, f.handler, f.claimsA, `{"currentPassword":"`+emailChangePassword+
		`","newPassword":"`+firstLoginNewPassword+`","email":" Ops@Acme.com "}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	updated, err := f.db.GetUser(ctx, f.user.UID)
	r.NoError(err)
	r.Equal("ops@acme.com", updated.Email)
	r.False(updated.MustChangePassword)
	r.Nil(updated.EmailVerifiedAt, "a new address is unverified")
	r.True(passwords.Verify(firstLoginNewPassword, *updated.PasswordHash))

	// Login works with the new email and the new password, not with the old email.
	login, err := f.svc.Login(ctx, firstLoginOrg, "ops@acme.com", firstLoginNewPassword, Context{})
	r.NoError(err)
	r.False(login.User.MustChangeEmail)
	r.False(login.User.MustChangePassword)
	_, err = f.svc.Login(ctx, firstLoginOrg, defaults.Email, firstLoginNewPassword, Context{})
	r.ErrorIs(err, ErrInvalidCredentials)

	// The caller's session survives, the other one is revoked.
	_, err = f.svc.Refresh(ctx, f.sessionA.RefreshToken)
	r.NoError(err)
	_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)

	events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: f.org.UID,
		EventTypes:      []models.EventType{models.EventTypeAuthEmailChanged},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal(defaults.Email, events[0].Payload[auditKeyOldEmail])
	r.Equal("ops@acme.com", events[0].Payload[auditKeyNewEmail])
	r.Equal(emailChangedBySelf, events[0].Payload[auditKeyChangedBy])

	r.Empty(emailJobRecipients(t, ctx, f.db, email.TemplateEmailChanged),
		"nothing is mailed to the default address")
	r.Equal([]string{"ops@acme.com"}, emailJobRecipients(t, ctx, f.db, "password-changed.html"),
		"the confirmation follows the new address")
}

// TestFirstLoginEmailRefusals: every refusal leaves the email, the password and
// the flag untouched.
func TestFirstLoginEmailRefusals(t *testing.T) {
	t.Parallel()

	body := func(current, address string) string {
		return `{"currentPassword":"` + current + `","newPassword":"` + firstLoginNewPassword +
			`","email":"` + address + `"}`
	}

	cases := []struct {
		name       string
		seedEmail  string
		mustChange bool
		body       string
		setup      func(t *testing.T, ctx context.Context, f *firstLoginFixture)
		wantCode   int
		wantErr    base.ErrorCode
	}{
		{
			name: "wrong current password", seedEmail: defaults.Email, mustChange: true,
			body:     body("wrongwrong", "ops@acme.com"),
			wantCode: http.StatusUnauthorized, wantErr: base.ErrorCodeInvalidCurrentPassword,
		},
		{
			name: "the default address", seedEmail: defaults.Email, mustChange: true,
			body:     body(emailChangePassword, defaults.Email),
			wantCode: http.StatusBadRequest, wantErr: base.ErrorCodeValidationError,
		},
		{
			name: "the default address in another case", seedEmail: defaults.Email, mustChange: true,
			body:     body(emailChangePassword, strings.ToUpper(defaults.Email)),
			wantCode: http.StatusBadRequest, wantErr: base.ErrorCodeValidationError,
		},
		{
			name: "invalid format", seedEmail: defaults.Email, mustChange: true,
			body:     body(emailChangePassword, "not an email"),
			wantCode: http.StatusBadRequest, wantErr: base.ErrorCodeValidationError,
		},
		{
			name: "taken by another user in another case", seedEmail: defaults.Email, mustChange: true,
			body: body(emailChangePassword, "TAKEN@acme.com"),
			setup: func(t *testing.T, ctx context.Context, f *firstLoginFixture) {
				t.Helper()
				require.NoError(t, f.db.CreateUser(ctx, models.NewUser("taken@acme.com")))
			},
			wantCode: http.StatusConflict, wantErr: base.ErrorCodeConflict,
		},
		{
			name: "account already on a real address", seedEmail: "ops@acme.com", mustChange: true,
			body:     body(emailChangePassword, "other@acme.com"),
			wantCode: http.StatusBadRequest, wantErr: base.ErrorCodeValidationError,
		},
		{
			name: "default address without a forced rotation", seedEmail: defaults.Email, mustChange: false,
			body:     body(emailChangePassword, "ops@acme.com"),
			wantCode: http.StatusBadRequest, wantErr: base.ErrorCodeValidationError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, ctx := newFirstLoginFixture(t, tc.seedEmail, tc.mustChange)
			if tc.setup != nil {
				tc.setup(t, ctx, f)
			}

			rec := postChangePassword(t, ctx, f.handler, f.claimsA, tc.body)
			r.Equal(tc.wantCode, rec.Code, rec.Body.String())
			r.Equal(string(tc.wantErr), decodeErrorCode(t, rec))

			user, err := f.db.GetUser(ctx, f.user.UID)
			r.NoError(err)
			r.Equal(f.user.Email, user.Email, "a refused call leaves the address untouched")
			r.NotNil(user.EmailVerifiedAt)
			r.Equal(tc.mustChange, user.MustChangePassword)
			r.True(passwords.Verify(emailChangePassword, *user.PasswordHash), "the password is not rotated")

			_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
			r.NoError(err, "a refused call revokes nothing")
			r.Empty(emailJobRecipients(t, ctx, f.db, "password-changed.html"))
		})
	}
}

// TestFirstLoginEmailOmittedKeepsTodaysBehaviour: no email, no change of
// address; the flag is still cleared and the confirmation goes to the address
// on file.
func TestFirstLoginEmailOmittedKeepsTodaysBehaviour(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newFirstLoginFixture(t, defaults.Email, true)

	rec := postChangePassword(t, ctx, f.handler, f.claimsA,
		`{"currentPassword":"`+emailChangePassword+`","newPassword":"`+firstLoginNewPassword+`"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	updated, err := f.db.GetUser(ctx, f.user.UID)
	r.NoError(err)
	r.Equal(defaults.Email, updated.Email)
	r.False(updated.MustChangePassword)
	r.NotNil(updated.EmailVerifiedAt)
	r.True(passwords.Verify(firstLoginNewPassword, *updated.PasswordHash))

	events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: f.org.UID,
		EventTypes:      []models.EventType{models.EventTypeAuthEmailChanged},
		Limit:           10,
	})
	r.NoError(err)
	r.Empty(events)
	r.Equal([]string{defaults.Email}, emailJobRecipients(t, ctx, f.db, "password-changed.html"))
}

// TestFirstLoginEmailGuardedRoutes: the change-password route stays denied to
// impersonation tokens and to the shared demo principal, so the email field
// cannot be reached through either.
func TestFirstLoginEmailGuardedRoutes(t *testing.T) {
	t.Parallel()

	require.False(t, IsImpersonationAllowed(http.MethodPost, ImpersonationPathChangePassword))
	require.False(t, IsDemoWriteAllowed(http.MethodPost, ImpersonationPathChangePassword))
	// Positive control: the same guards let an ordinary read through.
	require.True(t, IsImpersonationAllowed(http.MethodGet, ImpersonationPathMe))
}

// TestEmailChangeNoticeSkipsDefaultAddress: the Account > Profile change sends
// no notice to the default address, and still notifies any other old address.
func TestEmailChangeNoticeSkipsDefaultAddress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		oldEmail   string
		wantNotice bool
	}{
		{"default old address", defaults.Email, false},
		{"real old address", "old@acme.com", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, ctx := newFirstLoginFixture(t, tc.oldEmail, false)

			r.NoError(f.svc.applyEmailChange(ctx, &emailChange{
				user: f.user, newEmail: "new@acme.com", changedBy: emailChangedBySelf, actorUID: f.user.UID,
			}))

			recipients := emailJobRecipients(t, ctx, f.db, email.TemplateEmailChanged)
			if tc.wantNotice {
				r.Equal([]string{tc.oldEmail}, recipients)
			} else {
				r.Empty(recipients)
			}
		})
	}
}
