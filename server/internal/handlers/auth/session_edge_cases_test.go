package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// edgeWorld is a faultWorld whose store can fail the queries a test names.
type edgeWorld struct {
	*faultWorld

	fault *queryFault
}

func newEdgeWorld(t *testing.T) *edgeWorld {
	t.Helper()

	w := newFaultWorld(t)

	return &edgeWorld{faultWorld: w, fault: installQueryFault(w.dbSvc)}
}

// edgePassword is the password of the accounts withPassword prepares.
const edgePassword = "testpass1234"

// edgePasswordHash derives the hash of edgePassword once for the whole run:
// a derivation is the most expensive step of these fixtures.
//
//nolint:gochecknoglobals // test fixture, computed once
var edgePasswordHash = sync.OnceValues(func() (string, error) { return passwords.Hash(edgePassword) })

// signedIn seeds an organization and a member of it who holds a session, like
// faultWorld.member but without a password: the session comes from an
// organization switch, so the fixture costs no password derivation.
func (w *edgeWorld) signedIn(t *testing.T, role models.MemberRole) (string, *models.User, *LoginResponse) {
	t.Helper()

	id := w.n.Add(1)
	slug := fmt.Sprintf("edge-%d", id)

	org := models.NewOrganization(slug, "Edge "+slug)
	require.NoError(t, w.dbSvc.CreateOrganization(w.ctx, org))

	user := models.NewUser(fmt.Sprintf("user%d@acme.com", id))
	require.NoError(t, w.dbSvc.CreateUser(w.ctx, user))
	require.NoError(t, w.dbSvc.CreateOrganizationMember(w.ctx, models.NewOrganizationMember(org.UID, user.UID, role)))

	session, err := w.svc.SwitchOrg(w.ctx, user.UID, slug, edgeAuthContext)
	require.NoError(t, err)

	return slug, user, session
}

// withPassword gives the user the password edgePassword.
func (w *edgeWorld) withPassword(t *testing.T, user *models.User) {
	t.Helper()

	hash, err := edgePasswordHash()
	require.NoError(t, err)
	require.NoError(t, w.dbSvc.UpdateUser(w.ctx, user.UID, &models.UserUpdate{PasswordHash: &hash}))

	user.PasswordHash = &hash
}

// org returns the organization behind slug.
func (w *edgeWorld) org(t *testing.T, slug string) *models.Organization {
	t.Helper()

	org, err := w.dbSvc.GetOrganizationBySlug(w.ctx, slug)
	require.NoError(t, err)

	return org
}

// leave removes the user's membership of the organization.
func (w *edgeWorld) leave(t *testing.T, slug, userUID string) {
	t.Helper()

	member, err := w.dbSvc.GetMemberByUserAndOrg(w.ctx, userUID, w.org(t, slug).UID)
	require.NoError(t, err)
	require.NoError(t, w.dbSvc.DeleteOrganizationMember(w.ctx, member.UID))
}

// promote makes the user a super admin.
func (w *edgeWorld) promote(t *testing.T, userUID string) {
	t.Helper()

	superAdmin := true
	require.NoError(t, w.dbSvc.UpdateUser(w.ctx, userUID, &models.UserUpdate{SuperAdmin: &superAdmin}))
}

// token stores a token row of the given kind and returns its raw value.
func (w *edgeWorld) token(
	t *testing.T, userUID string, orgUID *string, kind models.TokenType, mutate func(*models.UserToken),
) (string, *models.UserToken) {
	t.Helper()

	value := fmt.Sprintf("raw-token-%d", w.n.Add(1))
	row := models.NewUserToken(userUID, orgUID, value, kind)

	if mutate != nil {
		mutate(row)
	}

	require.NoError(t, w.dbSvc.CreateUserToken(w.ctx, row))

	return value, row
}

func (w *edgeWorld) sessionCount(t *testing.T, userUID string) int {
	t.Helper()

	rows, err := w.dbSvc.ListUserTokensByType(w.ctx, userUID, models.TokenTypeRefresh)
	require.NoError(t, err)

	return len(rows)
}

var edgeAuthContext = Context{UserAgent: "ua", RemoteAddr: "127.0.0.1"} //nolint:gochecknoglobals // test fixture

