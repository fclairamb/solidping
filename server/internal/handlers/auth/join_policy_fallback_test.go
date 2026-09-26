package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
)

// Spec 2026-09-25-15: a federated login refused by the org it was started from
// (org A) must not hand a real member of org B an org-less session they cannot
// use. The identity is proven, so the answer is a normal session on B, while
// the membership request on A is still opened and still named.

// fallbackMember makes user an admitted member of a fresh org named slug and
// returns the org. joinedAt orders memberships (ListMembersByUser is
// created_at DESC).
func fallbackMember(
	ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User, slug string, joinedAt time.Time,
) *models.Organization {
	t.Helper()

	org := joinTestOrg(ctx, t, dbSvc, slug, true)
	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
	member.CreatedAt = joinedAt
	member.JoinedAt = &joinedAt
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	return org
}

// fallbackRefreshToken stores a refresh-token row for user on org, created at
// createdAt — what a past sign-in on that org leaves behind.
func fallbackRefreshToken(
	ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User, org *models.Organization,
	createdAt time.Time,
) {
	t.Helper()

	token := models.NewUserToken(user.UID, &org.UID, "rt-"+org.Slug+"-"+user.UID, models.TokenTypeRefresh)
	token.CreatedAt = createdAt
	expires := time.Now().Add(time.Hour)
	token.ExpiresAt = &expires
	require.NoError(t, dbSvc.CreateUserToken(ctx, token))
}

// TestCompleteOrgLoginRefusedMemberLandsOnOwnOrg is the case the spec exists
// for: refused on A, member of B → a full session on B (refresh token, the
// connector's method recorded, the login audited on B), with A still named.
func TestCompleteOrgLoginRefusedMemberLandsOnOwnOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)

	refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
	user := joinTestUser(ctx, t, dbSvc, "alice@elsewhere.example")
	own := fallbackMember(ctx, t, dbSvc, user, "acmetech", time.Now().Add(-time.Hour))

	result, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodGoogle))
	r.NoError(err)

	r.True(result.Pending, "org A still did not admit the user")
	r.Equal("demo", result.PendingOrgSlug, "the org with the open request is still named")
	r.Equal("acmetech", result.FallbackOrgSlug)
	r.NotEmpty(result.RefreshToken, "a session on an org the user belongs to is a full one")

	// The access token is scoped to B, with the user's role there.
	claims, err := svc.ValidateToken(ctx, result.AccessToken)
	r.NoError(err)
	r.Equal("acmetech", claims.OrgSlug)
	r.Equal(string(models.MemberRoleUser), claims.Role)
	r.NotEmpty(claims.RefreshUID)

	// The refresh-token row lives on B and records the connector.
	tokens, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Len(tokens, 1)
	r.NotNil(tokens[0].OrganizationUID)
	r.Equal(own.UID, *tokens[0].OrganizationUID)
	r.Equal(claims.RefreshUID, tokens[0].UID)
	createdWith, ok := tokens[0].Properties[keyCreatedWith].(map[string]any)
	r.True(ok, "created_with must be recorded on the session row")
	r.Equal(signupMethodGoogle, createdWith[keyMethod])

	// The login is audited on B, never on A.
	ownEvents, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: own.UID, EventTypePrefixes: []string{"auth"}, Limit: 10,
	})
	r.NoError(err)
	r.Len(ownEvents, 1)
	r.Equal(signupMethodGoogle, ownEvents[0].Payload["auth_method"])

	refusingEvents, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: refusing.UID, EventTypePrefixes: []string{"auth"}, Limit: 10,
	})
	r.NoError(err)
	r.Empty(refusingEvents, "no session was minted on the org that refused the login")

	// Nothing was granted on A: no membership, and the request is open.
	_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, refusing.UID)
	r.Error(memberErr, "a refused login must leave no organization_members row on A")

	request, err := dbSvc.GetMembershipRequestByOrgAndUser(ctx, refusing.UID, user.UID)
	r.NoError(err)
	r.Equal(models.MembershipRequestStatusPending, request.Status)
}

// TestCompleteOrgLoginRefusedNonMemberStaysOrgLess is the positive control: a
// user with no membership anywhere keeps today's org-less session.
func TestCompleteOrgLoginRefusedNonMemberStaysOrgLess(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)

	refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
	user := joinTestUser(ctx, t, dbSvc, "outsider@elsewhere.example")

	result, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodGoogle))
	r.NoError(err)

	r.True(result.Pending)
	r.Equal("demo", result.PendingOrgSlug)
	r.Empty(result.FallbackOrgSlug)
	r.Empty(result.RefreshToken, "an org-less session has no refresh token")

	claims, err := svc.ValidateToken(ctx, result.AccessToken)
	r.NoError(err)
	r.Empty(claims.OrgSlug)

	tokens, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Empty(tokens)
}

