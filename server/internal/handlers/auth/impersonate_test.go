package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// impersonationFixture is one org with a super admin, an ordinary member, a
// second super admin, a demo member and a user outside the org. Every name is
// suffixed so the Postgres twin can build several on one database.
type impersonationFixture struct {
	svc      *Service
	db       db.Service
	org      *models.Organization
	otherOrg *models.Organization
	admin    *models.User
	member   *models.User
	admin2   *models.User
	demo     *models.User
	outsider *models.User
}

func newImpersonationSQLiteService(t *testing.T) (*Service, db.Service) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	return NewService(dbSvc, impersonationTestConfig().Auth, impersonationTestConfig(), nil, nil), dbSvc
}

func impersonationTestConfig() *config.Config {
	return &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:            "test-jwt-secret",
			AccessTokenExpiry:    time.Hour,
			RefreshTokenExpiry:   7 * 24 * time.Hour,
			ImpersonationEnabled: true,
		},
	}
}

func newImpersonationFixture(
	ctx context.Context, t *testing.T, svc *Service, dbSvc db.Service, suffix string,
) *impersonationFixture {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization("imp"+suffix, "Acme "+suffix)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	otherOrg := models.NewOrganization("impo"+suffix, "Acme other "+suffix)
	r.NoError(dbSvc.CreateOrganization(ctx, otherOrg))

	mkUser := func(local string, mutate func(*models.User), role models.MemberRole) *models.User {
		user := models.NewUser(local + "-" + suffix + "@acme.com")
		if mutate != nil {
			mutate(user)
		}

		r.NoError(dbSvc.CreateUser(ctx, user))

		if role != "" {
			r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))
		}

		return user
	}

	return &impersonationFixture{
		svc:      svc,
		db:       dbSvc,
		org:      org,
		otherOrg: otherOrg,
		admin:    mkUser("root", func(u *models.User) { u.SuperAdmin = true }, ""),
		// The target has 2FA on: the admin's own authentication gates
		// impersonation, the target's second factor is not in the way.
		member:   mkUser("alice", func(u *models.User) { u.TOTPEnabled = true }, models.MemberRoleViewer),
		admin2:   mkUser("root2", func(u *models.User) { u.SuperAdmin = true }, models.MemberRoleAdmin),
		demo:     mkUser("demo", func(u *models.User) { u.Demo = true }, models.MemberRoleUser),
		outsider: mkUser("bob", nil, ""),
	}
}

func (f *impersonationFixture) refreshRows(ctx context.Context, t *testing.T, userUID string) int {
	t.Helper()

	tokens, err := f.db.ListUserTokens(ctx, userUID)
	require.NoError(t, err)

	return len(tokens)
}