// TestUnknownUserIsNotFound: a call made for a user that no longer exists
// answers ErrUserNotFound, never a raw store error or a success.
func TestUnknownUserIsNotFound(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	w := newEdgeWorld(t)
	slug, _, _ := w.signedIn(t, models.MemberRoleOwner)
	ghost := uuidV7(t)
	locale := "fr"

	cases := map[string]func() error{
		"logout user": func() error {
			_, err := w.svc.LogoutUser(w.ctx, ghost)

			return err
		},
		"logout other sessions": func() error {
			_, err := w.svc.LogoutOtherSessions(w.ctx, ghost, "")

			return err
		},
		"create a personal access token": func() error {
			_, err := w.svc.CreatePAT(w.ctx, slug, ghost, CreateTokenRequest{Name: "ci"})

			return err
		},
		"list tokens": func() error {
			_, err := w.svc.GetUserTokens(w.ctx, slug, ghost, "", "")

			return err
		},
		"switch organization": func() error {
			_, err := w.svc.SwitchOrg(w.ctx, ghost, slug, edgeAuthContext)

			return err
		},
		"me, session without organization": func() error {
			_, err := w.svc.GetUserInfo(w.ctx, &Claims{UserUID: ghost})

			return err
		},
		"update preferences": func() error {
			_, err := w.svc.UpdateProfile(w.ctx, &Claims{UserUID: ghost, OrgSlug: slug},
				UpdateProfileRequest{Preferences: &PreferencesRequest{Locale: &locale}})

			return err
		},
		"confirm 2FA": func() error {
			_, err := w.svc.Confirm2FA(w.ctx, ghost, Confirm2FARequest{Code: "000000"})

			return err
		},
		"disable 2FA": func() error {
			return w.svc.Disable2FA(w.ctx, ghost, "000000")
		},
	}

	for name, call := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(name, func(t *testing.T) { //nolint:paralleltest // shared fixture
			require.ErrorIs(t, call(), ErrUserNotFound)
		})
	}
}

// TestFormerMemberIsNotFoundInTheOrg: once the membership is gone, the
// credentials the user still holds for that organization stop working.
func TestFormerMemberIsNotFoundInTheOrg(t *testing.T) {
	t.Parallel()

	t.Run("refresh", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, login := w.signedIn(t, models.MemberRoleUser)
		w.leave(t, slug, user.UID)

		_, err := w.svc.Refresh(w.ctx, login.RefreshToken)
		require.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("personal access token", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleUser)
		pat, err := w.svc.CreatePAT(w.ctx, slug, user.UID, CreateTokenRequest{Name: "ci"})
		require.NoError(t, err)
		w.leave(t, slug, user.UID)

		_, err = w.svc.ValidatePATToken(w.ctx, pat.Token)
		require.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("me and token listing", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, login := w.signedIn(t, models.MemberRoleUser)
		claims, err := w.svc.ValidateToken(w.ctx, login.AccessToken)
		require.NoError(t, err)
		w.leave(t, slug, user.UID)

		_, err = w.svc.GetUserInfo(w.ctx, claims)
		require.ErrorIs(t, err, ErrUserNotFound)

		_, err = w.svc.GetUserTokens(w.ctx, slug, user.UID, "", "")
		require.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refresh of a deleted user", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		_, user, login := w.signedIn(t, models.MemberRoleUser)
		require.NoError(t, w.dbSvc.DeleteUser(w.ctx, user.UID))

		_, err := w.svc.Refresh(w.ctx, login.RefreshToken)
		require.ErrorIs(t, err, ErrUserNotFound)
	})
}

// TestSuperAdminNeedsNoMembership: a super admin acts in an organization they
// are not a member of, with the super admin role.
func TestSuperAdminNeedsNoMembership(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newEdgeWorld(t)
	slug, user, login := w.signedIn(t, models.MemberRoleUser)
	pat, err := w.svc.CreatePAT(w.ctx, slug, user.UID, CreateTokenRequest{Name: "ci"})
	r.NoError(err)

	w.leave(t, slug, user.UID)
	w.promote(t, user.UID)

	refreshed, err := w.svc.Refresh(w.ctx, login.RefreshToken)
	r.NoError(err)
	r.Equal(RoleSuperAdmin, refreshed.User.Role)

	patClaims, err := w.svc.ValidatePATToken(w.ctx, pat.Token)
	r.NoError(err)
	r.Equal(RoleSuperAdmin, patClaims.Role)

	me, err := w.svc.GetUserInfo(w.ctx, &Claims{UserUID: user.UID, OrgSlug: slug})
	r.NoError(err)
	r.Equal(RoleSuperAdmin, me.User.Role)
	r.Equal(slug, me.Organization.Slug)

	noOrg, err := w.svc.GetUserInfo(w.ctx, &Claims{UserUID: user.UID})
	r.NoError(err)
	r.Equal(RoleSuperAdmin, noOrg.User.Role)
	r.Nil(noOrg.Organization)

	switched, err := w.svc.SwitchOrg(w.ctx, user.UID, slug, edgeAuthContext)
	r.NoError(err)
	r.Equal(RoleSuperAdmin, switched.User.Role)
	r.Equal(slug, switched.Organization.Slug)
}

