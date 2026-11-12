package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupGitLabTestService(t *testing.T) (*GitLabOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		GitLab: config.GitLabOAuthConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
		},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{
			BaseURL: "http://localhost:4000",
		},
	}

	authService := NewService(dbService, cfg.Auth, cfg, nil, nil)
	svc := NewGitLabOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupGitLabTestOrg(ctx context.Context, t *testing.T, svc *GitLabOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestGitLabOAuthState(t *testing.T) {
	t.Parallel()

	svc, ctx := setupGitLabTestService(t)

	t.Run("generate and validate state", func(t *testing.T) {
		t.Parallel()

		nonce, err := svc.GenerateOAuthState(ctx, "/dashboard", "test-org")
		require.NoError(t, err)
		assert.NotEmpty(t, nonce)

		state, err := svc.ValidateOAuthState(ctx, nonce)
		require.NoError(t, err)
		assert.Equal(t, "/dashboard", state.RedirectURI)
		assert.Equal(t, "test-org", state.OrgSlug)
	})

	t.Run("state is one-time use", func(t *testing.T) {
		t.Parallel()

		nonce, err := svc.GenerateOAuthState(ctx, "/", "test-org")
		require.NoError(t, err)

		_, err = svc.ValidateOAuthState(ctx, nonce)
		require.NoError(t, err)

		// Second use should fail
		_, err = svc.ValidateOAuthState(ctx, nonce)
		assert.ErrorIs(t, err, ErrInvalidOAuthState)
	})

	t.Run("invalid state returns error", func(t *testing.T) {
		t.Parallel()

		_, err := svc.ValidateOAuthState(ctx, "invalid-nonce")
		assert.ErrorIs(t, err, ErrInvalidOAuthState)
	})
}

// gitLabConfirmedAt is a non-null GitLab confirmed_at value.
const gitLabConfirmedAt = "2026-01-02T03:04:05.000Z"

// fakeGitLab is an httptest GitLab instance serving the token exchange,
// /api/v4/user and /api/v4/user/emails. Profile and emails are raw JSON so a
// test can send null or omit a field.
type fakeGitLab struct {
	server     *httptest.Server
	profile    map[string]any
	emails     []map[string]any
	emailsHits int
	// emailsStatus, when non-zero, is answered by /api/v4/user/emails.
	emailsStatus int
}

func newFakeGitLab(t *testing.T, svc *GitLabOAuthService, profile map[string]any, emails []map[string]any) *fakeGitLab {
	t.Helper()

	fake := &fakeGitLab{profile: profile, emails: emails}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GitLabTokenResponse{
			AccessToken: "mock-access-token", TokenType: "Bearer", ExpiresIn: 7200,
			Scope: "read_user openid email",
		})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fake.profile)
	})
	mux.HandleFunc("/api/v4/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		fake.emailsHits++
		if fake.emailsStatus != 0 {
			w.WriteHeader(fake.emailsStatus)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fake.emails)
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	svc.cfg.GitLab.BaseURL = fake.server.URL
	svc.httpClient = fake.server.Client()

	return fake
}

// gitLabProfile builds a /api/v4/user body. confirmedAt nil sends null.
func gitLabProfile(id int, email string, confirmedAt any) map[string]any {
	return map[string]any{
		"id": id, "username": "gluser", "name": "GL User", "email": email,
		"avatar_url": "https://gitlab.example.com/avatar.png", "confirmed_at": confirmedAt,
	}
}

// confirmedEmails is a /api/v4/user/emails body listing email as confirmed.
func confirmedEmails(email string) []map[string]any {
	return []map[string]any{{"id": 1, "email": email, "confirmed_at": gitLabConfirmedAt}}
}