// TestFallbackMemberOrgChoice pins which org B is when there are several:
// the org of the most recent refresh token while still a member, else the most
// recently joined membership; a stale token for an org the user left and a
// membership in a deleted org never win.
func TestFallbackMemberOrgChoice(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name  string
		setup func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User)
		want  string
	}{
		{
			name: "no refresh token: the most recently joined membership",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User) {
				t.Helper()
				fallbackMember(ctx, t, dbSvc, user, "older", now.Add(-2*time.Hour))
				fallbackMember(ctx, t, dbSvc, user, "newer", now.Add(-time.Hour))
			},
			want: "newer",
		},
		{
			name: "the most recent refresh token's org wins over join order",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User) {
				t.Helper()
				older := fallbackMember(ctx, t, dbSvc, user, "older", now.Add(-2*time.Hour))
				newer := fallbackMember(ctx, t, dbSvc, user, "newer", now.Add(-time.Hour))
				fallbackRefreshToken(ctx, t, dbSvc, user, newer, now.Add(-30*time.Minute))
				fallbackRefreshToken(ctx, t, dbSvc, user, older, now.Add(-10*time.Minute))
			},
			want: "older",
		},
		{
			name: "a stale refresh token for an org the user left is ignored",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User) {
				t.Helper()
				fallbackMember(ctx, t, dbSvc, user, "kept", now.Add(-2*time.Hour))
				left := fallbackMember(ctx, t, dbSvc, user, "left", now.Add(-time.Hour))
				fallbackRefreshToken(ctx, t, dbSvc, user, left, now.Add(-10*time.Minute))

				membership, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, left.UID)
				require.NoError(t, err)
				require.NoError(t, dbSvc.DeleteOrganizationMember(ctx, membership.UID))
			},
			want: "kept",
		},
		{
			name: "a membership in a deleted org is skipped",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User) {
				t.Helper()
				fallbackMember(ctx, t, dbSvc, user, "alive", now.Add(-2*time.Hour))
				gone := fallbackMember(ctx, t, dbSvc, user, "gone", now.Add(-time.Hour))
				require.NoError(t, dbSvc.DeleteOrganization(ctx, gone.UID))
			},
			want: "alive",
		},
		{
			name: "no usable membership at all",
			setup: func(ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User) {
				t.Helper()
				gone := fallbackMember(ctx, t, dbSvc, user, "gone", now.Add(-time.Hour))
				require.NoError(t, dbSvc.DeleteOrganization(ctx, gone.UID))
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, dbSvc, ctx := setupAuthTestService(t)

			refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
			user := joinTestUser(ctx, t, dbSvc, "multi@elsewhere.example")
			tt.setup(ctx, t, dbSvc, user)

			result, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodOIDC))
			r.NoError(err)
			r.True(result.Pending)
			r.Equal(tt.want, result.FallbackOrgSlug)

			claims, err := svc.ValidateToken(ctx, result.AccessToken)
			r.NoError(err)
			r.Equal(tt.want, claims.OrgSlug, "the access token is scoped to the fallback org, or none")

			if tt.want == "" {
				r.Empty(result.RefreshToken)
			} else {
				r.NotEmpty(result.RefreshToken)
			}
		})
	}
}

// TestCompleteOrgLoginFallbackCountsAutoJoin: the cross-org auto-join runs
// before the fallback, so an org this very login just joined is where the
// session lands.
func TestCompleteOrgLoginFallbackCountsAutoJoin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)

	refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
	autoJoin := joinTestOrg(ctx, t, dbSvc, "acmeauto", true)
	r.NoError(dbSvc.SetOrgParameter(ctx, autoJoin.UID, registrationEmailPatternKey, `@acme\.com$`, false))

	user := joinTestUser(ctx, t, dbSvc, "bob@acme.com")

	result, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodGoogle))
	r.NoError(err)
	r.True(result.Pending)

	_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, autoJoin.UID)
	r.NoError(memberErr, "the auto-join must have admitted the user to acmeauto")
	r.Equal("acmeauto", result.FallbackOrgSlug)
	r.NotEmpty(result.RefreshToken)
}

