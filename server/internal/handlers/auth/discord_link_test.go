package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discordlink"
)

// linkEnv is an already-signed-in member and the org whose account page starts a
// link round trip.
type linkEnv struct {
	db      db.Service
	svc     *DiscordOAuthService
	handler *DiscordOAuthHandler
	org     *models.Organization
	user    *models.User
	fixture string
}

func setupLinkEnv(t *testing.T) *linkEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := newSQLiteDBService(t)
	svc := newDiscordTestService(t, dbSvc)
	fixture := nextFixture()

	org := models.NewOrganization("acme-"+fixture, "ACME")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("member" + fixture + "@acme.example")
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	return &linkEnv{
		db:      dbSvc,
		svc:     svc,
		handler: NewDiscordOAuthHandler(svc, svc.cfg),
		org:     org,
		user:    user,
		fixture: fixture,
	}
}

// callback drives the REAL /auth/discord/callback handler.
func (e *linkEnv) callback(t *testing.T, state string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/discord/callback?code=the-code&state="+url.QueryEscape(state), nil)
	rec := httptest.NewRecorder()

	require.NoError(t, e.handler.Callback(rec, req))

	return rec
}

// TestDiscordLinkModeMintsNoSessionAndNoOrg is the negative this whole separation
// exists for.
//
// The LOGIN path resolves an organization from the caller's guild list and mints
// a session. Link mode must do NEITHER: it is called by somebody who is already
// signed in, and the only thing it is allowed to write is one `user_providers`
// row. A link button that quietly created an org and signed the caller into it
// would be the 2026-08-24 capture's shape of bug on a new path.
//
// Asserted three ways, because any one of them alone could pass by accident: no
// new organization row, no `organization_providers` row for the guild the profile
// would have implied, and no session cookie or token anywhere in the response.
func TestDiscordLinkModeMintsNoSessionAndNoOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupLinkEnv(t)
	ctx := t.Context()

	userInfo := discordTestUser(env.fixture)
	// A guild the member belongs to, mapping to NO organization. The login path
	// would create one from it; link mode must not even look.
	fakeDiscordEndpoints(t, env.svc, userInfo,
		[]DiscordGuild{{ID: "g-unmapped-" + env.fixture, Name: "Unmapped Guild"}})

	orgsBefore, err := env.db.ListOrganizations(ctx)
	r.NoError(err)

	token, err := discordlink.Mint(ctx, env.db, discordlink.Payload{
		UserUID:     env.user.UID,
		OrgUID:      env.org.UID,
		RedirectURI: "/d/orgs/" + env.org.Slug + "/account/notifications",
	})
	r.NoError(err)

	rec := env.callback(t, discordlink.StatePrefix+token)

	// It succeeded, as a redirect carrying the success marker.
	r.Equal(http.StatusFound, rec.Code)

	location, err := url.Parse(rec.Header().Get("Location"))
	r.NoError(err)
	r.Equal("1", location.Query().Get(discordLinkedParam))
	r.Empty(location.Query().Get("error"), "a successful link carries no error")

	// NEGATIVE 1: no session. Not in the query, not in a cookie.
	r.Empty(location.Query().Get("access_token"), "link mode must mint no access token")
	r.Empty(location.Query().Get("refresh_token"), "link mode must mint no refresh token")
	r.Empty(rec.Result().Cookies(), "link mode must set no session cookie")

	// NEGATIVE 2: no organization was created.
	orgsAfter, err := env.db.ListOrganizations(ctx)
	r.NoError(err)
	r.Len(orgsAfter, len(orgsBefore), "link mode must create no organization")

	// NEGATIVE 3: the guild was not mapped to anything.
	providers, err := env.db.ListOrganizationProviders(ctx, env.org.UID)
	r.NoError(err)

	for _, provider := range providers {
		r.NotEqual("g-unmapped-"+env.fixture, provider.ProviderID,
			"link mode must not map a guild to an org")
	}

	// POSITIVE CONTROL: the one thing it IS allowed to write did happen.
	bound, err := env.db.GetUserProviderByProviderID(ctx, models.ProviderTypeDiscord, userInfo.ID)
	r.NoError(err)
	r.NotNil(bound)
	r.Equal(env.user.UID, bound.UserUID)
}

