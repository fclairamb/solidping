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
	OrgSlug      string
	UserUID      string
}

// MicrosoftOAuthService handles Microsoft OAuth authentication logic.
type MicrosoftOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client
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

	stateValue := &models.JSONMap{"state": string(stateJSON)}
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
	_ = s.db.DeleteStateEntry(ctx, nil, microsoftOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)["state"].(string)
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

	// Get email: prefer mail, fallback to userPrincipalName
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

	return &MicrosoftOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

// getTokenURL returns the Microsoft token endpoint URL for the configured tenant.
func (s *MicrosoftOAuthService) getTokenURL() string {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, microsoftUserURL, nil)
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

		if err := s.db.CreateUser(ctx, user); err != nil {
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

// ensureMembership ensures user is a member of the organization.
func (s *MicrosoftOAuthService) ensureMembership(
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

// getCallbackURL returns the OAuth callback URL for Microsoft.
func (s *MicrosoftOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/microsoft/callback"
}
