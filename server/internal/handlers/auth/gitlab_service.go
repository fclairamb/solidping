package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GitLab OAuth specific errors.
var (
	ErrGitLabTokenExchange = errors.New("gitlab token exchange failed")
	ErrGitLabAPI           = errors.New("gitlab API error")
)

const (
	gitLabOAuthStatePrefix = "oauth_state:gitlab:"
	gitLabOAuthStateTTL    = 10 * time.Minute
	gitLabDefaultBaseURL   = "https://gitlab.com"
)

// GitLabUserInfo represents user info from GitLab's /api/v4/user endpoint.
//
//nolint:tagliatelle // GitLab API uses snake_case JSON field names
type GitLabUserInfo struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// GitLabTokenResponse represents the response from GitLab's token exchange.
//
//nolint:tagliatelle // GitLab OAuth uses snake_case JSON field names
type GitLabTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// GitLabOAuthResult contains the result of a successful GitLab OAuth flow.
type GitLabOAuthResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	OrgSlug      string
	UserUID      string
	// Pending is true when the login succeeded but the org did not admit
	// the user: no membership was created, a membership request is awaiting
	// admin approval, and the tokens above are an org-less session.
	Pending bool
}

// GitLabOAuthService handles GitLab OAuth authentication logic.
type GitLabOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client
}

// NewGitLabOAuthService creates a new GitLab OAuth service.
func NewGitLabOAuthService(
	dbService db.Service, cfg *config.Config, authService *Service,
) *GitLabOAuthService {
	return &GitLabOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *GitLabOAuthService) GenerateOAuthState(
	ctx context.Context, redirectURI, orgSlug string,
) (string, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	state := OAuthState{
		Nonce:       nonce,
		RedirectURI: redirectURI,
		OrgSlug:     orgSlug,
		CreatedAt:   time.Now().Unix(),
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	stateValue := &models.JSONMap{keyState: string(stateJSON)}
	ttl := gitLabOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, gitLabOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *GitLabOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, gitLabOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, gitLabOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(gitLabOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from GitLab.
func (s *GitLabOAuthService) HandleCallback(
	ctx context.Context, code, orgSlug string,
) (*GitLabOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGitLabTokenExchange, err)
	}

	// Fetch user info
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrGitLabAPI, err)
	}

	// Validate email
	if userInfo.Email == "" {
		return nil, ErrEmailNotVerified
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodGitLab))
	if err != nil {
		return nil, err
	}

	return &GitLabOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// getGitLabBaseURL returns the GitLab instance base URL.
func (s *GitLabOAuthService) getGitLabBaseURL() string {
	if s.cfg.GitLab.BaseURL != "" {
		return strings.TrimRight(s.cfg.GitLab.BaseURL, "/")
	}

	return gitLabDefaultBaseURL
}

// exchangeCode exchanges an authorization code for tokens with GitLab.
func (s *GitLabOAuthService) exchangeCode(ctx context.Context, code string) (*GitLabTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.GitLab.ClientID)
	data.Set("client_secret", s.cfg.GitLab.ClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", s.getCallbackURL())

	tokenURL := s.getGitLabBaseURL() + "/oauth/token"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp GitLabTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrGitLabAPI, tokenResp.Error, tokenResp.ErrorDesc)
	}

	return &tokenResp, nil
}

// fetchUserProfile fetches user profile from GitLab's /api/v4/user endpoint.
func (s *GitLabOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*GitLabUserInfo, error) {
	userURL := s.getGitLabBaseURL() + "/api/v4/user"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrGitLabAPI, resp.StatusCode)
	}

	var userInfo GitLabUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// findOrCreateUser finds or creates a user by GitLab identity.
//
//nolint:dupl // OAuth provider findOrCreateUser methods share similar structure by design
func (s *GitLabOAuthService) findOrCreateUser(ctx context.Context, userInfo *GitLabUserInfo) (*models.User, error) {
	providerID := strconv.Itoa(userInfo.ID)

	// Check by GitLab user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitLab, providerID)
	if err == nil && provider != nil {
		return s.db.GetUser(ctx, provider.UserUID)
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found
	if user == nil {
		user = models.NewUser(userInfo.Email)
		user.Name = userInfo.Name
		user.AvatarURL = userInfo.AvatarURL

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodGitLab); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link GitLab provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeGitLab, providerID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// getCallbackURL returns the OAuth callback URL for GitLab.
func (s *GitLabOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/gitlab/callback"
}
