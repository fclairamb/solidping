package auth

import (
	"context"
	"encoding/json"
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

// Spec 2026-08-10-03 (GitHub issue #206): deleting an organization used to sign
// the deleter out — the handler revoked the caller's own session and answered
// 204, and /auth/me then 401ed on the vanished org slug, so a plain reload
// finished the job. These tests drive the real HTTP handlers and pin the
// replacement behavior: the caller stays authenticated, lands on a surviving
// org or on an org-less session, while every OTHER member is still signed out.

// deletionEnv bundles the pieces the org-deletion HTTP tests drive.
type deletionEnv struct {
	svc     *Service
	db      db.Service
	handler *Handler
	ctx     context.Context //nolint:containedctx // test fixture, scoped to one test
}

func newDeletionEnv(t *testing.T) deletionEnv {
	t.Helper()

	svc, dbService, ctx := setupAuthTestService(t)

	return deletionEnv{svc: svc, db: dbService, handler: NewHandler(svc, &config.Config{}), ctx: ctx}
}

// newUserWithOrgs creates a user and the given orgs, each owned by them, and
// returns the session minted for the LAST org created (the one the caller is
// "currently in", exactly as the dashboard would be).
func (e deletionEnv) newUserWithOrgs(t *testing.T, email string, slugs ...string) *OrgResponse {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, e.db.CreateUser(e.ctx, user))

	var last *OrgResponse

	for _, slug := range slugs {
		resp, err := e.svc.CreateOrg(e.ctx, user.UID, CreateOrgRequest{Name: slug, Slug: slug}, Context{})
		require.NoError(t, err)

		last = resp
	}

	return last
}

// claimsFor decodes an access token the way the auth middleware does.
func (e deletionEnv) claimsFor(t *testing.T, accessToken string) *Claims {
	t.Helper()

	claims, err := e.svc.ValidateToken(e.ctx, accessToken)
	require.NoError(t, err)

	return claims
}

// deleteOrg drives DELETE /api/v1/orgs/:org through the HTTP handler, with the
// chi route param and the claims the middleware would have put in place.
func (e deletionEnv) deleteOrg(
	t *testing.T, claims *Claims, orgSlug, confirmSlug string,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	body := `{"slug":"` + confirmSlug + `"}`
	req := httptest.NewRequestWithContext(e.ctx, http.MethodDelete,
		"/api/v1/orgs/"+orgSlug, strings.NewReader(body))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(fieldOrg, orgSlug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), base.ContextKeyClaims, claims))
	}

	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.DeleteOrg(rec, req))

	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	}

	return rec, decoded
}

// me drives GET /api/v1/auth/me with the given claims.
func (e deletionEnv) me(t *testing.T, claims *Claims) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequestWithContext(
		context.WithValue(e.ctx, base.ContextKeyClaims, claims),
		http.MethodGet, "/api/v1/auth/me", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.Me(rec, req))

	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	}

	return rec, decoded
}

// refresh drives POST /api/v1/auth/refresh with a raw refresh-token value.
func (e deletionEnv) refresh(t *testing.T, refreshToken string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(e.ctx, http.MethodPost, "/api/v1/auth/refresh",
		strings.NewReader(`{"refreshToken":"`+refreshToken+`"}`))
	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.Refresh(rec, req))

	return rec
}

// TestDeleteOrgAnswersAReplacementSession is spec §3 (a) and (b): whichever way
// the caller's membership list looks afterwards, DELETE /orgs/:org answers 200
// with a usable session rather than 204 + a dead cookie.
func TestDeleteOrgAnswersAReplacementSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// orgs are created in order; the LAST one is the one being deleted.
		orgs          []string
		wantOrgSlug   string // "" means the session must be org-less
		wantRefresh   bool
		wantAction    string
		wantRemaining int
	}{
		{
			name:          "a caller with another org lands on it",
			orgs:          []string{"keeper", "doomed"},
			wantOrgSlug:   "keeper",
			wantRefresh:   true,
			wantAction:    string(LoginActionOrgRedirect),
			wantRemaining: 1,
		},
		{
			name:          "a caller with no other org gets an org-less session",
			orgs:          []string{"only-one"},
			wantOrgSlug:   "",
			wantRefresh:   false,
			wantAction:    string(LoginActionNoOrg),
			wantRemaining: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			env := newDeletionEnv(t)
			doomed := tc.orgs[len(tc.orgs)-1]
			current := env.newUserWithOrgs(t, "owner-"+doomed+"@example.com", tc.orgs...)

			rec, body := env.deleteOrg(t, env.claimsFor(t, current.AccessToken), doomed, doomed)
			r.Equal(http.StatusOK, rec.Code, "the deleter must get a session, not a 204")

			accessToken, _ := body["accessToken"].(string)
			r.NotEmpty(accessToken, "the response must carry a replacement access token")

			// The org really is gone — otherwise every assertion below would be
			// vacuously satisfied by a delete that silently did nothing.
			_, err := env.db.GetOrganizationBySlug(env.ctx, doomed)
			r.Error(err)

			newClaims := env.claimsFor(t, accessToken)
			r.Equal(tc.wantOrgSlug, newClaims.OrgSlug, "the new token must be scoped to the surviving org (or to none)")

			if tc.wantOrgSlug == "" {
				r.NotContains(body, "organization")
			} else {
				org, ok := body["organization"].(map[string]any)
				r.True(ok, "an org-scoped session must name its organization")
				r.Equal(tc.wantOrgSlug, org["slug"])
			}

			refreshToken, _ := body["refreshToken"].(string)
			if tc.wantRefresh {
				r.NotEmpty(refreshToken, "an org-scoped session carries a refresh grant")
				r.Equal(http.StatusOK, env.refresh(t, refreshToken).Code,
					"the replacement refresh token must actually work")
			} else {
				// Refresh grants are org-scoped; an org-less session has none by
				// design (same shape as a zero-org login).
				r.Empty(refreshToken)
			}

			r.Equal(tc.wantAction, body["loginAction"])

			orgs, _ := body["organizations"].([]any)
			r.Len(orgs, tc.wantRemaining, "the switcher list must no longer contain the deleted org")

			// And the replacement session is genuinely usable: /auth/me answers
			// 200 with the right org context.
			meRec, meBody := env.me(t, newClaims)
			r.Equal(http.StatusOK, meRec.Code)

			if tc.wantOrgSlug == "" {
				r.Nil(meBody["organization"], "an org-less session reports organization: null")
			} else {
				meOrg, ok := meBody["organization"].(map[string]any)
				r.True(ok)
				r.Equal(tc.wantOrgSlug, meOrg["slug"])
			}
		})
	}
}

