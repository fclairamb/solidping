package auth

import (
	"context"
	"database/sql"
	"errors"
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

// acceptInviteFixture: alice@acme.com is an existing account with a password
// and TOTP enabled; bob is an admin of evilco, the org inviting her.
type acceptInviteFixture struct {
	svc   *Service
	db    db.Service
	org   *models.Organization
	alice *models.User
	bob   *models.User
	token string
}

func newAcceptInviteFixture(t *testing.T, inviteEmail string) *acceptInviteFixture {
	t.Helper()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

	org := models.NewOrganization("evilco", "Evil Co")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	bob := models.NewUser("bob@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, bob))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, bob.UID, models.MemberRoleAdmin)))

	alice := models.NewUser("alice@acme.com")
	hash, err := passwords.Hash("alice-secret-123")
	r.NoError(err)
	alice.PasswordHash = &hash
	secret := "JBSWY3DPEHPK3PXP"
	alice.TOTPSecret = &secret
	alice.TOTPEnabled = true
	r.NoError(dbSvc.CreateUser(ctx, alice))

	resp, err := svc.CreateInvitation(ctx, org.Slug, bob.UID, InviteRequest{
		Email: inviteEmail, Role: string(models.MemberRoleUser), ExpiresIn: "24h", App: "dash0",
	})
	r.NoError(err)

	return &acceptInviteFixture{svc: svc, db: dbSvc, org: org, alice: alice, bob: bob, token: resp.Token}
}

// assertNothingWritten checks a refused acceptance left no trace: no
// membership for the invitee, and the invitation still listed.
func (f *acceptInviteFixture) assertNothingWritten(t *testing.T, ctx context.Context, inviteeUID string) {
	t.Helper()
	r := require.New(t)

	if inviteeUID != "" {
		_, err := f.db.GetMemberByUserAndOrg(ctx, inviteeUID, f.org.UID)
		r.Error(err, "a refused acceptance must create no membership")
	}

	entries, err := f.db.ListStateEntries(ctx, &f.org.UID, inviteKeyPrefix)
	r.NoError(err)
	r.Len(entries, 1, "a refused acceptance must leave the invitation in place")
}

// TestAcceptInviteRequiresTheInvitedAccount is the C3 regression (spec
// 2026-10-16-06): before the fix, an anonymous acceptance of an invitation
// addressed to an existing account returned that account's session.
func TestAcceptInviteRequiresTheInvitedAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inviteEmail string
		password    string
		// caller picks the authenticated caller ("" = anonymous).
		caller func(f *acceptInviteFixture) string
		// preMember makes alice a member of the org before accepting.
		preMember bool
		wantErr   error
	}{
		{
			name: "anonymous, existing account, no password", inviteEmail: "alice@acme.com",
			caller: func(*acceptInviteFixture) string { return "" }, wantErr: ErrInviteLoginRequired,
		},
		{
			name: "anonymous, existing account, any password", inviteEmail: "alice@acme.com",
			password: "whatever-password",
			caller:   func(*acceptInviteFixture) string { return "" }, wantErr: ErrInviteLoginRequired,
		},
		{
			name: "anonymous, existing account, her real password", inviteEmail: "alice@acme.com",
			password: "alice-secret-123",
			caller:   func(*acceptInviteFixture) string { return "" }, wantErr: ErrInviteLoginRequired,
		},
		{
			name: "caller is the inviter", inviteEmail: "alice@acme.com",
			caller: func(f *acceptInviteFixture) string { return f.bob.UID }, wantErr: ErrInviteEmailMismatch,
		},
		{
			name: "already a member, anonymous", inviteEmail: "alice@acme.com", preMember: true,
			caller: func(*acceptInviteFixture) string { return "" }, wantErr: ErrInviteLoginRequired,
		},
		{
			name: "new email, signed-in caller", inviteEmail: "newcomer@acme.com",
			password: "newcomer-secret-1",
			caller:   func(f *acceptInviteFixture) string { return f.alice.UID }, wantErr: ErrInviteEmailMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()
			f := newAcceptInviteFixture(t, tc.inviteEmail)

			if tc.preMember {
				r.NoError(f.db.CreateOrganizationMember(ctx,
					models.NewOrganizationMember(f.org.UID, f.alice.UID, models.MemberRoleViewer)))
			}

			resp, err := f.svc.AcceptInvite(ctx, AcceptInviteRequest{
				Token: f.token, Name: "Someone", Password: tc.password,
			}, tc.caller(f))
			r.ErrorIs(err, tc.wantErr)
			r.Nil(resp, "a refused acceptance must mint no session")

			inviteeUID := ""
			if !tc.preMember {
				invitee, getErr := f.db.GetUserByEmail(ctx, tc.inviteEmail)
				switch {
				case getErr == nil && invitee != nil:
					inviteeUID = invitee.UID
				case getErr != nil && !errors.Is(getErr, sql.ErrNoRows):
					r.NoError(getErr)
				}
			}

			if tc.inviteEmail != f.alice.Email {
				r.Empty(inviteeUID, "a refused acceptance must create no account")
			}

			f.assertNothingWritten(t, ctx, inviteeUID)
		})
	}
}

