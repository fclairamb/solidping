package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	emailChangeOrg      = "email-change"
	emailChangePassword = "testpass1234"
)

// emailChangeFixture seeds an org and a verified password user in it, and
// logs that user in twice (two sessions).
type emailChangeFixture struct {
	svc      *Service
	db       db.Service
	handler  *Handler
	org      *models.Organization
	user     *models.User
	sessionA *LoginResponse
	sessionB *LoginResponse
	claimsA  *Claims
}

func newEmailChangeFixture(t *testing.T, email string) (*emailChangeFixture, context.Context) {
	t.Helper()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	changePasswordFixture(t, ctx, dbSvc, emailChangeOrg, email)

	user, err := dbSvc.GetUserByEmail(ctx, email)
	r.NoError(err)

	verifiedAt := time.Now()
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{EmailVerifiedAt: &verifiedAt}))

	org, err := dbSvc.GetOrganizationBySlug(ctx, emailChangeOrg)
	r.NoError(err)

	sessionA, err := svc.Login(ctx, emailChangeOrg, email, emailChangePassword, Context{})
	r.NoError(err)
	sessionB, err := svc.Login(ctx, emailChangeOrg, email, emailChangePassword, Context{})
	r.NoError(err)

	claimsA, err := svc.ValidateToken(ctx, sessionA.AccessToken)
	r.NoError(err)

	return &emailChangeFixture{
		svc: svc, db: dbSvc, handler: NewHandler(svc, &config.Config{}), org: org, user: user,
		sessionA: sessionA, sessionB: sessionB, claimsA: claimsA,
	}, ctx
}

//nolint:revive // ctx-second matches the existing helpers in this package
func patchMe(
	t *testing.T, ctx context.Context, handler *Handler, claims *Claims, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPatch, "/api/v1/auth/me", strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.UpdateMe(rec, httpReq))

	return rec
}

