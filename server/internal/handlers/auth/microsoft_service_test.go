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

	authService := NewService(dbService, cfg.Auth, cfg, nil)
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

	t.Run("ensure membership first user gets admin", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		user := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		member, err := svc.ensureMembership(ctx, org.UID, user.UID)
		require.NoError(t, err)
		assert.Equal(t, models.MemberRoleAdmin, member.Role)
	})

	t.Run("ensure membership second user gets user role", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Create first user as admin
		firstUser := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, firstUser))

		_, err := svc.ensureMembership(ctx, org.UID, firstUser.UID)
		require.NoError(t, err)

		// Second user should get user role
		secondUser := models.NewUser("second@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, secondUser))

		member, err := svc.ensureMembership(ctx, org.UID, secondUser.UID)
		require.NoError(t, err)
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

// testMicrosoftCallbackWithMockServers is a helper to test the full callback flow with mocked servers.
func testMicrosoftCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) *MicrosoftOAuthResult {
	t.Helper()

	result, err := microsoftCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL)
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

	return microsoftCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL)
}

// microsoftCallbackWithMockedURLs performs the HandleCallback flow using mock Microsoft endpoints.
func microsoftCallbackWithMockedURLs(
	ctx context.Context,
	svc *MicrosoftOAuthService,
	orgSlug, tokenURL, userURL string,
) (*MicrosoftOAuthResult, error) {
	mockSvc := &microsoftMockService{
		MicrosoftOAuthService: svc,
		tokenURL:              tokenURL,
		userURL:               userURL,
	}

	return mockSvc.handleCallbackMocked(ctx, "mock-code", orgSlug)
}

// microsoftMockService wraps MicrosoftOAuthService to override HTTP endpoints for testing.
type microsoftMockService struct {
	*MicrosoftOAuthService
	tokenURL string
	userURL  string
}

func (m *microsoftMockService) handleCallbackMocked(
	ctx context.Context, code, orgSlug string,
) (*MicrosoftOAuthResult, error) {
	// Exchange code using mock token URL
	tokenResp, err := m.exchangeCodeMocked(ctx, code)
	if err != nil {
		return nil, err
	}

	// Fetch user info using mock user URL
	userInfo, err := m.fetchUserProfileMocked(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, err
	}

	// Get email: prefer mail, fallback to userPrincipalName
	email := userInfo.Mail
	if email == "" {
		email = userInfo.UserPrincipalName
	}

	if email == "" {
		return nil, ErrEmailNotVerified
	}

	org, err := m.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	user, err := m.findOrCreateUser(ctx, userInfo, email)
	if err != nil {
		return nil, err
	}

	member, err := m.ensureMembership(ctx, org.UID, user.UID)
	if err != nil {
		return nil, err
	}

	tokens, err := m.authService.GenerateTokensForOAuth(ctx, user, org, string(member.Role))
	if err != nil {
		return nil, err
	}

	return &MicrosoftOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

func (m *microsoftMockService) exchangeCodeMocked(ctx context.Context, _ string) (*MicrosoftTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var tokenResp MicrosoftTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

func (m *microsoftMockService) fetchUserProfileMocked(
	ctx context.Context, accessToken string,
) (*MicrosoftUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.userURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var userInfo MicrosoftUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}
