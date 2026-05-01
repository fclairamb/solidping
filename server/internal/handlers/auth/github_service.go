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

// GitHub OAuth specific errors.
var (
	ErrGitHubTokenExchange = errors.New("github token exchange failed")
	ErrGitHubAPI           = errors.New("github API error")
)

const (
	gitHubOAuthStatePrefix = "oauth_state:github:"
	gitHubOAuthStateTTL    = 10 * time.Minute
	gitHubTokenURL         = "https://github.com/login/oauth/access_token"
	gitHubUserURL          = "https://api.github.com/user"
	gitHubUserEmailsURL    = "https://api.github.com/user/emails"
)

// GitHubUserInfo represents user info from GitHub's user endpoint.
type GitHubUserInfo struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"` //nolint:tagliatelle // GitHub API uses snake_case
}

// GitHubEmail represents an email from GitHub's user/emails endpoint.
type GitHubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// GitHubTokenResponse represents the response from GitHub's token exchange.
//
//nolint:tagliatelle // GitHub API uses snake_case JSON field names
type GitHubTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
}

// GitHubOAuthResult contains the result of a successful GitHub OAuth flow.
type GitHubOAuthResult struct {
	AccessToken  string
	RefreshToken string
	OrgSlug      string
	UserUID      string
}

// GitHubOAuthService handles GitHub OAuth authentication logic.
type GitHubOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client
}

// NewGitHubOAuthService creates a new GitHub OAuth service.
func NewGitHubOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *GitHubOAuthService {
	return &GitHubOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *GitHubOAuthService) GenerateOAuthState(ctx context.Context, redirectURI, orgSlug string) (string, error) {
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
	ttl := gitHubOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, gitHubOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *GitHubOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, gitHubOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_ = s.db.DeleteStateEntry(ctx, nil, gitHubOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(gitHubOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from GitHub.
func (s *GitHubOAuthService) HandleCallback(ctx context.Context, code, orgSlug string) (*GitHubOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGitHubTokenExchange, err)
	}

	// Fetch user info
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrGitHubAPI, err)
	}

	// GitHub users can have private emails, fetch from emails endpoint
	if userInfo.Email == "" {
		email, emailErr := s.fetchPrimaryEmail(ctx, tokenResp.AccessToken)
		if emailErr != nil {
			return nil, fmt.Errorf("%w: failed to fetch user emails: %w", ErrGitHubAPI, emailErr)
		}

		userInfo.Email = email
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

	// Ensure organization membership
	member, err := s.ensureMembership(ctx, org.UID, user.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure membership: %w", err)
	}

	// Auto-join matching orgs
	s.authService.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	// Generate tokens
	tokens, err := s.authService.GenerateTokensForOAuth(ctx, user, org, string(member.Role))
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	return &GitHubOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

// exchangeCode exchanges an authorization code for tokens with GitHub.
func (s *GitHubOAuthService) exchangeCode(ctx context.Context, code string) (*GitHubTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.GitHub.ClientID)
	data.Set("client_secret", s.cfg.GitHub.ClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", s.getCallbackURL())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gitHubTokenURL, strings.NewReader(data.Encode()))
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

	var tokenResp GitHubTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrGitHubAPI, tokenResp.Error, tokenResp.ErrorDesc)
	}

	return &tokenResp, nil
}

// fetchUserProfile fetches user profile from GitHub's user endpoint.
func (s *GitHubOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*GitHubUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitHubUserURL, nil)
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
		return nil, fmt.Errorf("%w: status %d", ErrGitHubAPI, resp.StatusCode)
	}

	var userInfo GitHubUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// fetchPrimaryEmail fetches the user's primary verified email from GitHub.
func (s *GitHubOAuthService) fetchPrimaryEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitHubUserEmailsURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", ErrGitHubAPI, resp.StatusCode)
	}

	var emails []GitHubEmail
	if err := json.Unmarshal(body, &emails); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}

	return "", nil
}

// findOrCreateUser finds or creates a user by GitHub identity.
//
//nolint:dupl // OAuth provider findOrCreateUser methods share similar structure by design
func (s *GitHubOAuthService) findOrCreateUser(ctx context.Context, userInfo *GitHubUserInfo) (*models.User, error) {
	providerID := strconv.Itoa(userInfo.ID)

	// Check by GitHub user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitHub, providerID)
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

		if err := s.db.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link GitHub provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeGitHub, providerID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// ensureMembership ensures user is a member of the organization.
func (s *GitHubOAuthService) ensureMembership(
	ctx context.Context, orgUID, userUID string,
) (*models.OrganizationMember, error) {
	// Check existing membership
	member, err := s.db.GetMemberByUserAndOrg(ctx, userUID, orgUID)
	if err == nil {
		return member, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get member: %w", err)
	}

	// Determine role (first user = admin)
	role := models.MemberRoleUser

	members, err := s.db.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list members: %w", err)
	}

	if len(members) == 0 {
		role = models.MemberRoleAdmin
	}

	// Create membership
	member = models.NewOrganizationMember(orgUID, userUID, role)
	now := time.Now()
	member.JoinedAt = &now

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to create member: %w", err)
	}

	return member, nil
}

// getCallbackURL returns the OAuth callback URL for GitHub.
func (s *GitHubOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/github/callback"
}