// TestDeleteOrgStillRevokesOtherMembersSessions is spec §3 (c), the negative
// control for the whole feature: keeping the DELETER signed in must not have
// quietly kept everyone ELSE signed in too. Revoking other members' sessions is
// deliberate (spec 2026-08-08-11), so this test is what stops the fix from
// turning into a security regression.
//
// The pre-delete refresh is the positive control: it proves the token was a
// working one, so the 401 afterwards is about the revocation and not about a
// malformed fixture.
func TestDeleteOrgStillRevokesOtherMembersSessions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDeletionEnv(t)
	current := env.newUserWithOrgs(t, "owner@revoke.example", "shared-org")

	// A second member with a real, live refresh grant on the same org.
	member := models.NewUser("member@revoke.example")
	r.NoError(env.db.CreateUser(env.ctx, member))

	org, err := env.db.GetOrganizationBySlug(env.ctx, "shared-org")
	r.NoError(err)

	joined := time.Now()
	membership := models.NewOrganizationMember(org.UID, member.UID, models.MemberRoleAdmin)
	membership.JoinedAt = &joined
	r.NoError(env.db.CreateOrganizationMember(env.ctx, membership))

	memberRefresh := "refresh-member-shared-org"
	memberToken := models.NewUserToken(member.UID, &org.UID, memberRefresh, models.TokenTypeRefresh)
	expires := joined.Add(24 * time.Hour)
	memberToken.ExpiresAt = &expires
	r.NoError(env.db.CreateUserToken(env.ctx, memberToken))

	// Positive control: the member's session works before the delete.
	r.Equal(http.StatusOK, env.refresh(t, memberRefresh).Code)

	rec, _ := env.deleteOrg(t, env.claimsFor(t, current.AccessToken), "shared-org", "shared-org")
	r.Equal(http.StatusOK, rec.Code)

	// The negative control: the other member is signed out for good.
	r.Equal(http.StatusUnauthorized, env.refresh(t, memberRefresh).Code,
		"another member's refresh token must be revoked with the organization")

	_, err = env.db.GetUserTokenByToken(env.ctx, memberRefresh)
	r.Error(err, "the revoked grant row must be gone, not merely rejected")
}

// TestMeDegradesOnAVanishedOrgInsteadOf401 is spec §3 (d): a tab still holding
// an access token that names the deleted org (it never saw the DELETE response)
// must reload into an org-less session, not be logged out. GetUserInfo used to
// return ErrOrganizationNotFound here, which handleUserInfoError maps to 401 —
// so a plain refresh destroyed a perfectly valid authentication.
func TestMeDegradesOnAVanishedOrgInsteadOf401(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDeletionEnv(t)
	current := env.newUserWithOrgs(t, "stale@example.com", "vanishing")

	// The stale token: minted before the delete, never replaced.
	staleClaims := env.claimsFor(t, current.AccessToken)
	r.Equal("vanishing", staleClaims.OrgSlug)

	// Control: it works while the org is alive.
	beforeRec, beforeBody := env.me(t, staleClaims)
	r.Equal(http.StatusOK, beforeRec.Code)
	r.NotNil(beforeBody["organization"])

	rec, _ := env.deleteOrg(t, env.claimsFor(t, current.AccessToken), "vanishing", "vanishing")
	r.Equal(http.StatusOK, rec.Code)

	afterRec, afterBody := env.me(t, staleClaims)
	r.Equal(http.StatusOK, afterRec.Code, "a vanished org slug must not 401 the session away")
	r.Nil(afterBody["organization"], "the session degrades to org-less")

	user, ok := afterBody["user"].(map[string]any)
	r.True(ok, "the user is still authenticated")
	r.Equal("stale@example.com", user["email"])
}

// TestDeleteOrgWithoutClaimsIsUnauthorized pins that the handler needs an
// authenticated caller to mint a session for — it must not fall back to
// deleting anonymously.
func TestDeleteOrgWithoutClaimsIsUnauthorized(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDeletionEnv(t)
	env.newUserWithOrgs(t, "guarded@example.com", "guarded")

	rec, _ := env.deleteOrg(t, nil, "guarded", "guarded")
	r.Equal(http.StatusUnauthorized, rec.Code)

	// The org is untouched.
	_, err := env.db.GetOrganizationBySlug(env.ctx, "guarded")
	r.NoError(err)
}
