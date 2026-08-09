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

func setupMicrosoftTestService(t *testing.T) (*MicrosoftOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		Microsoft: config.MicrosoftOAuthConfig{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			TenantID:     "test-tenant-id",
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
	svc := NewMicrosoftOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupMicrosoftTestOrg(ctx context.Context, t *testing.T, svc *MicrosoftOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestMicrosoftOAuthState(t *testing.T) {
	t.Parallel()

	svc, ctx := setupMicrosoftTestService(t)

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

func TestMicrosoftHandleCallback(t *testing.T) {
	t.Parallel()

	t.Run("full flow creates user and returns tokens", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Mock Microsoft token and user endpoints
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				Scope:       "openid email profile User.Read",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-12345",
				DisplayName:       "Test User",
				Mail:              "test@example.com",
				UserPrincipalName: "test@example.onmicrosoft.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testMicrosoftCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("fallback to userPrincipalName when mail is empty", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "Bearer",
				Scope:       "openid email profile User.Read",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-67890",
				DisplayName:       "UPN User",
				Mail:              "",
				UserPrincipalName: "upn@example.onmicrosoft.com",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testMicrosoftCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Pre-create user and provider link
		user := models.NewUser("existing-ms@example.com")
		user.Name = "Existing MS User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeMicrosoft, "ms-user-99999")
		require.NoError(t, svc.db.CreateUserProvider(ctx, provider))

		// Create membership
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		// Test findOrCreateUser returns existing user
		userInfo := &MicrosoftUserInfo{
			ID:                "ms-user-99999",
			DisplayName:       "Existing MS User",
			Mail:              "existing-ms@example.com",
			UserPrincipalName: "existing-ms@example.onmicrosoft.com",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo, "existing-ms@example.com")
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email-based user matching", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)

		// Pre-create user (e.g., from password login)
		user := models.NewUser("match-ms@example.com")
		user.Name = "Match MS User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		// Microsoft login with same email but new provider ID
		userInfo := &MicrosoftUserInfo{
			ID:                "ms-user-55555",
			DisplayName:       "Match MS User",
			Mail:              "match-ms@example.com",
			UserPrincipalName: "match-ms@example.onmicrosoft.com",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo, "match-ms@example.com")
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)

		// Verify provider was linked
		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, "ms-user-55555")
		require.NoError(t, err)
		assert.Equal(t, user.UID, linked.UserUID)
	})

	t.Run("new user creation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)

		userInfo := &MicrosoftUserInfo{
			ID:                "ms-user-77777",
			DisplayName:       "Brand New User",
			Mail:              "brand-new@example.com",
			UserPrincipalName: "brand-new@example.onmicrosoft.com",
		}

		user, err := svc.findOrCreateUser(ctx, userInfo, "brand-new@example.com")
		require.NoError(t, err)
		assert.Equal(t, "brand-new@example.com", user.Email)
		assert.Equal(t, "Brand New User", user.Name)
		assert.NotNil(t, user.EmailVerifiedAt)
	})

	t.Run("ensure membership first user gets owner", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

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

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

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

	t.Run("no email returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftTokenResponse{AccessToken: "mock-token", TokenType: "Bearer", ExpiresIn: 3600}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		// User endpoint returns no email fields
		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := MicrosoftUserInfo{
				ID:                "ms-user-11111",
				DisplayName:       "No Email User",
				Mail:              "",
				UserPrincipalName: "",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		_, err := testMicrosoftCallbackWithMockServersErr(
			ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL,
		)
		assert.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

func TestMicrosoftGetCallbackURL(t *testing.T) {
	t.Parallel()

	svc, _ := setupMicrosoftTestService(t)

	assert.Equal(t, "http://localhost:4000/api/v1/auth/microsoft/callback", svc.getCallbackURL())
}

func TestMicrosoftGetTokenURL(t *testing.T) {
	t.Parallel()

	t.Run("with configured tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		assert.Equal(t, "https://login.microsoftonline.com/test-tenant-id/oauth2/v2.0/token", svc.getTokenURL())
	})

	t.Run("defaults to common tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = ""
		assert.Equal(t, "https://login.microsoftonline.com/common/oauth2/v2.0/token", svc.getTokenURL())
	})
}

// TestMicrosoftBuildAuthURL asserts the tenant configured on cfg.Microsoft.TenantID
// (which the systemconfig overlay populates from env/DB/UI) is composed into the
// authorize endpoint, and that an empty tenant falls back to "common". This is the
// authorize-side mirror of TestMicrosoftGetTokenURL and guards the spec's core
// requirement that a settable tenant reaches the Microsoft URL.
func TestMicrosoftBuildAuthURL(t *testing.T) {
	t.Parallel()

	t.Run("with configured tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		handler := NewMicrosoftOAuthHandler(svc, svc.cfg)

		authURL := handler.buildMicrosoftAuthURL("state-123")
		assert.Contains(t, authURL, "https://login.microsoftonline.com/test-tenant-id/oauth2/v2.0/authorize?")
	})

	t.Run("defaults to common tenant", func(t *testing.T) {
		t.Parallel()

		svc, _ := setupMicrosoftTestService(t)
		svc.cfg.Microsoft.TenantID = ""
		handler := NewMicrosoftOAuthHandler(svc, svc.cfg)

		authURL := handler.buildMicrosoftAuthURL("state-123")
		assert.Contains(t, authURL, "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?")
	})
}

// testMicrosoftCallbackWithMockServers drives the REAL HandleCallback against
// httptest stand-ins for the Microsoft token and Graph endpoints — including
// the mail-empty → userPrincipalName fallback, which therefore stays covered
// by the production code rather than by a test-local copy of it.
func testMicrosoftCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) *MicrosoftOAuthResult {
	t.Helper()

	result, err := testMicrosoftCallbackWithMockServersErr(ctx, t, svc, orgSlug, tokenURL, userURL)
	require.NoError(t, err)

	return result
}

func testMicrosoftCallbackWithMockServersErr(
	ctx context.Context,
	t *testing.T,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) (*MicrosoftOAuthResult, error) {
	t.Helper()

	svc.tokenURL = tokenURL
	svc.userURL = userURL

	return svc.HandleCallback(ctx, "mock-code", orgSlug)
}