// TestSuperAdminLoginOrgResolution pins which organization a super admin
// lands in, depending on the slug they asked for and their memberships.
func TestSuperAdminLoginOrgResolution(t *testing.T) {
	t.Parallel()

	t.Run("no slug, a previous session: back to that organization", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		w.promote(t, user.UID)

		resp, err := w.svc.Login(w.ctx, "", user.Email, edgePassword, edgeAuthContext)
		require.NoError(t, err)
		require.Equal(t, LoginActionDefault, resp.LoginAction)
		require.Equal(t, slug, resp.Organization.Slug)
		require.Equal(t, RoleSuperAdmin, resp.User.Role)
	})

	t.Run("unknown slug, one membership: redirected to it", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		w.promote(t, user.UID)

		resp, err := w.svc.Login(w.ctx, "no-such-org", user.Email, edgePassword, edgeAuthContext)
		require.NoError(t, err)
		require.Equal(t, LoginActionOrgRedirect, resp.LoginAction)
		require.Equal(t, slug, resp.Organization.Slug)
	})

	t.Run("unknown slug, no membership: no organization", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		w.leave(t, slug, user.UID)
		w.promote(t, user.UID)

		resp, err := w.svc.Login(w.ctx, "no-such-org", user.Email, edgePassword, edgeAuthContext)
		require.NoError(t, err)
		require.Equal(t, LoginActionNoOrg, resp.LoginAction)
		require.Nil(t, resp.Organization)
		require.Empty(t, resp.RefreshToken, "no organization, no session row")
	})

	t.Run("unknown slug, organizations cannot be read: no organization", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		_, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		w.promote(t, user.UID)

		second := models.NewOrganization("second-org", "Second")
		require.NoError(t, w.dbSvc.CreateOrganization(w.ctx, second))

		for _, memberships := range []int{1, 2} {
			if memberships == 2 {
				require.NoError(t, w.dbSvc.CreateOrganizationMember(w.ctx,
					models.NewOrganizationMember(second.UID, user.UID, models.MemberRoleViewer)))
			}

			var (
				resp *LoginResponse
				err  error
			)

			w.fault.during(t, failingSQL(`FROM "organizations"`), func() {
				resp, err = w.svc.Login(w.ctx, "no-such-org", user.Email, edgePassword, edgeAuthContext)
			})
			require.NoError(t, err)
			require.Nil(t, resp.Organization, "%d membership(s)", memberships)
			require.Empty(t, resp.RefreshToken)
			require.Len(t, resp.Organizations, memberships)
			require.Equal(t, RoleSuperAdmin, resp.User.Role)
		}
	})

	t.Run("membership listing fails: the login still succeeds", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		w.promote(t, user.UID)

		var (
			resp *LoginResponse
			err  error
		)

		w.fault.during(t, failingSQL(`FROM "organization_members"`), func() {
			resp, err = w.svc.Login(w.ctx, slug, user.Email, edgePassword, edgeAuthContext)
		})
		require.NoError(t, err)
		require.Equal(t, slug, resp.Organization.Slug)
		require.Empty(t, resp.Organizations, "the list is decorative and was not loaded")
	})
}

