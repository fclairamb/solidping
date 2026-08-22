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

// Microsoft OAuth specific errors.
var (
	ErrMicrosoftTokenExchange = errors.New("microsoft token exchange failed")
	ErrMicrosoftAPI           = errors.New("microsoft API error")
)

const (
	microsoftOAuthStatePrefix = "oauth_state:microsoft:"
	microsoftOAuthStateTTL    = 10 * time.Minute
	microsoftUserURL          = "https://graph.microsoft.com/v1.0/me"
)

// MicrosoftUserInfo represents user info from Microsoft Graph /me endpoint.
type MicrosoftUserInfo struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// MicrosoftTokenResponse represents the response from Microsoft's token exchange.
//
//nolint:tagliatelle // Microsoft OAuth uses snake_case JSON field names
type MicrosoftTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
}

// MicrosoftOAuthResult contains the result of a successful Microsoft OAuth flow.
type MicrosoftOAuthResult struct {
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

// MicrosoftOAuthService handles Microsoft OAuth authentication logic.
type MicrosoftOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	// tokenURL / userURL override the Microsoft endpoints. Empty in
	// production (the real login.microsoftonline.com / graph.microsoft.com
	// URLs are used); tests point them at an httptest server so the REAL
	// HandleCallback can be driven end to end instead of a re-implementation
	// of it, which would not notice a regression reintroduced in the real
	// code path.
	tokenURL string
	userURL  string
}

// NewMicrosoftOAuthService creates a new Microsoft OAuth service.
func NewMicrosoftOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *MicrosoftOAuthService {
	return &MicrosoftOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *MicrosoftOAuthService) GenerateOAuthState(ctx context.Context, redirectURI, orgSlug string) (string, error) {
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
	ttl := microsoftOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, microsoftOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *MicrosoftOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, microsoftOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, microsoftOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(microsoftOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from Microsoft.
func (s *MicrosoftOAuthService) HandleCallback(
	ctx context.Context, code, orgSlug string,
) (*MicrosoftOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMicrosoftTokenExchange, err)
	}

	// Fetch user info from Microsoft Graph
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrMicrosoftAPI, err)
	}

	// Get email: prefer mail, fallback to userPrincipalName.
	//
	// KNOWN LIMITATION (tracked separately, deliberately not fixed here): when
	// Graph returns an empty `mail`, the UPN we fall back to is often the
	// tenant's *.onmicrosoft.com form (alice@contoso.onmicrosoft.com) rather
	// than the address the person actually uses (alice@contoso.com). Because
	// findOrCreateUser links accounts by email, that mints a SECOND local user
	// instead of linking the existing one — and, being a different domain, it
	// is also the address the org's registration.email_pattern is matched
	// against. Fixing it means resolving the real address (e.g. Graph
	// otherMails / proxyAddresses) before the lookup; scope for another spec.
	email := userInfo.Mail
	if email == "" {
		email = userInfo.UserPrincipalName
	}

	// Validate email
	if email == "" {
		return nil, ErrEmailNotVerified
	}

	// Look up organization by slug
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, fmt.Errorf("organization not found: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, email)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodMicrosoft))
	if err != nil {
		return nil, err
	}

	return &MicrosoftOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// getTokenURL returns the Microsoft token endpoint URL for the configured tenant.
func (s *MicrosoftOAuthService) getTokenURL() string {
	if s.tokenURL != "" {
		return s.tokenURL
	}

	tenant := s.cfg.Microsoft.TenantID
	if tenant == "" {
		tenant = "common"
	}

	return "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token"
}

// exchangeCode exchanges an authorization code for tokens with Microsoft.
func (s *MicrosoftOAuthService) exchangeCode(ctx context.Context, code string) (*MicrosoftTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.Microsoft.ClientID)
	data.Set("client_secret", s.cfg.Microsoft.ClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", s.getCallbackURL())
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.getTokenURL(), strings.NewReader(data.Encode()))
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

	var tokenResp MicrosoftTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrMicrosoftAPI, tokenResp.Error, tokenResp.ErrorDesc)
	}

	return &tokenResp, nil
}

// fetchUserProfile fetches user profile from Microsoft Graph /me endpoint.
func (s *MicrosoftOAuthService) fetchUserProfile(ctx context.Context, accessToken string) (*MicrosoftUserInfo, error) {
	profileURL := microsoftUserURL
	if s.userURL != "" {
		profileURL = s.userURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
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
		return nil, fmt.Errorf("%w: status %d", ErrMicrosoftAPI, resp.StatusCode)
	}

	var userInfo MicrosoftUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// findOrCreateUser finds or creates a user by Microsoft identity.
func (s *MicrosoftOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *MicrosoftUserInfo, email string,
) (*models.User, error) {
	// Check by Microsoft user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeMicrosoft, userInfo.ID)
	if err == nil && provider != nil {
		return s.db.GetUser(ctx, provider.UserUID)
	}

	// Check by email
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}

	// Create new user if not found
	if user == nil {
		user = models.NewUser(email)
		user.Name = userInfo.DisplayName

		now := time.Now()
		user.EmailVerifiedAt = &now

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodMicrosoft); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link Microsoft provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeMicrosoft, userInfo.ID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// getCallbackURL returns the OAuth callback URL for Microsoft.
func (s *MicrosoftOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/microsoft/callback"
}
