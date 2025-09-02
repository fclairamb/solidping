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

func setupGitHubTestService(t *testing.T) (*GitHubOAuthService, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() {
		_ = dbService.Close()
	})

	cfg := &config.Config{
		GitHub: config.GitHubOAuthConfig{
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
	svc := NewGitHubOAuthService(dbService, cfg, authService)

	return svc, ctx
}

func setupGitHubTestOrg(ctx context.Context, t *testing.T, svc *GitHubOAuthService) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	return org
}

func TestGitHubOAuthState(t *testing.T) {
	t.Parallel()

	svc, ctx := setupGitHubTestService(t)

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

func TestGitHubHandleCallback(t *testing.T) {
	t.Parallel()

	t.Run("full flow creates user and returns tokens", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

		// Mock GitHub token and user endpoints
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "bearer",
				Scope:       "user:email",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubUserInfo{
				ID:        12345,
				Login:     "testuser",
				Name:      "Test User",
				Email:     "test@example.com",
				AvatarURL: "https://avatars.githubusercontent.com/u/12345",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testGitHubCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("full flow with private email fetches from emails endpoint", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubTokenResponse{
				AccessToken: "mock-access-token",
				TokenType:   "bearer",
				Scope:       "user:email",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		// User endpoint returns no email (private)
		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubUserInfo{
				ID:        67890,
				Login:     "privateuser",
				Name:      "Private User",
				Email:     "",
				AvatarURL: "https://avatars.githubusercontent.com/u/67890",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		// Emails endpoint returns primary verified email
		emailsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			emails := []GitHubEmail{
				{Email: "noreply@github.com", Primary: false, Verified: true},
				{Email: "private@example.com", Primary: true, Verified: true},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(emails)
		}))
		defer emailsServer.Close()

		svc.httpClient = tokenServer.Client()

		result := testGitHubCallbackWithMockServersAndEmails(
			ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL, emailsServer.URL,
		)

		assert.NotEmpty(t, result.AccessToken)
		assert.NotEmpty(t, result.RefreshToken)
		assert.Equal(t, org.Slug, result.OrgSlug)
	})

	t.Run("existing user login via provider ID", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

		// Pre-create user and provider link
		user := models.NewUser("existing@example.com")
		user.Name = "Existing User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		provider := models.NewUserProvider(user.UID, models.ProviderTypeGitHub, "99999")
		require.NoError(t, svc.db.CreateUserProvider(ctx, provider))

		// Create membership
		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, svc.db.CreateOrganizationMember(ctx, member))

		// Test findOrCreateUser returns existing user
		userInfo := &GitHubUserInfo{
			ID:    99999,
			Login: "existinguser",
			Email: "existing@example.com",
			Name:  "Existing User",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)
	})

	t.Run("email-based user matching", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)

		// Pre-create user (e.g., from password login)
		user := models.NewUser("match@example.com")
		user.Name = "Match User"
		require.NoError(t, svc.db.CreateUser(ctx, user))

		// GitHub login with same email but new provider ID
		userInfo := &GitHubUserInfo{
			ID:    55555,
			Login: "matchuser",
			Email: "match@example.com",
			Name:  "Match User",
		}

		foundUser, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, user.UID, foundUser.UID)

		// Verify provider was linked
		linked, err := svc.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitHub, "55555")
		require.NoError(t, err)
		assert.Equal(t, user.UID, linked.UserUID)
	})

	t.Run("new user creation", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)

		userInfo := &GitHubUserInfo{
			ID:        77777,
			Login:     "brandnewuser",
			Email:     "brand-new@example.com",
			Name:      "Brand New User",
			AvatarURL: "https://avatars.githubusercontent.com/u/77777",
		}

		user, err := svc.findOrCreateUser(ctx, userInfo)
		require.NoError(t, err)
		assert.Equal(t, "brand-new@example.com", user.Email)
		assert.Equal(t, "Brand New User", user.Name)
		assert.Equal(t, "https://avatars.githubusercontent.com/u/77777", user.AvatarURL)
		assert.NotNil(t, user.EmailVerifiedAt)
	})

	t.Run("ensure membership first user gets admin", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

		user := models.NewUser("first@example.com")
		require.NoError(t, svc.db.CreateUser(ctx, user))

		member, err := svc.ensureMembership(ctx, org.UID, user.UID)
		require.NoError(t, err)
		assert.Equal(t, models.MemberRoleAdmin, member.Role)
	})

	t.Run("ensure membership second user gets user role", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

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

	t.Run("no verified email returns error", func(t *testing.T) {
		t.Parallel()

		svc, ctx := setupGitHubTestService(t)
		org := setupGitHubTestOrg(ctx, t, svc)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubTokenResponse{AccessToken: "mock-token", TokenType: "bearer", Scope: "user:email"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer tokenServer.Close()

		// User endpoint returns no email
		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			resp := GitHubUserInfo{
				ID:    11111,
				Login: "noemailuser",
				Name:  "No Email User",
				Email: "",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer userServer.Close()

		// Emails endpoint returns no primary verified email
		emailsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			emails := []GitHubEmail{
				{Email: "unverified@example.com", Primary: true, Verified: false},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(emails)
		}))
		defer emailsServer.Close()

		_, err := testGitHubCallbackWithMockServersAndEmailsErr(
			ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL, emailsServer.URL,
		)
		assert.ErrorIs(t, err, ErrEmailNotVerified)
	})
}