// runImpersonationContract is the whole behavioral contract, shared by the
// SQLite test below and its Postgres twin.
//
//nolint:funlen // One contract, read top to bottom.
func runImpersonationContract(t *testing.T, newFixture func(t *testing.T) (*impersonationFixture, context.Context)) {
	t.Helper()

	t.Run("super admin impersonates a member", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		before := f.refreshRows(ctx, t, f.member.UID)

		resp, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, f.org.Slug,
			Context{RemoteAddr: "203.0.113.9", UserAgent: "impersonation-test"})
		r.NoError(err)

		// Access token only: no refresh token, a 30-minute lifetime.
		r.NotEmpty(resp.AccessToken)
		r.Empty(resp.RefreshToken, "an impersonation never hands out a refresh token")
		r.Equal(int(ImpersonationTTL.Seconds()), resp.ExpiresIn)
		r.Equal(f.member.UID, resp.User.UID)
		r.Equal(string(models.MemberRoleViewer), resp.User.Role)
		r.Equal(f.org.Slug, resp.Organization.Slug)

		claims, err := f.svc.ValidateToken(ctx, resp.AccessToken)
		r.NoError(err)
		r.Equal(f.member.UID, claims.UserUID)
		r.Equal(string(models.MemberRoleViewer), claims.Role, "the target's REAL role")
		r.False(claims.IsSuperAdmin(), "never RoleSuperAdmin")
		r.Equal(f.admin.UID, claims.ImpersonatedBy)
		r.Empty(claims.RefreshUID, "no session row backs the token")
		r.LessOrEqual(claims.ExpiresAt.Sub(claims.IssuedAt.Time), ImpersonationTTL)
		r.Less(time.Until(claims.ExpiresAt.Time), ImpersonationTTL+time.Second)

		// The target's session list is untouched.
		r.Equal(before, f.refreshRows(ctx, t, f.member.UID))

		// Audited in the target's org, attributed to the admin.
		events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: f.org.UID,
			EventTypes:      []models.EventType{models.EventTypeAuthImpersonationStarted},
			Limit:           10,
		})
		r.NoError(err)
		r.Len(events, 1)
		r.Equal(models.EventTypeAuthImpersonationStarted, events[0].EventType)
		r.NotNil(events[0].ActorUID)
		r.Equal(f.admin.UID, *events[0].ActorUID, "the actor is the admin, not the target")
		r.Equal(f.member.UID, events[0].Payload[audit.PayloadKeyTargetUID])
		r.Equal(AuthMethodImpersonate, events[0].Payload[auditKeyMethod])
		r.Equal(f.admin.Email, events[0].Payload[auditKeyImpersonatorEmail])
		r.NotEmpty(events[0].Payload[auditKeyExpiresAt])
		r.NotNil(events[0].UserAgent)
		r.Equal("impersonation-test", *events[0].UserAgent)
	})

	t.Run("orgSlug defaults to the target's first org", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		resp, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, "", Context{})
		r.NoError(err)
		r.Equal(f.org.Slug, resp.Organization.Slug)
	})

	refusals := []struct {
		name    string
		wantErr error
		call    func(ctx context.Context, f *impersonationFixture) error
	}{
		{
			name:    "caller is not a super admin",
			wantErr: ErrImpersonationNotSuperAdmin,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.outsider.UID, f.member.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			name:    "target is a super admin",
			wantErr: ErrImpersonationTargetSuperAdmin,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.admin2.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			name:    "target is the actor",
			wantErr: ErrImpersonationTargetSelf,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.admin.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			name:    "target is the demo user",
			wantErr: ErrImpersonationTargetDemo,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.demo.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			name:    "target is not a member of orgSlug",
			wantErr: ErrImpersonationTargetNotMember,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, f.otherOrg.Slug, Context{})

				return err
			},
		},
		{
			name:    "target belongs to no org",
			wantErr: ErrImpersonationTargetNotMember,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.outsider.UID, "", Context{})

				return err
			},
		},
		{
			name:    "unknown org",
			wantErr: ErrImpersonationTargetNotMember,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, "no-such-org-xyz", Context{})

				return err
			},
		},
		{
			name:    "unknown target",
			wantErr: ErrUserNotFound,
			call: func(ctx context.Context, f *impersonationFixture) error {
				_, err := f.svc.Impersonate(ctx, f.admin.UID, "00000000-0000-0000-0000-000000000000", f.org.Slug, Context{})

				return err
			},
		},
		{
			name:    "deleted target",
			wantErr: ErrUserNotFound,
			call: func(ctx context.Context, f *impersonationFixture) error {
				if err := f.db.DeleteUser(ctx, f.member.UID); err != nil {
					return err
				}

				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			// The caller's own credential is an impersonation token: as
			// RequireAuth leaves it on the context.
			name:    "chaining from an impersonation token",
			wantErr: ErrImpersonationChained,
			call: func(ctx context.Context, f *impersonationFixture) error {
				ctx = audit.WithImpersonator(audit.WithUser(ctx, f.member.UID, models.ActorTypeUser), f.admin.UID)
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.demo.UID, f.org.Slug, Context{})

				return err
			},
		},
		{
			// Same, when only the claims are on the context.
			name:    "chaining detected from the claims alone",
			wantErr: ErrImpersonationChained,
			call: func(ctx context.Context, f *impersonationFixture) error {
				ctx = context.WithValue(ctx, base.ContextKeyClaims,
					&Claims{UserUID: f.member.UID, ImpersonatedBy: f.admin.UID})
				_, err := f.svc.Impersonate(ctx, f.admin.UID, f.outsider.UID, f.org.Slug, Context{})

				return err
			},
		},
	}

	for _, tc := range refusals {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newFixture(t)

			r.ErrorIs(tc.call(ctx, f), tc.wantErr)

			// A refusal is never recorded as a started impersonation.
			events, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
				OrganizationUID: f.org.UID,
				EventTypes:      []models.EventType{models.EventTypeAuthImpersonationStarted},
				Limit:           10,
			})
			r.NoError(err)
			r.Empty(events)
		})
	}

	t.Run("kill switch off", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		f.svc.fullCfg.Auth.ImpersonationEnabled = false

		_, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, f.org.Slug, Context{})
		r.ErrorIs(err, ErrImpersonationDisabled)
	})

	t.Run("no session can be minted from an impersonated request", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		before := f.refreshRows(ctx, t, f.member.UID)
		impCtx := audit.WithImpersonator(audit.WithUser(ctx, f.member.UID, models.ActorTypeUser), f.admin.UID)

		_, err := f.svc.SwitchOrg(impCtx, f.member.UID, f.org.Slug, Context{})
		r.ErrorIs(err, ErrImpersonationForbidden)
		r.Equal(before, f.refreshRows(ctx, t, f.member.UID), "no refresh row was created")

		// Positive control: the same call outside an impersonation works.
		_, err = f.svc.SwitchOrg(ctx, f.member.UID, f.org.Slug, Context{})
		r.NoError(err)
		r.Equal(before+1, f.refreshRows(ctx, t, f.member.UID))
	})

	t.Run("audit rows written under the token name the admin", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		impCtx := audit.WithImpersonator(audit.WithUser(ctx, f.member.UID, models.ActorTypeUser), f.admin.UID)
		event := audit.Record(impCtx, f.db, f.org.UID, models.EventTypeMemberRoleChanged,
			audit.Target{Type: auditTargetMember, UID: f.demo.UID}, models.JSONMap{auditKeyRole: "viewer"})
		r.NotNil(event)

		stored, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: f.org.UID,
			EventTypes:      []models.EventType{models.EventTypeMemberRoleChanged},
			Limit:           10,
		})
		r.NoError(err)
		r.Len(stored, 1)
		r.Equal(f.member.UID, *stored[0].ActorUID, "permission-wise the actor is the target")
		r.Equal(f.admin.UID, stored[0].Payload[audit.PayloadKeyImpersonatedBy], "and the real actor is named")

		// A row a service builds by hand (incident comments, status updates…)
		// is stamped at the insert, not only through audit.Record.
		handBuilt := models.NewEvent(f.org.UID, models.EventTypeIncidentComment, models.ActorTypeUser)
		r.NoError(f.db.CreateEvent(impCtx, handBuilt))

		comments, err := f.db.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: f.org.UID,
			EventTypes:      []models.EventType{models.EventTypeIncidentComment},
			Limit:           10,
		})
		r.NoError(err)
		r.Len(comments, 1)
		r.Equal(f.admin.UID, comments[0].Payload[audit.PayloadKeyImpersonatedBy])

		// Positive control: an ordinary request carries no such key.
		plain := audit.NewEvent(audit.WithUser(ctx, f.member.UID, models.ActorTypeUser),
			f.org.UID, models.EventTypeMemberRoleChanged, audit.Target{}, nil)
		r.NotContains(plain.Payload, audit.PayloadKeyImpersonatedBy)
	})

	t.Run("/auth/me describes the impersonation", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newFixture(t)

		resp, err := f.svc.Impersonate(ctx, f.admin.UID, f.member.UID, f.org.Slug, Context{})
		r.NoError(err)

		claims, err := f.svc.ValidateToken(ctx, resp.AccessToken)
		r.NoError(err)

		info := f.svc.ImpersonationInfoFor(ctx, claims)
		r.NotNil(info)
		r.Equal(f.admin.UID, info.ImpersonatorUID)
		r.Equal(f.admin.Email, info.ImpersonatorEmail)
		r.NotNil(info.ExpiresAt)

		r.Nil(f.svc.ImpersonationInfoFor(ctx, &Claims{UserUID: f.member.UID}), "ordinary token")
	})
}

