package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// selfEmailWorld is a signed-in password account about to change its email.
type selfEmailWorld struct {
	*edgeWorld

	user   *models.User
	claims *Claims
}

func newSelfEmailWorld(t *testing.T) *selfEmailWorld {
	t.Helper()

	w := newEdgeWorld(t)
	_, user, login := w.signedIn(t, models.MemberRoleOwner)
	w.withPassword(t, user)

	claims, err := w.svc.ValidateToken(w.ctx, login.AccessToken)
	require.NoError(t, err)

	return &selfEmailWorld{edgeWorld: w, user: user, claims: claims}
}

func (w *selfEmailWorld) changeEmail(newEmail string) (*MeResponse, error) {
	password := edgePassword

	return w.svc.UpdateProfile(w.ctx, w.claims, UpdateProfileRequest{Email: &newEmail, CurrentPassword: &password})
}

func (w *selfEmailWorld) storedEmail(t *testing.T) string {
	t.Helper()

	user, err := w.dbSvc.GetUser(w.ctx, w.user.UID)
	require.NoError(t, err)

	return user.Email
}

// TestOwnEmailChangeWhenTheStoreFails: the address only changes when its
// write went through. The bookkeeping after it is best effort.
func TestOwnEmailChangeWhenTheStoreFails(t *testing.T) {
	t.Parallel()

	const newEmail = "renamed@acme.com"

	refused := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		// The first read loads the caller.
		{"checking the address is free", failingSQL(`FROM "users"`).after(1), ""},
		{"writing the address", failingSQL(`UPDATE "users"`), "failed to update email"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newSelfEmailWorld(t)

			var err error

			w.fault.during(t, tt.fault, func() { _, err = w.changeEmail(newEmail) })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Equal(t, w.user.Email, w.storedEmail(t))
			require.Equal(t, 1, w.sessionCount(t, w.user.UID), "no session was revoked")
		})
	}

	bestEffort := map[string]sqlFault{
		"listing the organizations to audit":     failingSQL(`FROM "organization_members"`).once(),
		"resetting the password-attempt counter": softDeletedStateEntries,
	}
	for name, fault := range bestEffort {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newSelfEmailWorld(t)

			var (
				me  *MeResponse
				err error
			)

			w.fault.during(t, fault, func() { me, err = w.changeEmail(newEmail) })
			require.NoError(t, err)
			require.Equal(t, newEmail, me.User.Email)
			require.Equal(t, newEmail, w.storedEmail(t))
		})
	}
}

// adminEmailWorld is a super admin about to change a user's email.
type adminEmailWorld struct {
	*edgeWorld

	admin       *models.User
	adminClaims *Claims
	target      *models.User
}

func newAdminEmailWorld(t *testing.T) *adminEmailWorld {
	t.Helper()

	w := newEdgeWorld(t)
	_, admin, login := w.signedIn(t, models.MemberRoleOwner)
	w.promote(t, admin.UID)
	_, target, _ := w.signedIn(t, models.MemberRoleUser)

	claims, err := w.svc.ValidateToken(w.ctx, login.AccessToken)
	require.NoError(t, err)

	return &adminEmailWorld{edgeWorld: w, admin: admin, adminClaims: claims, target: target}
}

func TestAdminUpdateUserEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("refused under impersonation", func(t *testing.T) {
		t.Parallel()

		w := newAdminEmailWorld(t)
		impersonating := *w.adminClaims
		impersonating.ImpersonatedBy = w.admin.UID
		email := "renamed@acme.com"

		_, err := w.svc.AdminUpdateUser(w.ctx, &impersonating, w.target.UID, AdminUpdateUserRequest{Email: &email})
		require.ErrorIs(t, err, ErrImpersonationForbidden)

		stored, err := w.dbSvc.GetUser(w.ctx, w.target.UID)
		require.NoError(t, err)
		require.Equal(t, w.target.Email, stored.Email)
	})

	t.Run("the same address in another case changes nothing", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newAdminEmailWorld(t)
		shouted := strings.ToUpper(w.target.Email)
		r.NotEqual(shouted, w.target.Email)

		resp, err := w.svc.AdminUpdateUser(w.ctx, w.adminClaims, w.target.UID, AdminUpdateUserRequest{Email: &shouted})
		r.NoError(err)
		r.Equal(w.target.Email, resp.Email)
		r.Equal(1, w.sessionCount(t, w.target.UID), "an unchanged address signs nobody out")
	})

	t.Run("a super admin renaming themselves keeps the session they act from", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newAdminEmailWorld(t)
		orgUID := w.org(t, w.adminClaims.OrgSlug).UID
		_, elsewhere := w.token(t, w.admin.UID, &orgUID, models.TokenTypeRefresh, nil)
		email := "root@acme.com"

		resp, err := w.svc.AdminUpdateUser(w.ctx, w.adminClaims, w.admin.UID, AdminUpdateUserRequest{Email: &email})
		r.NoError(err)
		r.Equal(email, resp.Email)

		_, err = w.dbSvc.GetUserToken(w.ctx, w.adminClaims.RefreshUID)
		r.NoError(err, "the acting session survives")

		_, err = w.dbSvc.GetUserToken(w.ctx, elsewhere.UID)
		r.Error(err, "the other session is revoked")
	})

	failures := []struct {
		name  string
		fault sqlFault
	}{
		// Reads of users, in order: the actor, the target, the result.
		{"reading the target", failingSQL(`FROM "users"`).after(1)},
		{"reading the result back", failingSQL(`FROM "users"`).after(2)},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newAdminEmailWorld(t)

			var (
				resp *AdminUpdateUserResponse
				err  error
			)

			w.fault.during(t, tt.fault, func() {
				resp, err = w.svc.AdminUpdateUser(w.ctx, w.adminClaims, w.target.UID, AdminUpdateUserRequest{})
			})
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, resp)
		})
	}
}

