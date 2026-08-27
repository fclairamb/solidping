package auth

import (
	"context"
	"database/sql"
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
	"github.com/fclairamb/solidping/server/internal/oauthstate"
	"github.com/fclairamb/solidping/server/internal/orgslug"
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
	// slackSignInStateKind is the oauthstate kind for the Sign-in-with-Slack
	// flow (separate from the bot-install kind so a state minted for one
	// flow cannot be redeemed by the other's callback).
	slackSignInStateKind = "slack-signin"
	oauthStateTTL        = 10 * time.Minute
	slackOAuthURL        = "https://slack.com/api/oauth.v2.access"
	slackAPIBaseURL      = "https://slack.com/api"
	defaultTimeout       = 30 * time.Second

	payloadKeyRedirectURI = "redirectUri"
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
	// SlackTeamName / SlackTeamDomain are workspace-identity custom claims
	// returned by openid.connect.userInfo with the profile scope. TeamDomain
	// is the workspace subdomain (e.g. "acme" for acme.slack.com) and makes
	// an ideal org slug.
	SlackTeamName   string `json:"https://slack.com/team_name"`
	SlackTeamDomain string `json:"https://slack.com/team_domain"`
	Error           string `json:"error,omitempty"`
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
	// ExpiresIn is the access token's lifetime in seconds, so the callback
	// can scope the access_token cookie exactly like every other provider.
	ExpiresIn int
	OrgSlug   string
	UserUID   string
	// Pending is true when the login succeeded but the org did not admit
	// the user: no membership was created, a membership request is awaiting
	// admin approval, and the tokens above are an org-less session.
	Pending bool
}

// SlackOAuthService handles Slack OAuth authentication logic.
type SlackOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service // Reuse existing auth service for token generation

	// oauthURL / userInfoURL are the Slack endpoints this service talks to.
	// Fields rather than constants so tests can drive the real HandleCallback
	// against httptest stand-ins (same seam as the Microsoft connector).
	oauthURL    string
	userInfoURL string
}

// NewSlackOAuthService creates a new Slack OAuth service.
func NewSlackOAuthService(dbService db.Service, cfg *config.Config, authService *Service) *SlackOAuthService {
	return &SlackOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		oauthURL:    slackOAuthURL,
		userInfoURL: slackAPIBaseURL + "/openid.connect.userInfo",
	}
}

// SlackExchangeResult is what the exchange endpoint returns to the
// dashboard. It mirrors the payload stashed by the Slack integration's
// post-callback IssueExchangeCode call.
type SlackExchangeResult struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	// ExpiresIn is the access token's lifetime in seconds, so the dashboard
	// can compute an absolute expiry exactly like every other login path
	// (see auth.slack.complete.tsx) instead of assuming the configured
	// default.
	ExpiresIn int    `json:"expiresIn"`
	OrgSlug   string `json:"orgSlug"`
	UserUID   string `json:"userUid"`
	// ChannelUID is set when the install was triggered from a channel edit page.
	// The dashboard uses it to navigate back to that channel after login.
	ChannelUID string `json:"channelUid,omitempty"`
}

// ExchangeSlackInstallCode validates a single-use code minted by the Slack
// integration's install callback and returns the freshly minted session
// tokens. The code is consumed on success — repeat calls fail.
func (s *SlackOAuthService) ExchangeSlackInstallCode(
	ctx context.Context, code string,
) (*SlackExchangeResult, error) {
	entry, err := oauthstate.Validate(ctx, s.db, "slack-exchange", code)
	if err != nil {
		return nil, ErrInvalidOAuthState
	}

	if entry.Payload == nil {
		return nil, ErrInvalidOAuthState
	}

	access, _ := entry.Payload["accessToken"].(string)
	refresh, _ := entry.Payload["refreshToken"].(string)
	orgSlug, _ := entry.Payload["orgSlug"].(string)
	userUID, _ := entry.Payload["userUID"].(string)

	if access == "" || refresh == "" || orgSlug == "" || userUID == "" {
		return nil, ErrInvalidOAuthState
	}

	channelUID, _ := entry.Payload["channelUid"].(string)
	// Payload round-trips through JSON (oauthstate persists it as JSON text),
	// so the int stored by IssueExchangeCode comes back as float64 — assert
	// accordingly rather than int (which would silently zero via the ,_ discard).
	expiresIn, _ := entry.Payload["expiresIn"].(float64)

	return &SlackExchangeResult{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(expiresIn),
		OrgSlug:      orgSlug,
		UserUID:      userUID,
		ChannelUID:   channelUID,
	}, nil
}