// TestDiscordLinkModeRejectsSpentAndUnknownState: the token is single use, and an
// unknown one gets the same polite failure as an expired one so the public
// callback cannot be used to probe for live tokens.
func TestDiscordLinkModeRejectsSpentAndUnknownState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupLinkEnv(t)
	ctx := t.Context()

	fakeDiscordEndpoints(t, env.svc, discordTestUser(env.fixture), nil)

	token, err := discordlink.Mint(ctx, env.db, discordlink.Payload{
		UserUID: env.user.UID,
		OrgUID:  env.org.UID,
	})
	r.NoError(err)

	// First use succeeds.
	first := env.callback(t, discordlink.StatePrefix+token)
	r.Equal(http.StatusFound, first.Code)
	r.Empty(firstQuery(t, first).Get("error"))

	// Replay is refused.
	second := env.callback(t, discordlink.StatePrefix+token)
	r.Equal(discordLinkErrorCode, firstQuery(t, second).Get("error"))

	// A token that never existed is refused identically.
	unknown := env.callback(t, discordlink.StatePrefix+"never-minted")
	r.Equal(discordLinkErrorCode, firstQuery(t, unknown).Get("error"))
	r.Equal(ErrDiscordLinkStateInvalid.Error(),
		firstQuery(t, unknown).Get("error_description"),
		"an unknown token must be indistinguishable from an expired one")
}

// TestDiscordLinkModeRefusesAccountClaimedByAnotherUser: two SolidPing accounts
// cannot share one snowflake. Overwriting the first binding would move another
// member's Discord paging route onto this account without telling either of them.
func TestDiscordLinkModeRefusesAccountClaimedByAnotherUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupLinkEnv(t)
	ctx := t.Context()

	userInfo := discordTestUser(env.fixture)
	fakeDiscordEndpoints(t, env.svc, userInfo, nil)

	// Somebody else already owns this Discord account.
	other := models.NewUser("other" + env.fixture + "@acme.example")
	r.NoError(env.db.CreateUser(ctx, other))
	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(other.UID, models.ProviderTypeDiscord, userInfo.ID)))

	token, err := discordlink.Mint(ctx, env.db, discordlink.Payload{
		UserUID: env.user.UID,
		OrgUID:  env.org.UID,
	})
	r.NoError(err)

	rec := env.callback(t, discordlink.StatePrefix+token)
	query := firstQuery(t, rec)
	r.Equal(discordLinkErrorCode, query.Get("error"))
	r.Equal(ErrDiscordLinkClaimedByOther.Error(), query.Get("error_description"))

	// The first binding is untouched.
	bound, err := env.db.GetUserProviderByProviderID(ctx, models.ProviderTypeDiscord, userInfo.ID)
	r.NoError(err)
	r.Equal(other.UID, bound.UserUID)
}

// TestDiscordLinkModeIsIdempotent: re-linking the account already bound to this
// very user is a no-op success, not an error the account page has to explain.
func TestDiscordLinkModeIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupLinkEnv(t)
	ctx := t.Context()

	userInfo := discordTestUser(env.fixture)
	r.NoError(env.db.CreateUserProvider(ctx,
		models.NewUserProvider(env.user.UID, models.ProviderTypeDiscord, userInfo.ID)))

	r.NoError(env.svc.LinkDiscordProvider(ctx, env.user.UID, userInfo.ID))

	providers, err := env.db.ListUserProvidersByUser(ctx, env.user.UID)
	r.NoError(err)

	discordRows := 0

	for _, provider := range providers {
		if provider.ProviderType == models.ProviderTypeDiscord {
			discordRows++
		}
	}

	r.Equal(1, discordRows, "re-linking must not duplicate the row")
}

// TestIsDiscordLinkStateOnlyMatchesTheMarker: a login state must never be routed
// into link mode, and vice versa.
func TestIsDiscordLinkStateOnlyMatchesTheMarker(t *testing.T) {
	t.Parallel()

	require.True(t, isDiscordLinkState(discordlink.StatePrefix+"abc"))
	require.False(t, isDiscordLinkState("abc"))
	require.False(t, isDiscordLinkState(""))
}

func firstQuery(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()

	parsed, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	return parsed.Query()
}