//nolint:revive // ctx-second matches the existing helpers in this package
func patchSystemUser(
	t *testing.T, ctx context.Context, handler *Handler, claims *Claims, uid, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("uid", uid)
	reqCtx := context.WithValue(context.WithValue(ctx, base.ContextKeyClaims, claims), chi.RouteCtxKey, rctx)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPatch, "/api/v1/system/users/"+uid,
		strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.AdminUpdateUser(rec, httpReq))

	return rec
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"alice@acme.com", "alice@acme.com", false},
		{"  Alice@Acme.COM ", "alice@acme.com", false},
		{"", "", true},
		{"   ", "", true},
		{"not-an-email", "", true},
		{"alice@", "", true},
		{"@acme.com", "", true},
		{"Alice <alice@acme.com>", "", true},
		{"alice@acme.com, bob@acme.com", "", true},
		{strings.Repeat("a", 250) + "@acme.com", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := normalizeEmail(tc.in)
			if tc.wantErr {
				r.ErrorIs(err, ErrInvalidEmail)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

// TestUpdateMeEmailChange covers the self-service change end to end: the new
// address logs in, the old one does not, email_verified_at is cleared, the
// caller's session survives and the other one dies, and the change is audited.
func TestUpdateMeEmailChange(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newEmailChangeFixture(t, "old@acme.com")

	rec := patchMe(t, ctx, f.handler, f.claimsA,
		`{"email":" New@Acme.com ","currentPassword":"`+emailChangePassword+`","name":"Alice"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), `"new@acme.com"`)

	updated, err := f.db.GetUser(ctx, f.user.UID)
	r.NoError(err)
	r.Equal("new@acme.com", updated.Email)
	r.Equal("Alice", updated.Name, "the name is applied in the same request")
	r.Nil(updated.EmailVerifiedAt, "an email change un-verifies the account")

	// The caller's session survives, the other one is revoked.
	_, err = f.svc.Refresh(ctx, f.sessionA.RefreshToken)
	r.NoError(err)
	_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)

	// Login works with the new email and fails with the old one.
	_, err = f.svc.Login(ctx, emailChangeOrg, "new@acme.com", emailChangePassword, Context{})
	r.NoError(err)
	_, err = f.svc.Login(ctx, emailChangeOrg, "old@acme.com", emailChangePassword, Context{})
	r.ErrorIs(err, ErrInvalidCredentials)

	events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: f.org.UID,
		EventTypes:      []models.EventType{models.EventTypeAuthEmailChanged},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal("old@acme.com", events[0].Payload[auditKeyOldEmail])
	r.Equal("new@acme.com", events[0].Payload[auditKeyNewEmail])
	r.Equal(emailChangedBySelf, events[0].Payload[auditKeyChangedBy])
}

// TestUpdateMeEmailChangeRefusals: every refusal leaves the address untouched
// and every session alive.
func TestUpdateMeEmailChangeRefusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		setup    func(t *testing.T, ctx context.Context, f *emailChangeFixture) *Claims
		wantCode int
		wantErr  base.ErrorCode
	}{
		{
			name:     "wrong password",
			body:     `{"email":"new@acme.com","currentPassword":"wrongwrong"}`,
			wantCode: http.StatusForbidden,
			wantErr:  base.ErrorCodeInvalidCurrentPassword,
		},
		{
			name:     "missing password",
			body:     `{"email":"new@acme.com"}`,
			wantCode: http.StatusForbidden,
			wantErr:  base.ErrorCodeInvalidCurrentPassword,
		},
		{
			name:     "invalid format",
			body:     `{"email":"not an email","currentPassword":"` + emailChangePassword + `"}`,
			wantCode: http.StatusBadRequest,
			wantErr:  base.ErrorCodeValidationError,
		},
		{
			name: "duplicate in a different case",
			body: `{"email":"TAKEN@acme.com","currentPassword":"` + emailChangePassword + `"}`,
			setup: func(t *testing.T, ctx context.Context, f *emailChangeFixture) *Claims {
				t.Helper()
				require.NoError(t, f.db.CreateUser(ctx, models.NewUser("taken@acme.com")))

				return f.claimsA
			},
			wantCode: http.StatusConflict,
			wantErr:  base.ErrorCodeConflict,
		},
		{
			name: "password-less (OAuth-only) account",
			body: `{"email":"new@acme.com","currentPassword":"anything"}`,
			setup: func(t *testing.T, ctx context.Context, f *emailChangeFixture) *Claims {
				t.Helper()
				empty := ""
				require.NoError(t, f.db.UpdateUser(ctx, f.user.UID, &models.UserUpdate{PasswordHash: &empty}))

				return f.claimsA
			},
			wantCode: http.StatusForbidden,
			wantErr:  base.ErrorCodeForbidden,
		},
		{
			name: "impersonation token",
			body: `{"email":"new@acme.com","currentPassword":"` + emailChangePassword + `"}`,
			setup: func(t *testing.T, _ context.Context, f *emailChangeFixture) *Claims {
				t.Helper()
				claims := *f.claimsA
				claims.ImpersonatedBy = "some-admin-uid"
				claims.RefreshUID = ""

				return &claims
			},
			wantCode: http.StatusForbidden,
			wantErr:  base.ErrorCodeImpersonationForbidden,
		},
		{
			name: "demo account",
			body: `{"email":"new@acme.com","currentPassword":"` + emailChangePassword + `"}`,
			setup: func(t *testing.T, ctx context.Context, f *emailChangeFixture) *Claims {
				t.Helper()
				demo := true
				require.NoError(t, f.db.UpdateUser(ctx, f.user.UID, &models.UserUpdate{Demo: &demo}))

				return f.claimsA
			},
			wantCode: http.StatusForbidden,
			wantErr:  base.ErrorCodeDemoReadOnly,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, ctx := newEmailChangeFixture(t, "old@acme.com")

			claims := f.claimsA
			if tc.setup != nil {
				claims = tc.setup(t, ctx, f)
			}

			rec := patchMe(t, ctx, f.handler, claims, tc.body)
			r.Equal(tc.wantCode, rec.Code, rec.Body.String())
			r.Equal(string(tc.wantErr), decodeErrorCode(t, rec))

			user, err := f.db.GetUser(ctx, f.user.UID)
			r.NoError(err)
			r.Equal("old@acme.com", user.Email, "a refused change leaves the address untouched")
			r.NotNil(user.EmailVerifiedAt)

			_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
			r.NoError(err, "a refused change revokes nothing")
		})
	}
}

// TestUpdateMeSameEmailIsNoOp: re-sending the current address (in any case)
// needs no password and changes nothing but the name.
func TestUpdateMeSameEmailIsNoOp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newEmailChangeFixture(t, "same@acme.com")

	rec := patchMe(t, ctx, f.handler, f.claimsA, `{"email":"SAME@acme.com","name":"Bob"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	user, err := f.db.GetUser(ctx, f.user.UID)
	r.NoError(err)
	r.Equal("same@acme.com", user.Email)
	r.Equal("Bob", user.Name)
	r.NotNil(user.EmailVerifiedAt)

	_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
	r.NoError(err)
}

// TestAdminUpdateUserEmail covers PATCH /system/users/:uid.
func TestAdminUpdateUserEmail(t *testing.T) {
	t.Parallel()

	t.Run("a super admin changes another user's email without their password", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		f, ctx := newEmailChangeFixture(t, "target@acme.com")

		admin := models.NewUser("root@acme.com")
		admin.SuperAdmin = true
		r.NoError(f.db.CreateUser(ctx, admin))

		rec := patchSystemUser(t, ctx, f.handler, &Claims{UserUID: admin.UID}, f.user.UID,
			`{"email":"Renamed@Acme.com"}`)
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())
		r.Contains(rec.Body.String(), `"email":"renamed@acme.com"`)
		r.Contains(rec.Body.String(), `"emailVerified":false`)

		user, err := f.db.GetUser(ctx, f.user.UID)
		r.NoError(err)
		r.Equal("renamed@acme.com", user.Email)
		r.Nil(user.EmailVerifiedAt)

		// Every session of the target is revoked.
		_, err = f.svc.Refresh(ctx, f.sessionA.RefreshToken)
		r.ErrorIs(err, ErrInvalidToken)
		_, err = f.svc.Refresh(ctx, f.sessionB.RefreshToken)
		r.ErrorIs(err, ErrInvalidToken)

		_, err = f.svc.Login(ctx, emailChangeOrg, "renamed@acme.com", emailChangePassword, Context{})
		r.NoError(err)

		events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: f.org.UID,
			EventTypes:      []models.EventType{models.EventTypeAuthEmailChanged},
			Limit:           10,
		})
		r.NoError(err)
		r.Len(events, 1)
		r.Equal(emailChangedBySuperAdmin, events[0].Payload[auditKeyChangedBy])
		r.NotNil(events[0].ActorUID)
		r.Equal(admin.UID, *events[0].ActorUID)
	})

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name     string
			admin    bool
			target   string
			body     string
			wantCode int
			wantErr  base.ErrorCode
		}{
			{"non-super-admin caller", false, "", `{"email":"x@acme.com"}`, http.StatusForbidden, base.ErrorCodeForbidden},
			{"unknown user", true, "no-such-uid", `{"email":"x@acme.com"}`, http.StatusNotFound, base.ErrorCodeUserNotFound},
			{"invalid email", true, "", `{"email":"nope"}`, http.StatusBadRequest, base.ErrorCodeValidationError},
			{"duplicate", true, "", `{"email":"ROOT@acme.com"}`, http.StatusConflict, base.ErrorCodeConflict},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				f, ctx := newEmailChangeFixture(t, "target@acme.com")

				caller := models.NewUser("root@acme.com")
				caller.SuperAdmin = tc.admin
				r.NoError(f.db.CreateUser(ctx, caller))

				target := tc.target
				if target == "" {
					target = f.user.UID
				}

				rec := patchSystemUser(t, ctx, f.handler, &Claims{UserUID: caller.UID}, target, tc.body)
				r.Equal(tc.wantCode, rec.Code, rec.Body.String())
				r.Equal(string(tc.wantErr), decodeErrorCode(t, rec))

				user, err := f.db.GetUser(ctx, f.user.UID)
				r.NoError(err)
				r.Equal("target@acme.com", user.Email)
			})
		}
	})
}
