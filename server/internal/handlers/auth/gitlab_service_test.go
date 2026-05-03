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

	authService := NewService(dbService, cfg.Auth, cfg, nil)
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

func TestGitLabHandleCallback(t *testing.T) {
	t.Parallel()

	t.Run("full flow creates user and returns tokens", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		// Mock GitLab token and user endpoints
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitLabTokenResponse{
				AccessToken:  "mock-access-token",
				TokenType:    "Bearer",
				ExpiresIn:    7200,
				RefreshToken: "mock-refresh-token",
				Scope:        "read_user openid email",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitLabUserInfo{
				ID:        12345,
				Username:  "testuser",
				Name:      "Test GL User",
				Email:     "test-gl@example.com",
				AvatarURL: "https://gitlab.com/uploads/-/system/user/avatar/12345/avatar.png",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testGitLabCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		// Pre-create user and provider link
		user := models.NewUser("existing-gl@example.com")
		user.Name = "Existing GL User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeGitLab, "99999")
		require.NoError(t, svc.db.CreateUserProvider(ctx, provider))

		// Create membership
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		// Test findOrCreateUser returns existing user
		userInfo := &GitLabUserInfo{
			ID:       99999,
			Username: "existinguser",
			Email:    "existing-gl@example.com",
			Name:     "Existing GL User",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email-based user matching", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)

		// Pre-create user (e.g., from password login)
		user := models.NewUser("match-gl@example.com")
		user.Name = "Match GL User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		// GitLab login with same email but new provider ID
		userInfo := &GitLabUserInfo{
			ID:       55555,
			Username: "matchuser",
			Email:    "match-gl@example.com",
			Name:     "Match GL User",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)

		// Verify provider was linked
		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitLab, "55555")
		require.NoError(t, err)
		assert.Equal(t, user.UID, linked.UserUID)
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

		user, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, "brand-new-gl@example.com", user.Email)
		assert.Equal(t, "Brand New GL User", user.Name)
		assert.Equal(t, "https://gitlab.com/uploads/-/system/user/avatar/77777/avatar.png", user.AvatarURL)
		assert.NotNil(t, user.EmailVerifiedAt)
	})

	t.Run("ensure membership first user gets admin", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		user := models.NewUser("first-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		member, err := svc.ensureMembership(ctx, org.UID, user.UID)
		require.NoError(t, err)
		assert.Equal(t, models.MemberRoleAdmin, member.Role)
	})

	t.Run("ensure membership second user gets user role", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		// Create first user as admin
		firstUser := models.NewUser("first-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, firstUser))

		_, err := svc.ensureMembership(ctx, org.UID, firstUser.UID)
		require.NoError(t, err)

		// Second user should get user role
		secondUser := models.NewUser("second-gl@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, secondUser))

		member, err := svc.ensureMembership(ctx, org.UID, secondUser.UID)
		require.NoError(t, err)
		assert.Equal(t, models.MemberRoleUser, member.Role)
	})

	t.Run("no email returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitLabTestService(t)
		org := setupGitLabTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitLabTokenResponse{AccessToken: "mock-token", TokenType: "Bearer", ExpiresIn: 7200}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		// User endpoint returns no email
		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitLabUserInfo{
				ID:       11111,
				Username: "noemailuser",
				Name:     "No Email GL User",
				Email:    "",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		_, err := testGitLabCallbackWithMockServersErr(
			ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL,
		)
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

// testGitLabCallbackWithMockServers is a helper to test the full callback flow with mocked servers.
func testGitLabCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *GitLabOAuthService,
	orgSlug, tokenURL, userURL string,
) *GitLabOAuthResult {
	t.Helper()

	result, err := gitLabCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL)
	require.NoError(t, err)

	return result
}

func testGitLabCallbackWithMockServersErr(
	ctx context.Context,
	t *testing.T,
	svc *GitLabOAuthService,
	orgSlug, tokenURL, userURL string,
) (*GitLabOAuthResult, error) {
	t.Helper()

	return gitLabCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL)
}

// gitLabCallbackWithMockedURLs performs the HandleCallback flow using mock GitLab endpoints.
func gitLabCallbackWithMockedURLs(
	ctx context.Context,
	svc *GitLabOAuthService,
	orgSlug, tokenURL, userURL string,
) (*GitLabOAuthResult, error) {
	mockSvc := &gitLabMockService{
		GitLabOAuthService: svc,
		tokenURL:           tokenURL,
		userURL:            userURL,
	}

	return mockSvc.handleCallbackMocked(ctx, "mock-code", orgSlug)
}

// gitLabMockService wraps GitLabOAuthService to override HTTP endpoints for testing.
type gitLabMockService struct {
	*GitLabOAuthService
	tokenURL string
	userURL  string
}

func (m *gitLabMockService) handleCallbackMocked(
	ctx context.Context, code, orgSlug string,
) (*GitLabOAuthResult, error) {
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

	if userInfo.Email == "" {
		return nil, ErrEmailNotVerified
	}

	org, err := m.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	user, err := m.findOrCreateUser(ctx, userInfo)
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

	return &GitLabOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

func (m *gitLabMockService) exchangeCodeMocked(ctx context.Context, _ string) (*GitLabTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var tokenResp GitLabTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

func (m *gitLabMockService) fetchUserProfileMocked(
	ctx context.Context, accessToken string,
) (*GitLabUserInfo, error) {
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

	var userInfo GitLabUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}