// gitLabCallbackRedirect drives the real Callback handler with a fresh state
// and returns the query of the redirect it answers.
func gitLabCallbackRedirect(t *testing.T, svc *GitLabOAuthService, orgSlug string) url.Values {
	t.Helper()

	state, err := svc.GenerateOAuthState(t.Context(), "/d/orgs/"+orgSlug, orgSlug)
	require.NoError(t, err)

	handler := NewGitLabOAuthHandler(svc, svc.cfg)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/gitlab/callback?code=mock-code&state="+url.QueryEscape(state), nil)
	require.NoError(t, handler.Callback(recorder, req))
	require.Equal(t, http.StatusFound, recorder.Code)

	location, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)

	return location.Query()
}

func requireNoGitLabLink(ctx context.Context, t *testing.T, svc *GitLabOAuthService, providerID string) {
	t.Helper()

	_, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitLab, providerID)
	require.ErrorIs(t, err, sql.ErrNoRows, "a refused GitLab sign-in must create no user_providers row")
}

// TestGitLabSignInRequiresVerifiedEmail covers GHSA-c7jr-53g7-65q8: GitLab
// sign-in used to trust /api/v4/user's email with no verification signal and
// link it to any existing account with that email.
func TestGitLabSignInRequiresVerifiedEmail(t *testing.T) {
	t.Parallel()

	t.Run("unconfirmed profile matching an existing user is refused", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		victim := newVerifiedUser("victim-gl@acme.com")
		require.NoError(t, svc.db.CreateUser(ctx, victim))

		fake := newFakeGitLab(t, svc, gitLabProfile(4001, victim.Email, nil), confirmedEmails(victim.Email))

		params := gitLabCallbackRedirect(t, svc, org.Slug)
		assert.Equal(t, OAuthCodeEmailNotVerified, params.Get("error"))
		assert.Empty(t, params.Get("code"), "a refused sign-in must hand out no session code")
		requireNoGitLabLink(ctx, t, svc, "4001")
		assert.Zero(t, fake.emailsHits, "an unconfirmed profile is refused before the emails list")
	})

	t.Run("profile without confirmed_at is refused", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		profile := gitLabProfile(4002, "absent-gl@acme.com", nil)
		delete(profile, "confirmed_at")
		newFakeGitLab(t, svc, profile, confirmedEmails("absent-gl@acme.com"))

		_, err := svc.HandleCallback(ctx, "mock-code", org.Slug)
		require.ErrorIs(t, err, ErrEmailNotVerified)
		requireNoGitLabLink(ctx, t, svc, "4002")
	})

	t.Run("confirmed profile matching an existing user is refused", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		victim := newVerifiedUser("taken-gl@acme.com")
		require.NoError(t, svc.db.CreateUser(ctx, victim))

		newFakeGitLab(t, svc, gitLabProfile(4003, victim.Email, gitLabConfirmedAt), confirmedEmails(victim.Email))

		params := gitLabCallbackRedirect(t, svc, org.Slug)
		assert.Equal(t, OAuthCodeAccountExistsSignInFirst, params.Get("error"))
		assert.Equal(t, OAuthDescAccountExistsSignInFirst, params.Get("error_description"))
		assert.Empty(t, params.Get("code"))
		requireNoGitLabLink(ctx, t, svc, "4003")
	})

	t.Run("primary email not a confirmed entry of user/emails is refused", func(t *testing.T) {
		t.Parallel()

		cases := map[string][]map[string]any{
			"unconfirmed entry":          {{"id": 1, "email": "primary-gl@acme.com", "confirmed_at": nil}},
			"entry without confirmed_at": {{"id": 1, "email": "primary-gl@acme.com"}},
			"primary not listed":         confirmedEmails("other-gl@acme.com"),
			"empty list":                 {},
		}

		for name, emails := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				svc, ctx := setupGitLabTestService(t)
				org := setupGitLabTestOrg(ctx, t, svc)
				newFakeGitLab(t, svc, gitLabProfile(4004, "primary-gl@acme.com", gitLabConfirmedAt), emails)

				params := gitLabCallbackRedirect(t, svc, org.Slug)
				assert.Equal(t, OAuthCodeEmailNotVerified, params.Get("error"))
				requireNoGitLabLink(ctx, t, svc, "4004")

				_, err := svc.db.GetUserByEmail(ctx, "primary-gl@acme.com")
				require.ErrorIs(t, err, sql.ErrNoRows, "no account may be created")
			})
		}
	})

	t.Run("emails endpoint failure fails closed", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		fake := newFakeGitLab(t, svc, gitLabProfile(4005, "down-gl@acme.com", gitLabConfirmedAt), nil)
		fake.emailsStatus = http.StatusForbidden

		_, err := svc.HandleCallback(ctx, "mock-code", org.Slug)
		require.ErrorIs(t, err, ErrGitLabAPI)
		requireNoGitLabLink(ctx, t, svc, "4005")
	})

	t.Run("unconfirmed profile consumes no invitation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		inviter := models.NewUser("inviter-gl@acme.com")
		require.NoError(t, svc.db.CreateUser(ctx, inviter))
		require.NoError(t, svc.db.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(org.UID, inviter.UID, models.MemberRoleAdmin)))

		_, err := svc.authService.CreateInvitation(ctx, org.Slug, inviter.UID, InviteRequest{
			Email: "invited-gl@acme.com", Role: string(models.MemberRoleUser), ExpiresIn: "24h", App: "dash0",
		})
		require.NoError(t, err)

		newFakeGitLab(t, svc, gitLabProfile(4006, "invited-gl@acme.com", nil), confirmedEmails("invited-gl@acme.com"))

		params := gitLabCallbackRedirect(t, svc, org.Slug)
		assert.Equal(t, OAuthCodeEmailNotVerified, params.Get("error"))

		entries, err := svc.db.ListStateEntries(ctx, &org.UID, inviteKeyPrefix)
		require.NoError(t, err)
		assert.Len(t, entries, 1, "the invitation must still be outstanding")

		_, err = svc.db.GetUserByEmail(ctx, "invited-gl@acme.com")
		require.ErrorIs(t, err, sql.ErrNoRows, "no account may be created")
	})

	t.Run("confirmed profile with no local user creates a verified account", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		newFakeGitLab(t, svc, gitLabProfile(4007, "new-gl@acme.com", gitLabConfirmedAt),
			[]map[string]any{
				{"id": 2, "email": "secondary-gl@acme.com", "confirmed_at": nil},
				{"id": 1, "email": "New-GL@acme.com", "confirmed_at": gitLabConfirmedAt},
			})

		result, err := svc.HandleCallback(ctx, "mock-code", org.Slug)
		require.NoError(t, err)
		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)

		user, err := svc.db.GetUserByEmail(ctx, "new-gl@acme.com")
		require.NoError(t, err)
		assert.NotNil(t, user.EmailVerifiedAt)
		assert.Equal(t, user.UID, result.UserUID)

		link, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitLab, "4007")
		require.NoError(t, err)
		assert.Equal(t, user.UID, link.UserUID)
	})

	t.Run("already linked identity keeps signing in", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		user := newVerifiedUser("linked-gl@acme.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))
		require.NoError(t, svc.db.CreateUserProvider(ctx,
			models.NewUserProvider(user.UID, models.ProviderTypeGitLab, "4008")))
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		newFakeGitLab(t, svc, gitLabProfile(4008, user.Email, gitLabConfirmedAt), confirmedEmails(user.Email))

		result, err := svc.HandleCallback(ctx, "mock-code", org.Slug)
		require.NoError(t, err)
		assert.NotEmpty(t, result.AccessToken)
		assert.Equal(t, user.UID, result.UserUID)
	})
}

