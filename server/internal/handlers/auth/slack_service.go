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

// Slack OAuth specific errors.
var (
	ErrInvalidOAuthState  = errors.New("invalid or expired OAuth state")
	ErrSlackOAuthFailed   = errors.New("slack OAuth exchange failed")
	ErrEmailNotVerified   = errors.New("email not verified in Slack profile")
	ErrSlackTokenExchange = errors.New("token exchange failed")
	ErrSlackAPI           = errors.New("slack API error")
)

const (
	oauthStatePrefix = "oauth_state:slack:"
	oauthStateTTL    = 10 * time.Minute
	slackOAuthURL    = "https://slack.com/api/oauth.v2.access"
	slackAPIBaseURL  = "https://slack.com/api"
	defaultTimeout   = 30 * time.Second
)

// Slack API types (inlined to avoid import cycle).

// AuthedUser represents the authenticated user in OAuth response.
//
//nolint:tagliatelle // Slack API uses snake_case JSON field names
type AuthedUser struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// OAuthResponse represents the response from Slack's OAuth token exchange.
//
//nolint:tagliatelle // Slack API uses snake_case JSON field names
type OAuthResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AccessToken string `json:"access_token"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AuthedUser AuthedUser `json:"authed_user"`
}

// OpenIDUserInfo represents user info from OpenID Connect endpoint.
//
//nolint:tagliatelle // Slack API uses custom JSON field names
type OpenIDUserInfo struct {
	OK            bool   `json:"ok"`
	Sub           string `json:"sub"` // Slack user ID
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	Error         string `json:"error,omitempty"`
}

// OAuthState represents the state stored during OAuth flow.
type OAuthState struct {
	Nonce       string `json:"nonce"`
	RedirectURI string `json:"redirectUri"`
	OrgSlug     string `json:"orgSlug,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

// SlackOAuthResult contains the result of a successful Slack OAuth flow.
type SlackOAuthResult struct {
	AccessToken  string
	RefreshToken string
	OrgSlug      string
	UserUID      string
}

// SlackOAuthService handles Slack OAuth authentication logic.
type SlackOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service // Reuse existing auth service for token generation
}