// TestPendingFallbackHandoff: the pending-with-fallback outcome goes through
// the same handoff route, still flagged membershipPending=A, and the code
// redeems for a session on B that the exchange reports as organization=B.
func TestPendingFallbackHandoff(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)

	refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
	user := joinTestUser(ctx, t, dbSvc, "alice@elsewhere.example")
	fallbackMember(ctx, t, dbSvc, user, "acmetech", time.Now().Add(-time.Hour))

	login, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodOIDC))
	r.NoError(err)
	r.Equal("acmetech", login.FallbackOrgSlug)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/oidc/callback", nil)

	r.NoError(finishProviderCallback(rec, req, dbSvc, "oidc", "/d/orgs/demo", &ProviderOutcome{
		AccessToken:     login.AccessToken,
		RefreshToken:    login.RefreshToken,
		ExpiresIn:       login.ExpiresIn,
		OrgSlug:         refusing.Slug,
		UserUID:         user.UID,
		Pending:         login.Pending,
		FallbackOrgSlug: login.FallbackOrgSlug,
		PendingOrgSlug:  login.PendingOrgSlug,
	}))

	r.Equal(http.StatusFound, rec.Code)

	rawLocation := rec.Header().Get("Location")
	location, err := url.Parse(rawLocation)
	r.NoError(err)
	r.Equal(handoffCompletePath, location.Path)
	r.Equal("demo", location.Query().Get(pendingMembershipParam))
	r.NotContains(rawLocation, login.AccessToken)
	r.NotContains(rawLocation, login.RefreshToken)

	code := location.Query().Get(handoffCodeParam)
	r.NotEmpty(code)

	exchange := postExchange(t, svc, codeBody(t, code))
	r.Equal(http.StatusOK, exchange.Code, exchange.Body.String())

	var resp HandoffExchangeResponse
	r.NoError(json.Unmarshal(exchange.Body.Bytes(), &resp))

	r.Equal(login.AccessToken, resp.AccessToken)
	r.Equal(login.RefreshToken, resp.RefreshToken)
	r.NotNil(resp.Organization)
	r.Equal("acmetech", resp.Organization.Slug)
	r.Equal(LoginActionDefault, resp.LoginAction)
	r.Equal("demo", resp.MembershipPending)
	r.Equal("/d/orgs/demo", resp.ReturnTo, "returnTo is only a hint; the dashboard's same-org guard drops it")
}

// TestPendingFallbackHandoffSessionShape pins handoffSession for the three
// outcomes: admitted (scoped to the login's org), pending with a fallback
// (scoped to the fallback, still naming the pending org), pending without one
// (org-less).
func TestPendingFallbackHandoffSessionShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	admitted := (&ProviderOutcome{OrgSlug: "demo", UserUID: "u"}).handoffSession("/d")
	r.Equal("demo", admitted.OrgSlug)
	r.Empty(admitted.MembershipPending)

	fallback := (&ProviderOutcome{
		OrgSlug: "demo", UserUID: "u", Pending: true, FallbackOrgSlug: "acmetech", PendingOrgSlug: "demo",
	}).handoffSession("/d")
	r.Equal("acmetech", fallback.OrgSlug)
	r.Equal("demo", fallback.MembershipPending)

	orgLess := (&ProviderOutcome{
		OrgSlug: "demo", UserUID: "u", Pending: true, PendingOrgSlug: "demo",
	}).handoffSession("/d")
	r.Empty(orgLess.OrgSlug)
	r.Equal("demo", orgLess.MembershipPending)
}

// errFallbackForced is what the forced-failure wrappers below answer with.
var errFallbackForced = errors.New("forced fallback failure")

// failingMembershipListDB forces the fallback's membership lookup to fail.
type failingMembershipListDB struct {
	db.Service
}

func (f *failingMembershipListDB) ListMembersByUser(
	_ context.Context, _ string,
) ([]*models.OrganizationMember, error) {
	return nil, fmt.Errorf("list members: %w", errFallbackForced)
}

// failingSessionStoreDB forces the fallback's session minting to fail: the
// refresh-token row cannot be stored.
type failingSessionStoreDB struct {
	db.Service
}

func (f *failingSessionStoreDB) CreateUserToken(_ context.Context, _ *models.UserToken) error {
	return fmt.Errorf("create user token: %w", errFallbackForced)
}