func TestGitLabFindOrCreateUser(t *testing.T) {
	t.Parallel()

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)

		user := models.NewUser("existing-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))
		require.NoError(t, svc.db.CreateUserProvider(ctx,
			models.NewUserProvider(user.UID, models.ProviderTypeGitLab, "99999")))

		userInfo := &GitLabUserInfo{ID: 99999, Username: "existinguser", Email: "existing-gl@example.com"}

		foundUser, created, err := svc.findOrCreateUser(ctx, userInfo, true)
		require.NoError(t, err)
		require.False(t, created, "an existing account must not be reported as newly created")
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email match without a link is refused", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)

		user := models.NewUser("match-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		userInfo := &GitLabUserInfo{ID: 55555, Username: "matchuser", Email: "match-gl@example.com"}

		_, _, err := svc.findOrCreateUser(ctx, userInfo, true)
		require.ErrorIs(t, err, ErrGitLabLinkRequiresSignIn)
		requireNoGitLabLink(ctx, t, svc, "55555")
	})

	t.Run("unverified email creates nothing", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)

		userInfo := &GitLabUserInfo{ID: 66666, Username: "unverified", Email: "unverified-gl@example.com"}

		_, _, err := svc.findOrCreateUser(ctx, userInfo, false)
		require.ErrorIs(t, err, ErrEmailNotVerified)
		requireNoGitLabLink(ctx, t, svc, "66666")
	})

	t.Run("new user creation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)

		userInfo := &GitLabUserInfo{
			ID:        77777,
			Username:  "brandnewuser",
			Email:     "brand-new-gl@example.com",
			Name:      "Brand New GL User",
			AvatarURL: "https://gitlab.com/uploads/-/system/user/avatar/77777/avatar.png",
		}

		user, created, err := svc.findOrCreateUser(ctx, userInfo, true)
		require.NoError(t, err)
		require.True(t, created, "a brand-new account must be reported as newly created")
		assert.Equal(t, "brand-new-gl@example.com", user.Email)
		assert.Equal(t, "Brand New GL User", user.Name)
		assert.Equal(t, "https://gitlab.com/uploads/-/system/user/avatar/77777/avatar.png", user.AvatarURL)
		assert.NotNil(t, user.EmailVerifiedAt)
	})

	t.Run("ensure membership first user gets owner", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		user := models.NewUser("first-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		member, pending, err := svc.authService.JoinOrgViaLogin(ctx, org, user)
		require.NoError(t, err)
		require.False(t, pending)
		require.NotNil(t, member)
		// First member of an empty org owns it (spec 2026-08-08-11).
		assert.Equal(t, models.MemberRoleOwner, member.Role)
	})

	t.Run("ensure membership second user gets user role", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		firstUser := models.NewUser("first-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, firstUser))

		_, _, err := svc.authService.JoinOrgViaLogin(ctx, org, firstUser)
		require.NoError(t, err)

		// The org admits @example.com, so the second user joins as a
		// plain user (without the pattern they would be left pending —
		// see TestJoinOrgViaLogin).
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@example\.com$`, false))

		secondUser := newVerifiedUser("second-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, secondUser))

		member, pending, err := svc.authService.JoinOrgViaLogin(ctx, org, secondUser)
		require.NoError(t, err)
		require.False(t, pending)
		require.NotNil(t, member)
		assert.Equal(t, models.MemberRoleUser, member.Role)
	})

	t.Run("no email returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)
		newFakeGitLab(t, svc, gitLabProfile(11111, "", gitLabConfirmedAt), nil)

		_, err := svc.HandleCallback(ctx, "mock-code", org.Slug)
		assert.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

func TestGitLabGetCallbackURL(t *testing.T) {
	t.Parallel()

	svc, _ := setupGitLabTestService(t)

	assert.Equal(t, "http://localhost:4000/api/v1/auth/gitlab/callback", svc.getCallbackURL())
}

func TestGitLabGetBaseURL(t *testing.T) {
	t.Parallel()

	t.Run("defaults to gitlab.com", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupGitLabTestService(t)
		assert.Equal(t, "https://gitlab.com", svc.getGitLabBaseURL())
	})

	t.Run("uses configured base URL", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupGitLabTestService(t)
		svc.cfg.GitLab.BaseURL = "https://gitlab.example.com"
		assert.Equal(t, "https://gitlab.example.com", svc.getGitLabBaseURL())
	})

	t.Run("trims trailing slash", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupGitLabTestService(t)
		svc.cfg.GitLab.BaseURL = "https://gitlab.example.com/"
		assert.Equal(t, "https://gitlab.example.com", svc.getGitLabBaseURL())
	})
}