// NewSlackOAuthService creates a new Slack OAuth service.
func NewSlackOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *SlackOAuthService {
	return &SlackOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *SlackOAuthService) GenerateOAuthState(ctx context.Context, redirectURI string) (string, error) {
	// Generate nonce
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	// Create state object
	state := OAuthState{
		Nonce:       nonce,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now().Unix(),
	}

	// Encode state as JSON
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	// Store in state_entries table with TTL
	stateValue := &models.JSONMap{"state": string(stateJSON)}
	ttl := oauthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, oauthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	// Return nonce as state parameter
	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *SlackOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	// Retrieve state from store
	entry, err := s.db.GetStateEntry(ctx, nil, oauthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_ = s.db.DeleteStateEntry(ctx, nil, oauthStatePrefix+stateParam)

	// Parse state
	stateJSON, ok := (*entry.Value)["state"].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	// Check expiry
	if time.Now().Unix()-state.CreatedAt > int64(oauthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// exchangeCode exchanges an OAuth code for an access token.
func exchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (*OAuthResponse, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)

	if redirectURI != "" {
		data.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackOAuthURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var oauthResp OAuthResponse
	if err := json.Unmarshal(body, &oauthResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !oauthResp.OK {
		return nil, fmt.Errorf("%w: %s", ErrSlackAPI, oauthResp.Error)
	}

	return &oauthResp, nil
}

// fetchOpenIDUserInfo fetches user info via OpenID Connect.
func fetchOpenIDUserInfo(ctx context.Context, userAccessToken string) (*OpenIDUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, slackAPIBaseURL+"/openid.connect.userInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userAccessToken)

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call API: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var userInfo OpenIDUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !userInfo.OK {
		return nil, fmt.Errorf("%w: %s", ErrSlackAPI, userInfo.Error)
	}

	return &userInfo, nil
}

// HandleCallback processes the OAuth callback from Slack.
func (s *SlackOAuthService) HandleCallback(ctx context.Context, code string) (*SlackOAuthResult, error) {
	// Exchange code for tokens
	// Slack requires the same redirect_uri that was used in the authorization request
	oauthResp, err := exchangeCode(
		ctx,
		s.cfg.Slack.ClientID,
		s.cfg.Slack.ClientSecret,
		code,
		s.getCallbackURL(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSlackTokenExchange, err)
	}

	// Fetch user info via OpenID Connect
	userInfo, err := fetchOpenIDUserInfo(ctx, oauthResp.AuthedUser.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrSlackOAuthFailed, err)
	}

	// Validate email
	if userInfo.Email == "" {
		return nil, ErrEmailNotVerified
	}

	if !userInfo.EmailVerified {
		return nil, ErrEmailNotVerified
	}

	// Find or create organization by Slack Team ID
	org, err := s.findOrCreateOrganization(ctx, oauthResp.Team.ID, oauthResp.Team.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create organization: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, oauthResp.Team.ID, oauthResp.Team.Name)
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

	return &SlackOAuthResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
	}, nil
}

// getCallbackURL returns the OAuth callback URL for this application.
func (s *SlackOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/slack/callback"
}

// findOrCreateOrganization finds or creates an org linked to the Slack team.
func (s *SlackOAuthService) findOrCreateOrganization(
	ctx context.Context, teamID, teamName string,
) (*models.Organization, error) {
	// Check organization_providers table for existing Slack team link
	orgProvider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)
	if err == nil && orgProvider != nil {
		return s.db.GetOrganization(ctx, orgProvider.OrganizationUID)
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get organization provider: %w", err)
	}

	// Create new organization
	slug := s.generateUniqueSlug(ctx, teamName)
	org := models.NewOrganization(slug, teamName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create organization provider to link org to Slack team
	orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, teamID)
	orgProvider.ProviderName = teamName

	if err := s.db.CreateOrganizationProvider(ctx, orgProvider); err != nil {
		return nil, fmt.Errorf("failed to create organization provider: %w", err)
	}

	return org, nil
}

// generateUniqueSlug generates a unique organization slug from a team name.
func (s *SlackOAuthService) generateUniqueSlug(ctx context.Context, teamName string) string {
	// Normalize: lowercase and replace spaces with hyphens
	base := strings.ToLower(teamName)
	base = strings.ReplaceAll(base, " ", "-")

	// Filter: keep only [a-z0-9-]
	var filtered strings.Builder

	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			filtered.WriteRune(r)
		}
	}

	base = filtered.String()

	// Trim: remove leading/trailing hyphens, collapse multiple hyphens
	base = strings.Trim(base, "-")

	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}

	// Ensure minimum length
	if len(base) < 3 {
		base = "org"
	}

	// Ensure maximum length
	if len(base) > 20 {
		base = base[:20]
	}

	// Trim trailing hyphens again
	base = strings.TrimRight(base, "-")

	// Check uniqueness, append number if needed
	slug := base
	suffix := 2

	for {
		_, err := s.db.GetOrganizationBySlug(ctx, slug)
		if err != nil {
			// Slug is available (not found)
			return slug
		}

		slug = fmt.Sprintf("%s%d", base, suffix)
		suffix++
	}
}

// findOrCreateUser finds or creates user by Slack identity.
func (s *SlackOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *OpenIDUserInfo, teamID, teamName string,
) (*models.User, error) {
	// Check by Slack user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, userInfo.Sub)
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
		user.AvatarURL = userInfo.Picture

		if userInfo.EmailVerified {
			now := time.Now()
			user.EmailVerifiedAt = &now
		}

		if err := s.db.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link Slack provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeSlack, userInfo.Sub)
		provider.Metadata = models.JSONMap{
			"team_id":   teamID,
			"team_name": teamName,
		}

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// ensureMembership ensures user is a member of the organization.
func (s *SlackOAuthService) ensureMembership(
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