// TestImpersonateStoreFailuresAndUnknownActor: a store failure while
// resolving the actor, the target or the membership is an error, never a
// token, and an actor that does not exist is not a super admin.
func TestImpersonateStoreFailuresAndUnknownActor(t *testing.T) {
	t.Parallel()

	newFixture := func(t *testing.T) (*impersonationFixture, *queryFault) {
		t.Helper()

		svc, dbSvc := newImpersonationSQLiteService(t)

		return newImpersonationFixture(t.Context(), t, svc, dbSvc, "edge"), installQueryFault(dbSvc)
	}

	t.Run("unknown actor", func(t *testing.T) {
		t.Parallel()

		f, _ := newFixture(t)

		resp, err := f.svc.Impersonate(t.Context(), uuidV7(t), f.member.UID, f.org.Slug, Context{})
		require.ErrorIs(t, err, ErrImpersonationNotSuperAdmin)
		require.Nil(t, resp)
	})

	failures := []struct {
		name    string
		orgSlug bool
		fault   sqlFault
	}{
		{"reading the actor", true, failingSQL(`FROM "users"`)},
		{"reading the target", true, failingSQL(`FROM "users"`).after(1)},
		{"listing the target's organizations", false, failingSQL(`FROM "organization_members"`)},
		{"reading the organization", true, failingSQL(`FROM "organizations"`)},
		{"reading the target's membership", true, failingSQL(`FROM "organization_members"`)},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, fault := newFixture(t)

			slug := ""
			if tt.orgSlug {
				slug = f.org.Slug
			}

			var (
				resp *LoginResponse
				err  error
			)

			fault.during(t, tt.fault, func() {
				resp, err = f.svc.Impersonate(t.Context(), f.admin.UID, f.member.UID, slug, Context{})
			})
			require.ErrorIs(t, err, context.Canceled)
			require.NotErrorIs(t, err, ErrImpersonationTargetNotMember, "a store failure is not a missing membership")
			require.Nil(t, resp)

			// Control: the same call mints a token once the store is back.
			resp, err = f.svc.Impersonate(t.Context(), f.admin.UID, f.member.UID, slug, Context{})
			require.NoError(t, err)
			require.Equal(t, f.member.Email, resp.User.Email)
		})
	}
}

// TestImpersonateHandlerErrors covers the HTTP translation of the refusals
// the route can produce.
func TestImpersonateHandlerErrors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, dbSvc := newImpersonationSQLiteService(t)
	f := newImpersonationFixture(ctx, t, svc, dbSvc, "http")
	fixture := &coverFixture{svc: svc, h: NewHandler(svc, impersonationTestConfig()), ctx: ctx}
	target := map[string]string{"uid": f.member.UID}

	tests := []struct {
		name     string
		call     coverCall
		want     int
		wantCode base.ErrorCode
	}{
		{"no session", coverCall{params: target}, http.StatusUnauthorized, base.ErrorCodeUnauthorized},
		{
			"body that is not JSON",
			coverCall{params: target, claims: &Claims{UserUID: f.admin.UID}, body: "{"},
			http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
		},
		{
			"actor who is not a super admin",
			coverCall{params: target, claims: &Claims{UserUID: f.outsider.UID}},
			http.StatusForbidden, base.ErrorCodeForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.call.fn = fixture.h.Impersonate
			rec := fixture.do(t, tt.call)
			require.Equal(t, tt.want, rec.Code, rec.Body.String())

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, string(tt.wantCode), body["code"])
			require.NotContains(t, body, "accessToken")
		})
	}
}

func TestSignedInAtProperty(t *testing.T) {
	t.Parallel()

	row := &models.UserToken{}
	stampSignedInAt(row, 0)
	require.Nil(t, row.Properties, "no sign-in time, nothing stamped")

	stampSignedInAt(row, 1700000000)
	require.Equal(t, int64(1700000000), signedInAtOf(row))

	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{"int64", int64(42), 42},
		{"int", 42, 42},
		{"JSON number", float64(42), 42},
		{"json.Number", json.Number("42"), 42},
		{"json.Number that is not an integer", json.Number("4.2"), 0},
		{"string", "42", 0},
		{"absent", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			props := models.JSONMap{}
			if tt.value != nil {
				props[keySignedInAt] = tt.value
			}

			require.Equal(t, tt.want, signedInAtOf(&models.UserToken{Properties: props}))
		})
	}

	require.Zero(t, signedInAtOf(nil))
}

func TestConfirm2FARefusesAForgedSetupToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newEdgeWorld(t)
	_, user, _ := w.signedIn(t, models.MemberRoleOwner)
	secret := "JBSWY3DPEHPK3PXP"
	r.NoError(w.dbSvc.UpdateUser(w.ctx, user.UID, &models.UserUpdate{TOTPSecret: &secret}))

	_, err := w.svc.Confirm2FA(w.ctx, user.UID, Confirm2FARequest{Code: "000000", SetupToken: "not-a-token"})
	r.ErrorIs(err, ErrInvalidSetupToken)

	stored, err := w.dbSvc.GetUser(w.ctx, user.UID)
	r.NoError(err)
	r.False(stored.TOTPEnabled)
}