// TestLoginOrgResolutionWhenTheStoreFails: a store failure while picking the
// organization to land in.
func TestLoginOrgResolutionWhenTheStoreFails(t *testing.T) {
	t.Parallel()

	t.Run("no slug, memberships cannot be listed: refused", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		_, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)

		// No earlier session to go back to: the default organization comes
		// from a second membership listing, the one that fails.
		_, err := w.svc.LogoutUser(w.ctx, user.UID)
		require.NoError(t, err)

		w.fault.during(t, failingSQL(`FROM "organization_members"`).after(1), func() {
			_, err = w.svc.Login(w.ctx, "", user.Email, edgePassword, edgeAuthContext)
		})
		require.ErrorIs(t, err, ErrInvalidCredentials)
		require.Zero(t, w.sessionCount(t, user.UID))
	})

	t.Run("no slug, organizations cannot be read: refused", func(t *testing.T) {
		t.Parallel()

		w := newEdgeWorld(t)
		_, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		before := w.sessionCount(t, user.UID)

		var err error

		w.fault.during(t, failingSQL(`FROM "organizations"`), func() {
			_, err = w.svc.Login(w.ctx, "", user.Email, edgePassword, edgeAuthContext)
		})
		require.ErrorIs(t, err, ErrInvalidCredentials)
		require.Equal(t, before, w.sessionCount(t, user.UID))
	})

	t.Run("unknown slug, two memberships: falls back to the first one", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		first, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)

		second := models.NewOrganization("second-org", "Second")
		r.NoError(w.dbSvc.CreateOrganization(w.ctx, second))
		r.NoError(w.dbSvc.CreateOrganizationMember(w.ctx,
			models.NewOrganizationMember(second.UID, user.UID, models.MemberRoleViewer)))

		var (
			resp *LoginResponse
			err  error
		)

		w.fault.during(t, failingSQL(`FROM "organizations"`), func() {
			resp, err = w.svc.Login(w.ctx, "no-such-org", user.Email, edgePassword, edgeAuthContext)
		})
		r.NoError(err)
		r.Equal(LoginActionOrgChoice, resp.LoginAction)
		r.Len(resp.Organizations, 2)
		r.Contains([]string{first, second.Slug}, resp.Organization.Slug)
		r.Equal(resp.Organizations[0].Slug, resp.Organization.Slug)
		r.Equal(resp.Organizations[0].Role, resp.User.Role)
	})
}

// TestTokenOfTheWrongKindIsRefused: a token row is only accepted by the flow
// it was minted for.
func TestTokenOfTheWrongKindIsRefused(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	w := newEdgeWorld(t)
	slug, user, login := w.signedIn(t, models.MemberRoleOwner)
	_, other, otherLogin := w.signedIn(t, models.MemberRoleOwner)
	orgUID := w.org(t, slug).UID
	past := time.Now().Add(-time.Hour)

	patAsRefresh, _ := w.token(t, user.UID, &orgUID, models.TokenTypePAT, nil)
	expiredRefresh, _ := w.token(t, user.UID, &orgUID, models.TokenTypeRefresh, func(row *models.UserToken) {
		row.ExpiresAt = &past
	})
	orglessRefresh, _ := w.token(t, user.UID, nil, models.TokenTypeRefresh, nil)
	refreshAsPAT, _ := w.token(t, user.UID, &orgUID, models.TokenTypeRefresh, nil)
	orglessPAT, _ := w.token(t, user.UID, nil, models.TokenTypePAT, nil)

	otherSession, err := w.dbSvc.GetUserTokenByToken(w.ctx, otherLogin.RefreshToken)
	require.NoError(t, err)

	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"refresh with a personal access token", func() error {
			_, err := w.svc.Refresh(w.ctx, patAsRefresh)

			return err
		}, ErrInvalidToken},
		{"refresh with an expired token", func() error {
			_, err := w.svc.Refresh(w.ctx, expiredRefresh)

			return err
		}, ErrTokenExpired},
		{"refresh with a token bound to no organization", func() error {
			_, err := w.svc.Refresh(w.ctx, orglessRefresh)

			return err
		}, ErrInvalidToken},
		{"refresh token presented as a personal access token", func() error {
			_, err := w.svc.ValidatePATToken(w.ctx, refreshAsPAT)

			return err
		}, ErrInvalidToken},
		{"personal access token bound to no organization", func() error {
			_, err := w.svc.ValidatePATToken(w.ctx, orglessPAT)

			return err
		}, ErrInvalidToken},
		{"revoke the token of another user", func() error {
			return w.svc.RevokeToken(w.ctx, user.UID, otherSession.UID)
		}, ErrTokenNotFound},
	}

	for _, tc := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.call(), tc.want)
		})
	}

	// Logout leaves the sessions of others alone.
	{
		r := require.New(t)

		r.NoError(w.svc.LogoutSession(w.ctx, user.UID, otherSession.UID))
		r.NoError(w.svc.LogoutSession(w.ctx, user.UID, uuidV7(t)))
		r.NoError(w.svc.Logout(w.ctx, "no-such-refresh-token"))

		_, err := w.svc.Refresh(w.ctx, otherLogin.RefreshToken)
		r.NoError(err, "the other user's session is still alive")
		_, err = w.svc.Refresh(w.ctx, login.RefreshToken)
		r.NoError(err)
		r.Equal(1, w.sessionCount(t, other.UID))
	}
}