func TestGitHubGetCallbackURL(t *testing.T) {
	t.Parallel()

	svc, _ := setupGitHubTestService(t)

	assert.Equal(t, "http://localhost:4000/api/v1/auth/github/callback", svc.getCallbackURL())
}

// testGitHubCallbackWithMockServers is a helper to test the full callback flow with mocked servers.
func testGitHubCallbackWithMockServers(
	ctx context.Context,
	t *testing.T,
	svc *GitHubOAuthService,
	orgSlug, tokenURL, userURL string,
) *GitHubOAuthResult {
	t.Helper()

	result, err := gitHubCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL, "")
	require.NoError(t, err)

	return result
}

func testGitHubCallbackWithMockServersAndEmails(
	ctx context.Context,
	t *testing.T,
	svc *GitHubOAuthService,
	orgSlug, tokenURL, userURL, emailsURL string,
) *GitHubOAuthResult {
	t.Helper()

	result, err := gitHubCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL, emailsURL)
	require.NoError(t, err)

	return result
}

func testGitHubCallbackWithMockServersAndEmailsErr(
	ctx context.Context,
	t *testing.T,
	svc *GitHubOAuthService,
	orgSlug, tokenURL, userURL, emailsURL string,
) (*GitHubOAuthResult, error) {
	t.Helper()

	return gitHubCallbackWithMockedURLs(ctx, svc, orgSlug, tokenURL, userURL, emailsURL)
}

// gitHubCallbackWithMockedURLs performs the HandleCallback flow using mock GitHub endpoints.
func gitHubCallbackWithMockedURLs(
	ctx context.Context,
	svc *GitHubOAuthService,
	orgSlug, tokenURL, userURL, emailsURL string,
) (*GitHubOAuthResult, error) {
	mockSvc := &gitHubMockService{
		GitHubOAuthService: svc,
		tokenURL:           tokenURL,
		userURL:            userURL,
		emailsURL:          emailsURL,
	}

	return mockSvc.handleCallbackMocked(ctx, "mock-code", orgSlug)
}

// gitHubMockService wraps GitHubOAuthService to override HTTP endpoints for testing.
type gitHubMockService struct {
	*GitHubOAuthService
	tokenURL  string
	userURL   string
	emailsURL string
}

func (m *gitHubMockService) handleCallbackMocked(
	ctx context.Context, code, orgSlug string,
) (*GitHubOAuthResult, error) {
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

	// Fetch primary email if not present
	if userInfo.Email == "" && m.emailsURL != "" {
		email, emailErr := m.fetchPrimaryEmailMocked(ctx, tokenResp.AccessToken)
		if emailErr != nil {
			return nil, emailErr
		}

		userInfo.Email = email
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

	return &GitHubOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

func (m *gitHubMockService) exchangeCodeMocked(ctx context.Context, _ string) (*GitHubTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var tokenResp GitHubTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

func (m *gitHubMockService) fetchUserProfileMocked(ctx context.Context, accessToken string) (*GitHubUserInfo, error) {
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

	var userInfo GitHubUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

func (m *gitHubMockService) fetchPrimaryEmailMocked(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.emailsURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var emails []GitHubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}

	return "", nil
}