// GenerateOAuthState mints an OAuth state for the Sign-in-with-Slack flow.
// The redirectURI is stashed in the entry payload so the callback can route
// the user back where they came from.
func (s *SlackOAuthService) GenerateOAuthState(ctx context.Context, redirectURI string) (string, error) {
	payload := map[string]any{}
	if redirectURI != "" {
		payload[payloadKeyRedirectURI] = redirectURI
	}

	nonce, err := oauthstate.Generate(ctx, s.db, slackSignInStateKind, payload, oauthStateTTL)
	if err != nil {
		return "", fmt.Errorf("failed to generate oauth state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes a Sign-in-with-Slack OAuth state.
func (s *SlackOAuthService) ValidateOAuthState(ctx context.Context, stateParam string) (*OAuthState, error) {
	entry, err := oauthstate.Validate(ctx, s.db, slackSignInStateKind, stateParam)
	if err != nil {
		return nil, ErrInvalidOAuthState
	}

	state := &OAuthState{
		Nonce:     entry.Nonce,
		CreatedAt: entry.CreatedAt,
	}

	if entry.Payload != nil {
		if rawURI, ok := entry.Payload[payloadKeyRedirectURI].(string); ok {
			state.RedirectURI = rawURI
		}
	}

	return state, nil
}

// exchangeCode exchanges an OAuth code for an access token at endpoint
// (Slack's oauth.v2.access in production, an httptest stand-in in tests).
func exchangeCode(
	ctx context.Context, endpoint, clientID, clientSecret, code, redirectURI string,
) (*OAuthResponse, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)

	if redirectURI != "" {
		data.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
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

// fetchOpenIDUserInfo fetches user info via OpenID Connect at endpoint.
func fetchOpenIDUserInfo(ctx context.Context, endpoint, userAccessToken string) (*OpenIDUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
		s.oauthURL,
		s.cfg.Slack.ClientID,
		s.cfg.Slack.ClientSecret,
		code,
		s.getCallbackURL(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSlackTokenExchange, err)
	}

	// Fetch user info via OpenID Connect
	userInfo, err := fetchOpenIDUserInfo(ctx, s.userInfoURL, oauthResp.AuthedUser.AccessToken)
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

	// Prefer the workspace team_name claim for the org display name; fall
	// back to the OAuth response's Team.Name (often empty in this flow).
	orgName := userInfo.SlackTeamName
	if orgName == "" {
		orgName = oauthResp.Team.Name
	}

	// Find or create organization by Slack Team ID. Slug candidates are tried
	// in priority order: workspace subdomain → workspace team_name →
	// OAuth Team.Name → "org".
	org, err := s.findOrCreateOrganization(
		ctx,
		oauthResp.Team.ID,
		orgName,
		userInfo.SlackTeamDomain,
		userInfo.SlackTeamName,
		oauthResp.Team.Name,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create organization: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, oauthResp.Team.ID, orgName)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	//
	// The team ID comes from the OAuth token exchange we just performed —
	// Slack only completes it for a member of that workspace — so it is
	// handed to the policy as an attestation of workspace membership. The
	// policy still verifies, server-side, that this very org is the one
	// linked to that workspace.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user,
		WithSlackWorkspace(oauthResp.Team.ID), WithLoginMethod(signupMethodSlack))
	if err != nil {
		return nil, err
	}

	return &SlackOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// getCallbackURL returns the OAuth callback URL for this application.
func (s *SlackOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/slack/callback"
}

// findOrCreateOrganization finds or creates an org linked to the Slack team.
// orgName is the display name; slugCandidates are tried in priority order by
// the shared slug generator.
func (s *SlackOAuthService) findOrCreateOrganization(
	ctx context.Context, teamID, orgName string, slugCandidates ...string,
) (*models.Organization, error) {
	// Check organization_providers table for existing Slack team link
	orgProvider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get organization provider: %w", err)
	}

	if err == nil && orgProvider != nil {
		// A link pointing at a soft-deleted org is stale: it is cleared and we
		// fall through to the create path rather than failing this login (and
		// every later one) forever. See ResolveLinkedOrganization.
		org, resolveErr := ResolveLinkedOrganization(
			ctx, s.db, models.ProviderTypeSlack, teamID, orgProvider,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if org != nil {
			return org, nil
		}
	}

	// Create new organization
	slug := orgslug.GenerateUnique(ctx, s.db, slugCandidates...)
	org := models.NewOrganization(slug, orgName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create organization provider to link org to Slack team
	orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, teamID)
	orgProvider.ProviderName = orgName

	if err := s.db.CreateOrganizationProvider(ctx, orgProvider); err != nil {
		return nil, fmt.Errorf("failed to create organization provider: %w", err)
	}

	return org, nil
}

// findOrCreateUser finds or creates user by Slack identity.
func (s *SlackOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *OpenIDUserInfo, teamID, teamName string,
) (*models.User, error) {
	// Check by Slack user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, userInfo.Sub)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		user, resolveErr := ResolveLinkedUser(
			ctx, s.db, models.ProviderTypeSlack, userInfo.Sub, provider,
		)
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
		if err := createUserAndCapture(ctx, s.db, user, signupMethodSlack); err != nil {
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