// TestExpiredTokensAreNotListed: both token listings hide expired rows.
func TestExpiredTokensAreNotListed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newEdgeWorld(t)
	slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
	orgUID := w.org(t, slug).UID
	past := time.Now().Add(-time.Hour)

	_, expired := w.token(t, user.UID, &orgUID, models.TokenTypePAT, func(row *models.UserToken) {
		row.ExpiresAt = &past
	})
	_, live := w.token(t, user.UID, &orgUID, models.TokenTypePAT, nil)

	uids := func(infos []TokenInfo) []string {
		out := make([]string, 0, len(infos))
		for i := range infos {
			out = append(out, infos[i].UID)
		}

		return out
	}

	scoped, err := w.svc.GetUserTokens(w.ctx, slug, user.UID, string(models.TokenTypePAT), "")
	r.NoError(err)
	r.Equal([]string{live.UID}, uids(scoped.Data))

	all, err := w.svc.GetAllUserTokens(w.ctx, user.UID, string(models.TokenTypePAT), "")
	r.NoError(err)
	r.Equal([]string{live.UID}, uids(all.Data))
	r.NotContains(uids(all.Data), expired.UID)
}

// TestSessionBestEffortWritesNeverFailTheCall: bookkeeping writes around a
// session (cap pruning, activity stamps, bulk deletes) are best effort. When
// one fails, the call still answers and the rows it could not touch stay.
func TestSessionBestEffortWritesNeverFailTheCall(t *testing.T) {
	t.Parallel()

	softDelete := failingSQL(`UPDATE "user_tokens"`, "deleted_at")

	t.Run("pruning sessions over the cap", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		w.withPassword(t, user)
		orgUID := w.org(t, slug).UID

		// Rows that were never refreshed: their age is their creation time.
		for range maxActiveSessions {
			w.token(t, user.UID, &orgUID, models.TokenTypeRefresh, nil)
		}

		before := w.sessionCount(t, user.UID)
		r.Greater(before, maxActiveSessions)

		var err error

		w.fault.during(t, softDelete, func() {
			_, err = w.svc.Login(w.ctx, slug, user.Email, edgePassword, edgeAuthContext)
		})
		r.NoError(err)
		r.Equal(before+1, w.sessionCount(t, user.UID), "nothing could be pruned")

		_, err = w.svc.Login(w.ctx, slug, user.Email, edgePassword, edgeAuthContext)
		r.NoError(err)
		r.Equal(maxActiveSessions, w.sessionCount(t, user.UID), "the next login prunes down to the cap")
	})

	t.Run("signing out other sessions", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		slug, user, login := w.signedIn(t, models.MemberRoleOwner)
		orgUID := w.org(t, slug).UID
		current, err := w.dbSvc.GetUserTokenByToken(w.ctx, login.RefreshToken)
		r.NoError(err)

		for range 3 {
			w.token(t, user.UID, &orgUID, models.TokenTypeRefresh, nil)
		}

		var resp *LogoutResponse

		w.fault.during(t, softDelete.after(1), func() {
			resp, err = w.svc.LogoutOtherSessions(w.ctx, user.UID, current.UID)
		})
		r.NoError(err)
		r.Equal(1, resp.TokensDeleted, "only the delete that went through is counted")
		r.Equal(3, w.sessionCount(t, user.UID), "the caller's session and the two that failed")
	})

	t.Run("signing out everywhere", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		orgUID := w.org(t, slug).UID
		_, grant := w.token(t, user.UID, &orgUID, models.TokenTypeOAuthRefresh, nil)

		var (
			resp *LogoutResponse
			err  error
		)

		w.fault.during(t, softDelete, func() {
			resp, err = w.svc.LogoutUser(w.ctx, user.UID)
		})
		r.NoError(err)
		r.Zero(resp.TokensDeleted, "no delete went through, none is counted")
		r.Equal(1, w.sessionCount(t, user.UID))

		_, err = w.dbSvc.GetUserToken(w.ctx, grant.UID)
		r.NoError(err, "the OAuth grant is still there too")

		resp, err = w.svc.LogoutUser(w.ctx, user.UID)
		r.NoError(err)
		r.Equal(1, resp.TokensDeleted)
		r.Zero(w.sessionCount(t, user.UID))
	})

	t.Run("seeding the weekly report of a new organization", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		_, user, _ := w.signedIn(t, models.MemberRoleOwner)

		var (
			resp *OrgResponse
			err  error
		)

		w.fault.during(t, failingSQL(`INSERT INTO "report_schedules"`), func() {
			resp, err = w.svc.CreateOrg(w.ctx, user.UID, CreateOrgRequest{Name: "Second Org"}, edgeAuthContext)
		})
		r.NoError(err)

		schedules, err := w.dbSvc.ListReportSchedules(w.ctx, resp.UID)
		r.NoError(err)
		r.Empty(schedules, "the organization exists without its default report")

		// Control: with a healthy store the default report is seeded.
		resp, err = w.svc.CreateOrg(w.ctx, user.UID, CreateOrgRequest{Name: "Third Org"}, edgeAuthContext)
		r.NoError(err)

		schedules, err = w.dbSvc.ListReportSchedules(w.ctx, resp.UID)
		r.NoError(err)
		r.Len(schedules, 1)
	})

	t.Run("sliding the session expiry on refresh", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		orgUID := w.org(t, slug).UID
		idleSince := time.Now().Add(-2 * time.Hour)
		value, row := w.token(t, user.UID, &orgUID, models.TokenTypeRefresh, func(row *models.UserToken) {
			row.LastActiveAt = &idleSince
		})

		var (
			resp *LoginResponse
			err  error
		)

		w.fault.during(t, failingSQL(`UPDATE "user_tokens"`), func() {
			resp, err = w.svc.Refresh(w.ctx, value)
		})
		r.NoError(err)
		r.NotEmpty(resp.AccessToken)

		stored, err := w.dbSvc.GetUserToken(w.ctx, row.UID)
		r.NoError(err)
		r.WithinDuration(idleSince, *stored.LastActiveAt, time.Second, "the stamp was not written")
	})

	t.Run("stamping the user's last activity on a federated login", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newEdgeWorld(t)
		slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
		before := w.sessionCount(t, user.UID)

		var (
			resp *OAuthLoginResponse
			err  error
		)

		w.fault.during(t, failingSQL(`UPDATE "users"`), func() {
			resp, err = w.svc.GenerateTokensForOAuth(
				w.ctx, user, w.org(t, slug), string(models.MemberRoleOwner), "", Context{})
		})
		r.NoError(err)
		r.NotEmpty(resp.RefreshToken)
		r.Equal(before+1, w.sessionCount(t, user.UID))
	})
}

// TestMeWithoutOrganizationSurfacesStoreFailures: the zero-org /auth/me
// answers an error when one of its lookups fails, not a partial profile.
func TestMeWithoutOrganizationSurfacesStoreFailures(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	w := newEdgeWorld(t)
	_, user, _ := w.signedIn(t, models.MemberRoleOwner)
	claims := &Claims{UserUID: user.UID}

	cases := map[string]sqlFault{
		"user":                failingSQL(`FROM "users"`),
		"memberships":         failingSQL(`FROM "organization_members"`),
		"membership requests": failingSQL(`FROM "membership_requests"`),
	}

	for name, fault := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(name, func(t *testing.T) {
			var (
				resp *MeResponse
				err  error
			)

			w.fault.during(t, fault, func() {
				resp, err = w.svc.GetUserInfo(w.ctx, claims)
			})
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, resp)
		})
	}

	// Control: it answers when the store is healthy.
	{
		resp, err := w.svc.GetUserInfo(w.ctx, claims)
		require.NoError(t, err)
		require.Equal(t, user.Email, resp.User.Email)
		require.Nil(t, resp.Organization)
	}
}
