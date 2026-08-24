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
	"github.com/fclairamb/solidping/server/internal/orgslug"
)

// Discord OAuth specific errors.
var (
	ErrDiscordTokenExchange = errors.New("discord token exchange failed")
	ErrDiscordAPI           = errors.New("discord API error")
)

const (
	discordOAuthStatePrefix = "oauth_state:discord:"
	discordOAuthStateTTL    = 10 * time.Minute
	discordTokenURL         = "https://discord.com/api/oauth2/token"
	discordAPIBaseURL       = "https://discord.com/api"
)

// DiscordUserInfo represents user info from Discord's /users/@me endpoint.
//
//nolint:tagliatelle // Discord API uses snake_case JSON field names
type DiscordUserInfo struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Discriminator string `json:"discriminator"`
	GlobalName    string `json:"global_name"`
	Email         string `json:"email"`
	Verified      bool   `json:"verified"`
	Avatar        string `json:"avatar"`
}

// AvatarURL returns the Discord CDN URL for the user's avatar.
func (u *DiscordUserInfo) AvatarURL() string {
	if u.Avatar == "" {
		return ""
	}

	return fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png", u.ID, u.Avatar)
}

// DisplayName returns the user's display name, preferring global name over username.
func (u *DiscordUserInfo) DisplayName() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}

	return u.Username
}

// DiscordGuild represents a guild from Discord's /users/@me/guilds endpoint.
type DiscordGuild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// DiscordTokenResponse represents the response from Discord's token exchange.
//
//nolint:tagliatelle // Discord API uses snake_case JSON field names
type DiscordTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// DiscordOAuthResult contains the result of a successful Discord OAuth flow.
type DiscordOAuthResult struct {
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

// DiscordOAuthService handles Discord OAuth authentication logic.
type DiscordOAuthService struct {
	db          db.Service
	cfg         *config.Config
	authService *Service
	httpClient  *http.Client

	// tokenURL / apiBaseURL are the Discord endpoints this service talks to.
	// Fields rather than constants so tests can drive the real HandleCallback
	// against httptest stand-ins (same seam as the Slack connector).
	tokenURL   string
	apiBaseURL string
}

// NewDiscordOAuthService creates a new Discord OAuth service.
func NewDiscordOAuthService(
	dbService db.Service, cfg *config.Config, authService *Service,
) *DiscordOAuthService {
	return &DiscordOAuthService{
		db:          dbService,
		cfg:         cfg,
		authService: authService,
		httpClient:  &http.Client{Timeout: defaultTimeout},
		tokenURL:    discordTokenURL,
		apiBaseURL:  discordAPIBaseURL,
	}
}

// GenerateOAuthState creates a new OAuth state and stores it in the database.
func (s *DiscordOAuthService) GenerateOAuthState(
	ctx context.Context, redirectURI string,
) (string, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	nonce := base64.URLEncoding.EncodeToString(nonceBytes)

	state := OAuthState{
		Nonce:       nonce,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now().Unix(),
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	stateValue := &models.JSONMap{"state": string(stateJSON)}
	ttl := discordOAuthStateTTL

	if err := s.db.SetStateEntry(ctx, nil, discordOAuthStatePrefix+nonce, stateValue, &ttl); err != nil {
		return "", fmt.Errorf("failed to store state: %w", err)
	}

	return nonce, nil
}

// ValidateOAuthState validates and consumes an OAuth state.
func (s *DiscordOAuthService) ValidateOAuthState(
	ctx context.Context, stateParam string,
) (*OAuthState, error) {
	entry, err := s.db.GetStateEntry(ctx, nil, discordOAuthStatePrefix+stateParam)
	if err != nil || entry == nil {
		return nil, ErrInvalidOAuthState
	}

	// Delete state (one-time use)
	_, _ = s.db.DeleteStateEntry(ctx, nil, discordOAuthStatePrefix+stateParam)

	stateJSON, ok := (*entry.Value)["state"].(string)
	if !ok {
		return nil, ErrInvalidOAuthState
	}

	var state OAuthState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, ErrInvalidOAuthState
	}

	if time.Now().Unix()-state.CreatedAt > int64(discordOAuthStateTTL.Seconds()) {
		return nil, ErrInvalidOAuthState
	}

	return &state, nil
}

// HandleCallback processes the OAuth callback from Discord.
func (s *DiscordOAuthService) HandleCallback(
	ctx context.Context, code string,
) (*DiscordOAuthResult, error) {
	// Exchange code for tokens
	tokenResp, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDiscordTokenExchange, err)
	}

	// Fetch user info
	userInfo, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrDiscordAPI, err)
	}

	// Validate email
	if userInfo.Email == "" || !userInfo.Verified {
		return nil, ErrEmailNotVerified
	}

	// Fetch user guilds to find/create an organization
	guilds, err := s.fetchUserGuilds(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch guilds: %w", ErrDiscordAPI, err)
	}

	// Use the first guild as the organization, or create a personal one
	var org *models.Organization

	if len(guilds) > 0 {
		org, err = s.findOrCreateOrganization(ctx, guilds[0].ID, guilds[0].Name)
	} else {
		// No guilds, create personal org from username
		org, err = s.findOrCreateOrganization(ctx, "discord-user-"+userInfo.ID, userInfo.DisplayName())
	}

	if err != nil {
		return nil, fmt.Errorf("failed to find/create organization: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to find/create user: %w", err)
	}

	// Admission policy + session minting, shared by every connector
	// (see Service.JoinOrgViaLogin). A user the org does not admit gets
	// login.Pending and an org-less session instead of a membership.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodDiscord))
	if err != nil {
		return nil, err
	}

	return &DiscordOAuthResult{
		AccessToken:  login.AccessToken,
		RefreshToken: login.RefreshToken,
		ExpiresIn:    login.ExpiresIn,
		OrgSlug:      org.Slug,
		UserUID:      user.UID,
		Pending:      login.Pending,
	}, nil
}