func TestImpersonate(t *testing.T) {
	t.Parallel()

	runImpersonationContract(t, func(t *testing.T) (*impersonationFixture, context.Context) {
		t.Helper()

		svc, dbSvc := newImpersonationSQLiteService(t)
		ctx := t.Context()

		return newImpersonationFixture(ctx, t, svc, dbSvc, "s"), ctx
	})
}

func TestIsImpersonationAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method, pattern string
		want            bool
	}{
		{"GET", ImpersonationPathChangePassword, true},
		{"GET", "/api/v1/orgs/{org}/checks", true},
		{"POST", "/api/v1/orgs/{org}/checks", true},
		{"PATCH", "/api/v1/auth/me", true},
		{"POST", "/api/v1/auth/logout", true},
		{"POST", ImpersonationPathChangePassword, false},
		{"POST", ImpersonationPath2FASetup, false},
		{"POST", ImpersonationPath2FAConfirm, false},
		{"DELETE", ImpersonationPath2FA, false},
		{"POST", ImpersonationPathPasskeyBegin, false},
		{"POST", ImpersonationPathPasskeyFinish, false},
		{"DELETE", ImpersonationPatternPasskey, false},
		{"POST", ImpersonationPatternOrgTokens, false},
		{"POST", ImpersonationPatternEnrollmentToken, false},
		{"POST", ImpersonationPathDeviceConsent, false},
		{"POST", ImpersonationPathSwitchOrg, false},
		{"POST", ImpersonationPathOrgs, false},
		{"DELETE", ImpersonationPatternToken, false},
		{"DELETE", ImpersonationPathTokenCurrent, false},
		{"POST", ImpersonationPatternImpersonate, false},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.pattern, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsImpersonationAllowed(tc.method, tc.pattern))
		})
	}
}
