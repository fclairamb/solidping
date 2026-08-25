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
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Google OAuth specific errors.
var (
	ErrGoogleTokenExchange = errors.New("google token exchange failed")
	ErrGoogleAPI           = errors.New("google API error")
)

const (
	googleOAuthStatePrefix = "oauth_state:google:"
	googleOAuthStateTTL    = 10 * time.Minute
	googleTokenURL         = "https://oauth2.googleapis.com/token"
	googleUserInfoURL      = "https://www.googleapis.com/oauth2/v3/userinfo"
)

// GoogleUserInfo represents user info from Google's userinfo endpoint.
//
//nolint:tagliatelle // Google API uses snake_case JSON field names
type GoogleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// GoogleTokenResponse represents the response from Google's token exchange.
//
//nolint:tagliatelle // Google API uses snake_case JSON field names
type GoogleTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
}

// GoogleOAuthResult contains the result of a successful Google OAuth flow.
type GoogleOAuthResult struct {
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

// GoogleOAuthService handles Google OAuth authentication logic.
type GoogleOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	// tokenURL / userInfoURL override the Google endpoints. Empty in
	// production; tests point them at an httptest server so the REAL
	// HandleCallback runs end to end (see microsoft_service.go for why a
	// test-local re-implementation is not good enough).
	tokenURL    string
	userInfoURL string
}

// NewGoogleOAuthService creates a new Google OAuth service.
func NewGoogleOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *GoogleOAuthService {
	return &GoogleOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *GoogleOAuthService) GenerateOAuthState(ctx context.Context, redirectURI, orgSlug string) (string, error) {
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

	stateValue := &models.JSONMap{"state": string(stateJSON)}
	ttl := googleOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, googleOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *GoogleOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, googleOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, googleOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)["state"].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(googleOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from Google.
func (s *GoogleOAuthService) HandleCallback(ctx context.Context, code, orgSlug string) (*GoogleOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGoogleTokenExchange, err)
	}

	// Fetch user info
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrGoogleAPI, err)
	}

	// Validate email
	if userInfo.Email == "" || !userInfo.EmailVerified {
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
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodGoogle))
	if err != nil {
		return nil, err
	}

	return &GoogleOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// exchangeCode exchanges an authorization code for tokens with Google.
func (s *GoogleOAuthService) exchangeCode(ctx context.Context, code string) (*GoogleTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.Google.ClientID)
	data.Set("client_secret", s.cfg.Google.ClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", s.getCallbackURL())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.getTokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp GoogleTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrGoogleAPI, tokenResp.Error, tokenResp.ErrorDesc)
	}

	return &tokenResp, nil
}

// getTokenURL returns Google's token endpoint (test-overridable).
func (s *GoogleOAuthService) getTokenURL() string {
	if s.tokenURL != "" {
		return s.tokenURL
	}

	return googleTokenURL
}

// getUserInfoURL returns Google's userinfo endpoint (test-overridable).
func (s *GoogleOAuthService) getUserInfoURL() string {
	if s.userInfoURL != "" {
		return s.userInfoURL
	}

	return googleUserInfoURL
}

// fetchUserProfile fetches user profile from Google's userinfo endpoint.
func (s *GoogleOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*GoogleUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.getUserInfoURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var userInfo GoogleUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrGoogleAPI, resp.StatusCode)
	}

	return &userInfo, nil
}

// findOrCreateUser finds or creates a user by Google identity.
func (s *GoogleOAuthService) findOrCreateUser(ctx context.Context, userInfo *GoogleUserInfo) (*models.User, error) {
	// Check by Google user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeGoogle, userInfo.Sub)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		// A link pointing at a soft-deleted user is stale: it is cleared
		// and we fall through to the email lookup / create path rather
		// than failing this login (and every later one) forever.
		user, resolveErr := resolveLinkedUser(ctx, s.db, models.ProviderTypeGoogle, userInfo.Sub, provider)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if user != nil {
			return user, nil
		}

		provider = nil
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
		user.AvatarURL = userInfo.Picture

		if userInfo.EmailVerified {
			now := time.Now()
			user.EmailVerifiedAt = &now
		}

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodGoogle); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link Google provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeGoogle, userInfo.Sub)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// getCallbackURL returns the OAuth callback URL for Google.
func (s *GoogleOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/google/callback"
}
