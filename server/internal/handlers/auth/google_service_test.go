package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func setupGoogleTestService(t *testing.T) (*GoogleOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		Google: config.GoogleOAuthConfig{
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
	svc := NewGoogleOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupTestOrg(ctx context.Context, t *testing.T, svc *GoogleOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestGoogleOAuthState(t *testing.T) {
	t.Parallel()

	svc, ctx := setupGoogleTestService(t)

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

func TestGoogleHandleCallback(t *testing.T) {
	t.Parallel()

	t.Run("full flow creates user and returns tokens", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)
		org := setupTestOrg(ctx, t, svc)

		// Mock Google token and userinfo endpoints
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GoogleTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GoogleUserInfo{
				Sub:           "google-user-123",
				Email:         "test@example.com",
				EmailVerified: true,
				Name:          "Test User",
				Picture:       "https://example.com/photo.jpg",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		// Override URLs for testing by using a custom service with patched client
		svc.httpClient = tokenServer.Client()

		// We need to test the individual methods since HandleCallback calls real URLs
		// Test exchangeCode with mocked server
		result := testGoogleCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userInfoServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)
		org := setupTestOrg(ctx, t, svc)

		// Pre-create user and provider link
		user := models.NewUser("existing@example.com")
		user.Name = "Existing User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeGoogle, "google-existing-123")
		require.NoError(t, svc.db.CreateUserProvider(ctx, provider))

		// Create membership
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		// Test findOrCreateUser returns existing user
		userInfo := &GoogleUserInfo{
			Sub:           "google-existing-123",
			Email:         "existing@example.com",
			EmailVerified: true,
			Name:          "Existing User",
		}

		foundUser, created, err := svc.findOrCreateUser(ctx, userInfo)
		require.False(t, created, "an existing account must not be reported as newly created")
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email-based user matching", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)

		// Pre-create user (e.g., from password login)
		user := models.NewUser("match@example.com")
		user.Name = "Match User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		// Google login with same email but new provider ID
		userInfo := &GoogleUserInfo{
			Sub:           "google-new-456",
			Email:         "match@example.com",
			EmailVerified: true,
			Name:          "Match User",
		}

		foundUser, created, err := svc.findOrCreateUser(ctx, userInfo)
		require.False(t, created, "an existing account must not be reported as newly created")
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)

		// Verify provider was linked
		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGoogle, "google-new-456")
		require.NoError(t, err)
		assert.Equal(t, user.UID, linked.UserUID)
	})

	t.Run("new user creation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)

		userInfo := &GoogleUserInfo{
			Sub:           "google-brand-new-789",
			Email:         "brand-new@example.com",
			EmailVerified: true,
			Name:          "Brand New User",
			Picture:       "https://example.com/new.jpg",
		}

		user, created, err := svc.findOrCreateUser(ctx, userInfo)
		require.True(t, created, "a brand-new account must be reported as newly created")
		require.NoError(t, err)
		assert.Equal(t, "brand-new@example.com", user.Email)
		assert.Equal(t, "Brand New User", user.Name)
		assert.Equal(t, "https://example.com/new.jpg", user.AvatarURL)
		assert.NotNil(t, user.EmailVerifiedAt)
	})

	t.Run("ensure membership first user gets owner", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)
		org := setupTestOrg(ctx, t, svc)

		user := models.NewUser("first@example.com")
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

		svc, ctx := setupGoogleTestService(t)
		org := setupTestOrg(ctx, t, svc)

		// Create first user as admin
		firstUser := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, firstUser))

		_, _, err := svc.authService.JoinOrgViaLogin(ctx, org, firstUser)
		require.NoError(t, err)

		// The org admits @example.com, so the second user joins as a
		// plain user (without the pattern they would be left pending —
		// see TestJoinOrgViaLogin).
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@example\.com$`, false))

		secondUser := models.NewUser("second@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, secondUser))

		member, pending, err := svc.authService.JoinOrgViaLogin(ctx, org, secondUser)
		require.NoError(t, err)
		require.False(t, pending)
		require.NotNil(t, member)
		assert.Equal(t, models.MemberRoleUser, member.Role)
	})

	t.Run("email not verified returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGoogleTestService(t)
		org := setupTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GoogleTokenResponse{AccessToken: "mock-token", TokenType: "Bearer", ExpiresIn: 3600}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GoogleUserInfo{
				Sub:           "google-unverified",
				Email:         "unverified@example.com",
				EmailVerified: false,
				Name:          "Unverified User",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userInfoServer.Close()

		_, err := testGoogleCallbackWithMockServersErr(ctx, t, svc, org.Slug, tokenServer.URL, userInfoServer.URL)
		assert.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

func TestGoogleGetCallbackURL(t *testing.T) {
	t.Parallel()

	svc, _ := setupGoogleTestService(t)

	assert.Equal(t, "http://localhost:4000/api/v1/auth/google/callback", svc.getCallbackURL())
}

// testGoogleCallbackWithMockServers drives the REAL HandleCallback against
// httptest stand-ins for Google's token and userinfo endpoints. It must stay a
// thin URL-injection shim: a test-local re-implementation of the callback body
// would keep passing if someone reintroduced a direct membership creation in
// the production path, which is exactly the regression the join-policy gate
// exists to prevent.
func testGoogleCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *GoogleOAuthService,
	orgSlug, tokenURL, userInfoURL string,
) *GoogleOAuthResult {
	t.Helper()

	result, err := testGoogleCallbackWithMockServersErr(ctx, t, svc, orgSlug, tokenURL, userInfoURL)
	require.NoError(t, err)

	return result
}

func testGoogleCallbackWithMockServersErr(
	ctx context.Context,
	t *testing.T,
	svc *GoogleOAuthService,
	orgSlug, tokenURL, userInfoURL string,
) (*GoogleOAuthResult, error) {
	t.Helper()

	svc.tokenURL = tokenURL
	svc.userInfoURL = userInfoURL

	return svc.HandleCallback(ctx, "mock-code", orgSlug)
}
