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
	// ErrGitLabLinkRequiresSignIn refuses a first GitLab sign-in whose email
	// matches an existing SolidPing account (GHSA-c7jr-53g7-65q8). The GitLab
	// instance admin controls the emails of every account on it, so even a
	// confirmed GitLab email does not prove the SolidPing account is the same
	// person's: the owner signs in with the method they already use instead.
	ErrGitLabLinkRequiresSignIn = errors.New("an account already uses this email: sign in with it first")
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
	// ConfirmedAt is when the GitLab account confirmed its email. Null or
	// absent means unconfirmed: self-managed instances can run with email
	// confirmation off or soft, and an admin can set any address.
	ConfirmedAt *string `json:"confirmed_at"`
}

// GitLabEmail is one entry of GitLab's /api/v4/user/emails endpoint.
//
//nolint:tagliatelle // GitLab API uses snake_case JSON field names
type GitLabEmail struct {
	ID          int     `json:"id"`
	Email       string  `json:"email"`
	ConfirmedAt *string `json:"confirmed_at"`
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
// It is the ProviderOutcome every federated callback hands to
// finishProviderCallback.
type GitLabOAuthResult = ProviderOutcome

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

	// The email must carry a verification signal before it can name, create
	// or admit an account (GHSA-c7jr-53g7-65q8). Fail closed: an unconfirmed
	// profile is refused before the emails list is even fetched.
	if !gitLabAccountConfirmed(userInfo) {
		return nil, ErrEmailNotVerified
	}

	emails, err := s.fetchUserEmails(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user emails: %w", ErrGitLabAPI, err)
	}

	if !gitLabEmailConfirmed(userInfo.Email, emails) {
		return nil, ErrEmailNotVerified
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, _, err := s.findOrCreateUser(ctx, userInfo, true)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending instead of a membership, with a session on an org they
	// already belong to (login.FallbackOrgSlug) or an org-less one.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user,
		WithLoginMethod(signupMethodGitLab))
	if err != nil {
		return nil, err
	}

	return login.Outcome(org.Slug, user.UID), nil
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

// gitLabAccountConfirmed reports whether the GitLab profile has an email and a
// non-null confirmed_at. A missing field counts as unconfirmed.
func gitLabAccountConfirmed(userInfo *GitLabUserInfo) bool {
	return userInfo != nil && userInfo.Email != "" &&
		userInfo.ConfirmedAt != nil && *userInfo.ConfirmedAt != ""
}

// gitLabEmailConfirmed reports whether primary is listed in the
// /api/v4/user/emails response with a non-null confirmed_at. An entry without
// that field is treated as unconfirmed.
func gitLabEmailConfirmed(primary string, emails []GitLabEmail) bool {
	if primary == "" {
		return false
	}

	for i := range emails {
		entry := &emails[i]
		if strings.EqualFold(strings.TrimSpace(entry.Email), strings.TrimSpace(primary)) &&
			entry.ConfirmedAt != nil && *entry.ConfirmedAt != "" {
			return true
		}
	}

	return false
}

// fetchUserEmails fetches the token owner's emails from GitLab's
// /api/v4/user/emails endpoint (read_user scope).
func (s *GitLabOAuthService) fetchUserEmails(ctx context.Context, accessToken string) ([]GitLabEmail, error) {
	body, err := s.getGitLabAPI(ctx, accessToken, "/api/v4/user/emails")
	if err != nil {
		return nil, err
	}

	var emails []GitLabEmail
	if err := json.Unmarshal(body, &emails); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return emails, nil
}

// fetchUserProfile fetches user profile from GitLab's /api/v4/user endpoint.
func (s *GitLabOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*GitLabUserInfo, error) {
	body, err := s.getGitLabAPI(ctx, accessToken, "/api/v4/user")
	if err != nil {
		return nil, err
	}

	var userInfo GitLabUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// getGitLabAPI issues an authenticated GET on the GitLab API and returns the
// body of a 200 response.
func (s *GitLabOAuthService) getGitLabAPI(ctx context.Context, accessToken, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.getGitLabBaseURL()+path, nil)
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

	return body, nil
}

// findOrCreateUser finds or creates a user by GitLab identity.
//
// An identity already linked signs in to its account. Otherwise the email is
// only used to create a NEW account, and only when emailVerified is true (the
// caller checked confirmed_at on /user and /user/emails). An email that
// matches an existing account is refused with ErrGitLabLinkRequiresSignIn: it
// is never linked silently (GHSA-c7jr-53g7-65q8).
func (s *GitLabOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *GitLabUserInfo, emailVerified bool,
) (*models.User, bool, error) {
	// created reports whether THIS call minted the account.
	var created bool

	providerID := strconv.Itoa(userInfo.ID)

	// Check by GitLab user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGitLab, providerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := ResolveLinkedUser(ctx, s.db, models.ProviderTypeGitLab, providerID, provider)
		if resolveErr != nil {
			return nil, false, resolveErr
		}

		if user != nil {
			return user, false, nil
		}
	}

	// No link (or a stale one, just cleared): the paths below create an account from the email and mark it
	// verified, which drives invitations and email-domain auto-join. Only a
	// confirmed email may do that.
	if !emailVerified || userInfo.Email == "" {
		return nil, false, ErrEmailNotVerified
	}

	// An existing account with this email is never linked on a GitLab
	// sign-in: its owner signs in with their existing method.
	existing, err := s.db.GetUserByEmail(ctx, userInfo.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get user by email: %w", err)
	}

	if existing != nil {
		return nil, false, ErrGitLabLinkRequiresSignIn
	}

	created = true
	user := models.NewUser(userInfo.Email)
	user.Name = userInfo.Name
	user.AvatarURL = userInfo.AvatarURL

	// Safe only because emailVerified is true here (checked above).
	now := time.Now()
	user.EmailVerifiedAt = &now

	// Routed through the package's single account-creation chokepoint so
	// the user_signed_up product event fires for SSO signups too.
	if err := createUserAndCapture(ctx, s.db, user, signupMethodGitLab); err != nil {
		return nil, false, fmt.Errorf("failed to create user: %w", err)
	}

	provider = models.NewUserProvider(user.UID, models.ProviderTypeGitLab, providerID)

	if err := s.db.CreateUserProvider(ctx, provider); err != nil {
		return nil, false, fmt.Errorf("failed to create user provider: %w", err)
	}

	return user, created, nil
}

// getCallbackURL returns the OAuth callback URL for GitLab.
func (s *GitLabOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/gitlab/callback"
}