// TestCompleteOrgLoginFallbackFailureDegradesToOrgLess pins the "never fails
// the login" half of plan item 2: when the fallback cannot be resolved or
// minted, the refused login still succeeds with today's org-less session,
// still names the refusing org, and its membership request is still opened.
func TestCompleteOrgLoginFallbackFailureDegradesToOrgLess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		wrap func(db.Service) db.Service
	}{
		{
			name: "membership lookup fails",
			wrap: func(inner db.Service) db.Service { return &failingMembershipListDB{Service: inner} },
		},
		{
			name: "minting the fallback session fails",
			wrap: func(inner db.Service) db.Service { return &failingSessionStoreDB{Service: inner} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, dbSvc, ctx := setupAuthTestService(t)

			refusing := joinTestOrg(ctx, t, dbSvc, "demo", true)
			user := joinTestUser(ctx, t, dbSvc, "alice@elsewhere.example")
			fallbackMember(ctx, t, dbSvc, user, "acmetech", time.Now().Add(-time.Hour))

			// Only now: the fixtures above must be written for real.
			svc.db = tt.wrap(dbSvc)

			result, err := svc.CompleteOrgLogin(ctx, refusing, user, WithLoginMethod(signupMethodGoogle))
			r.NoError(err, "a fallback failure must never fail the login")

			r.True(result.Pending)
			r.Empty(result.FallbackOrgSlug)
			r.Empty(result.RefreshToken, "the degraded session is the org-less one")
			r.Equal("demo", result.PendingOrgSlug, "the refusing org is still named")

			claims, err := svc.ValidateToken(ctx, result.AccessToken)
			r.NoError(err)
			r.Empty(claims.OrgSlug)

			request, err := dbSvc.GetMembershipRequestByOrgAndUser(ctx, refusing.UID, user.UID)
			r.NoError(err)
			r.Equal(models.MembershipRequestStatusPending, request.Status)

			tokens, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
			r.NoError(err)
			r.Empty(tokens, "no half-minted session may be left behind")
		})
	}
}

// TestCompleteOrgLoginSuppressedRequestWithFallback: rule 6's SaaS carve-out
// (a brand-new account that only met the platform default org) opens no join
// request and names no org, yet a user who does belong to another org (here,
// through the cross-org auto-join) still lands there with a full session, and
// the handoff redirect carries no membershipPending flag.
func TestCompleteOrgLoginSuppressedRequestWithFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.fullCfg.Deployment.Mode = config.DeploymentModeSaaS

	defaultOrg := joinTestOrg(ctx, t, dbSvc, defaults.Organization, true)
	autoJoin := joinTestOrg(ctx, t, dbSvc, "acmeauto", true)
	r.NoError(dbSvc.SetOrgParameter(ctx, autoJoin.UID, registrationEmailPatternKey, `@acme\.com$`, false))

	user := joinTestUser(ctx, t, dbSvc, "newcomer@acme.com")

	login, err := svc.CompleteOrgLogin(ctx, defaultOrg, user,
		WithLoginMethod(signupMethodGoogle), WithNewlyCreatedUser())
	r.NoError(err)

	r.True(login.Pending)
	r.Empty(login.PendingOrgSlug, "rule 6 suppressed the request, so no org may be named")
	r.Equal("acmeauto", login.FallbackOrgSlug)
	r.NotEmpty(login.RefreshToken)

	claims, err := svc.ValidateToken(ctx, login.AccessToken)
	r.NoError(err)
	r.Equal("acmeauto", claims.OrgSlug)

	request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, defaultOrg.UID, user.UID)
	r.True(reqErr != nil || request == nil, "no join request may be queued against the default org")

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/google/callback", nil)

	r.NoError(finishProviderCallback(rec, req, dbSvc, "google", "/d/orgs/default", &ProviderOutcome{
		AccessToken:     login.AccessToken,
		RefreshToken:    login.RefreshToken,
		ExpiresIn:       login.ExpiresIn,
		OrgSlug:         defaultOrg.Slug,
		UserUID:         user.UID,
		Pending:         login.Pending,
		FallbackOrgSlug: login.FallbackOrgSlug,
		PendingOrgSlug:  login.PendingOrgSlug,
	}))

	r.Equal(http.StatusFound, rec.Code)

	location, err := url.Parse(rec.Header().Get("Location"))
	r.NoError(err)
	r.Equal(handoffCompletePath, location.Path)
	r.False(location.Query().Has(pendingMembershipParam), "nothing to name, so no flag")

	exchange := postExchange(t, svc, codeBody(t, location.Query().Get(handoffCodeParam)))
	r.Equal(http.StatusOK, exchange.Code, exchange.Body.String())

	var resp HandoffExchangeResponse
	r.NoError(json.Unmarshal(exchange.Body.Bytes(), &resp))
	r.NotNil(resp.Organization)
	r.Equal("acmeauto", resp.Organization.Slug)
	r.Empty(resp.MembershipPending)
}