// TestAcceptInviteByTheInvitedAccount is the positive control: alice, signed
// in, accepts her own invitation.
func TestAcceptInviteByTheInvitedAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	f := newAcceptInviteFixture(t, "alice@acme.com")

	resp, err := f.svc.AcceptInvite(ctx, AcceptInviteRequest{Token: f.token}, f.alice.UID)
	r.NoError(err)
	r.NotEmpty(resp.AccessToken)
	r.NotNil(resp.Organization)
	r.Equal(f.org.Slug, resp.Organization.Slug)

	member, err := f.db.GetMemberByUserAndOrg(ctx, f.alice.UID, f.org.UID)
	r.NoError(err)
	r.Equal(models.MemberRoleUser, member.Role)

	entries, err := f.db.ListStateEntries(ctx, &f.org.UID, inviteKeyPrefix)
	r.NoError(err)
	r.Empty(entries, "an accepted invitation must be deleted")
}

// TestAcceptInviteNewAccountAnonymous: the new-user path is unchanged.
func TestAcceptInviteNewAccountAnonymous(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	f := newAcceptInviteFixture(t, "newcomer@acme.com")

	resp, err := f.svc.AcceptInvite(ctx, AcceptInviteRequest{
		Token: f.token, Name: "Newcomer", Password: "newcomer-secret-1",
	}, "")
	r.NoError(err)
	r.NotEmpty(resp.AccessToken)
	r.Equal(f.org.Slug, resp.Organization.Slug)

	user, err := f.db.GetUserByEmail(ctx, "newcomer@acme.com")
	r.NoError(err)
	r.NotNil(user.EmailVerifiedAt)

	_, err = f.db.GetMemberByUserAndOrg(ctx, user.UID, f.org.UID)
	r.NoError(err)
}

// TestAcceptInviteHandlerErrorShapes pins the HTTP shapes of the two refusals.
func TestAcceptInviteHandlerErrorShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		claims     func(f *acceptInviteFixture) *Claims
		wantStatus int
		wantCode   base.ErrorCode
	}{
		{
			name:       "anonymous",
			claims:     func(*acceptInviteFixture) *Claims { return nil },
			wantStatus: http.StatusUnauthorized,
			wantCode:   base.ErrorCodeInviteLoginRequired,
		},
		{
			name:       "another account",
			claims:     func(f *acceptInviteFixture) *Claims { return &Claims{UserUID: f.bob.UID, OrgSlug: f.org.Slug} },
			wantStatus: http.StatusForbidden,
			wantCode:   base.ErrorCodeInviteEmailMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()
			f := newAcceptInviteFixture(t, "alice@acme.com")

			reqCtx := ctx
			if claims := tc.claims(f); claims != nil {
				reqCtx = context.WithValue(ctx, base.ContextKeyClaims, claims)
			}

			handler := NewHandler(f.svc, &config.Config{})
			httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/api/v1/auth/accept-invite",
				strings.NewReader(`{"token":"`+f.token+`","password":"whatever-password"}`))
			rec := httptest.NewRecorder()

			r.NoError(handler.AcceptInvite(rec, httpReq))
			r.Equal(tc.wantStatus, rec.Code, rec.Body.String())

			code, _ := decodeErrorResponse(t, rec)
			r.Equal(string(tc.wantCode), code)
			r.Empty(rec.Result().Cookies(), "a refused acceptance must set no session cookie")
		})
	}
}