// exchangeCode exchanges an authorization code for tokens with Discord.
func (s *DiscordOAuthService) exchangeCode(
	ctx context.Context, code string,
) (*DiscordTokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", s.cfg.Discord.ClientID)
	data.Set("client_secret", s.cfg.Discord.ClientSecret)
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", s.getCallbackURL())

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, s.tokenURL, strings.NewReader(data.Encode()),
	)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrDiscordAPI, resp.StatusCode, string(body))
	}

	var tokenResp DiscordTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &tokenResp, nil
}

// fetchUserProfile fetches user profile from Discord's /users/@me endpoint.
func (s *DiscordOAuthService) fetchUserProfile(
	ctx context.Context, accessToken string,
) (*DiscordUserInfo, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, s.apiBaseURL+"/users/@me", nil,
	)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrDiscordAPI, resp.StatusCode)
	}

	var userInfo DiscordUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &userInfo, nil
}

// fetchUserGuilds fetches the user's guilds from Discord's /users/@me/guilds endpoint.
func (s *DiscordOAuthService) fetchUserGuilds(
	ctx context.Context, accessToken string,
) ([]DiscordGuild, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, s.apiBaseURL+"/users/@me/guilds", nil,
	)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrDiscordAPI, resp.StatusCode)
	}

	var guilds []DiscordGuild
	if err := json.Unmarshal(body, &guilds); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return guilds, nil
}

// findOrCreateOrganization finds or creates an org linked to the Discord guild.
func (s *DiscordOAuthService) findOrCreateOrganization(
	ctx context.Context, guildID, guildName string,
) (*models.Organization, error) {
	// Check organization_providers table for existing Discord guild link
	orgProvider, err := s.db.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeDiscord, guildID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get organization provider: %w", err)
	}

	if err == nil && orgProvider != nil {
		// A link pointing at a soft-deleted org is stale: it is cleared and we
		// fall through to the create path rather than failing this login (and
		// every later one) forever. See resolveLinkedOrganization.
		org, resolveErr := resolveLinkedOrganization(
			ctx, s.db, models.ProviderTypeDiscord, guildID, orgProvider,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if org != nil {
			return org, nil
		}
	}

	// Create new organization. Discord has no workspace-subdomain equivalent,
	// so the guild name is the only slug source. The shared generator applies
	// the same normalization/collision rules as the Slack flows.
	slug := orgslug.GenerateUnique(ctx, s.db, guildName)
	org := models.NewOrganization(slug, guildName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create organization provider to link org to Discord guild
	orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeDiscord, guildID)
	orgProvider.ProviderName = guildName

	if err := s.db.CreateOrganizationProvider(ctx, orgProvider); err != nil {
		return nil, fmt.Errorf("failed to create organization provider: %w", err)
	}

	return org, nil
}

// findOrCreateUser finds or creates a user by Discord identity.
func (s *DiscordOAuthService) findOrCreateUser(
	ctx context.Context, userInfo *DiscordUserInfo,
) (*models.User, error) {
	// Check by Discord user ID first (via user_providers)
	provider, err := s.db.GetUserProviderByProviderID(
		ctx, models.ProviderTypeDiscord, userInfo.ID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get user provider: %w", err)
	}

	if err == nil && provider != nil {
		user, resolveErr := resolveLinkedUser(
			ctx, s.db, models.ProviderTypeDiscord, userInfo.ID, provider,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}

		if user != nil {
			return user, nil
		}

		// Stale link cleared — fall through to the email lookup / create path,
		// and re-link at the end (provider == nil drives that below).
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
		user.Name = userInfo.DisplayName()
		user.AvatarURL = userInfo.AvatarURL()

		if userInfo.Verified {
			now := time.Now()
			user.EmailVerifiedAt = &now
		}

		// Routed through the package's single account-creation chokepoint so
		// the user_signed_up product event fires for SSO signups too.
		if err := createUserAndCapture(ctx, s.db, user, signupMethodDiscord); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link Discord provider if not already linked
	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeDiscord, userInfo.ID)

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}
	}

	return user, nil
}

// getCallbackURL returns the OAuth callback URL for Discord.
func (s *DiscordOAuthService) getCallbackURL() string {
	return s.cfg.Server.BaseURL + "/api/v1/auth/discord/callback"
}
