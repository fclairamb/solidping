package slack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
	"github.com/fclairamb/solidping/server/internal/orgslug"
)

// IncidentService defines the interface for incident operations needed by Slack integration.
// This interface is implemented by handlers/incidents.Service via an adapter.
type IncidentService interface {
	// AcknowledgeIncidentFromSlack marks an incident as acknowledged via Slack.
	AcknowledgeIncidentFromSlack(
		ctx context.Context, orgUID, incidentUID, slackUserID, slackUsername string,
	) (*models.Incident, error)
	// GetIncidentByUID gets an incident by UID.
	GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error)
	// GetCheckByUID gets a check by UID.
	GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
}

var (
	// ErrConnectionNotFound is returned when a connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrNotSlackChannel is returned when the channel is not of type slack.
	ErrNotSlackChannel = errors.New("channel is not of type slack")
	// ErrSlackNotConnected is returned when a Slack channel has no bot token
	// (e.g. a manually-created stub). Such a channel must be (re-)connected via
	// the OAuth install flow before its destinations can be listed.
	ErrSlackNotConnected = errors.New("slack channel has no bot token — install via OAuth")
	// ErrInvalidState is returned when the OAuth state is invalid.
	ErrInvalidState = errors.New("invalid OAuth state")
	// ErrOAuthFailed is returned when OAuth exchange fails.
	ErrOAuthFailed = errors.New("OAuth exchange failed")
	// ErrEmailRequired is returned when the user has no email in their Slack profile.
	ErrEmailRequired = errors.New("email required in Slack profile")
)

// OAuthResult contains the result of a successful OAuth callback.
type OAuthResult struct {
	ConnectionUID string
	AccessToken   string
	RefreshToken  string
	OrgSlug       string
	UserUID       string
}

// installStateKind / installStateTTL govern the bot-install OAuth flow's
// CSRF-state lifetime. exchangeStateKind backs the post-callback session
// handoff (60s window between the redirect and the dashboard's exchange
// call).
const (
	installStateKind  = "slack-install"
	installStateTTL   = 10 * time.Minute
	exchangeStateKind = "slack-exchange"
	exchangeStateTTL  = 60 * time.Second
)

// payloadKey* are the keys used inside the exchange-state Payload map. They
// are also the JSON field names returned by the exchange endpoint.
const (
	payloadKeyAccessToken  = "accessToken"
	payloadKeyRefreshToken = "refreshToken"
	payloadKeyOrgSlug      = "orgSlug"
	payloadKeyUserUID      = "userUID"
	payloadKeySource       = "source"
)

// slackBotScopes / slackUserScopes are the scopes requested during install.
// Bot scopes drive the integration's runtime; user scopes drive the OpenID
// Connect lookup that identifies the installing user.
//
//nolint:gochecknoglobals // package-level constant scope lists
var (
	slackBotScopes = []string{
		"chat:write",
		"chat:write.public",
		"channels:read",
		"groups:read",
		"users:read",
		"users:read.email",
		"team:read",
		"commands",
		"app_mentions:read",
		"reactions:write",
		"links:read",
	}
	slackUserScopes = []string{
		"openid",
		"email",
		"profile",
	}
)

// Service provides business logic for Slack integration.
type Service struct {
	db               db.Service
	cfg              *config.Config
	authService      *auth.Service
	checksService    *checks.Service
	incidentsService IncidentService
}

// NewService creates a new Slack integration service.
func NewService(
	dbService db.Service,
	cfg *config.Config,
	authService *auth.Service,
	checksService *checks.Service,
	incidentsService IncidentService,
) *Service {
	return &Service{
		db:               dbService,
		cfg:              cfg,
		authService:      authService,
		checksService:    checksService,
		incidentsService: incidentsService,
	}
}

// GetConnectionByTeamID retrieves a Slack connection by team ID.
func (s *Service) GetConnectionByTeamID(ctx context.Context, teamID string) (*models.Channel, error) {
	conn, err := s.db.GetChannelByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	return conn, nil
}

// BuildInstallURL mints a fresh CSRF state and returns the Slack OAuth
// authorization URL the user should be redirected to. `source` (when set)
// is stashed in the state payload for install-source analytics on the
// callback side.
func (s *Service) BuildInstallURL(ctx context.Context, source string) (string, error) {
	payload := map[string]any{}
	if source != "" {
		payload[payloadKeySource] = source
	}

	nonce, err := oauthstate.Generate(ctx, s.db, installStateKind, payload, installStateTTL)
	if err != nil {
		return "", fmt.Errorf("generate install state: %w", err)
	}

	redirectURI := s.cfg.Server.BaseURL + "/api/v1/integrations/slack/oauth"

	params := url.Values{}
	params.Set("client_id", s.cfg.Slack.ClientID)
	params.Set("scope", strings.Join(slackBotScopes, ","))
	params.Set("user_scope", strings.Join(slackUserScopes, ","))
	params.Set("redirect_uri", redirectURI)
	params.Set("state", nonce)

	return "https://slack.com/oauth/v2/authorize?" + params.Encode(), nil
}

// IssueExchangeCode persists a single-use code that the dashboard will
// trade in (server-to-server) for the freshly minted access/refresh tokens.
// The 60-second TTL is intentionally tight — the dashboard hits the
// exchange endpoint immediately after the post-install redirect.
func (s *Service) IssueExchangeCode(ctx context.Context, result *OAuthResult) (string, error) {
	payload := map[string]any{
		payloadKeyAccessToken:  result.AccessToken,
		payloadKeyRefreshToken: result.RefreshToken,
		payloadKeyOrgSlug:      result.OrgSlug,
		payloadKeyUserUID:      result.UserUID,
	}

	code, err := oauthstate.Generate(ctx, s.db, exchangeStateKind, payload, exchangeStateTTL)
	if err != nil {
		return "", fmt.Errorf("issue exchange code: %w", err)
	}

	return code, nil
}

// HandleOAuthCallback handles the OAuth callback from Slack.
// It validates the CSRF state up front, then creates/updates the integration
// connection and creates user and organization if needed.
func (s *Service) HandleOAuthCallback(ctx context.Context, code, state string) (*OAuthResult, error) {
	if _, err := oauthstate.Validate(ctx, s.db, installStateKind, state); err != nil {
		return nil, ErrInvalidState
	}

	// Exchange code for access token
	oauthResp, err := ExchangeCode(
		ctx,
		s.cfg.Slack.ClientID,
		s.cfg.Slack.ClientSecret,
		code,
		"", // redirect_uri is optional for token exchange
	)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to exchange OAuth code", "error", err)

		return nil, fmt.Errorf("%w: %w", ErrOAuthFailed, err)
	}

	// Fetch user info via OpenID Connect using the user token
	userInfo, err := FetchOpenIDUserInfo(ctx, oauthResp.AuthedUser.AccessToken)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"Failed to fetch user info from Slack via OpenID Connect",
			"error", err,
			"user_id", oauthResp.AuthedUser.ID,
		)

		return nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrOAuthFailed, err)
	}

	// Validate email is present
	if userInfo.Email == "" {
		return nil, ErrEmailRequired
	}

	// Find or create organization from the Slack workspace identity.
	org, orgName, err := s.resolveOrganization(ctx, oauthResp, userInfo)
	if err != nil {
		return nil, err
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, oauthResp.Team.ID, orgName)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create user: %w", err)
	}

	// Ensure user is a member of the organization
	member, err := s.ensureOrganizationMembership(ctx, org.UID, user.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure organization membership: %w", err)
	}

	// Create or update the integration connection
	connUID, err := s.createOrUpdateConnection(ctx, org.UID, oauthResp)
	if err != nil {
		return nil, err
	}

	// Generate authentication tokens
	tokens, err := s.authService.GenerateTokensForOAuth(ctx, user, org, string(member.Role))
	if err != nil {
		return nil, fmt.Errorf("failed to generate auth tokens: %w", err)
	}

	slog.InfoContext(ctx, "Slack OAuth completed successfully",
		"org_uid", org.UID,
		"org_slug", org.Slug,
		"user_uid", user.UID,
		"user_email", user.Email,
		"team_id", oauthResp.Team.ID,
		"team_name", oauthResp.Team.Name,
	)

	return &OAuthResult{
		ConnectionUID: connUID,
		AccessToken:   tokens.AccessToken,
		RefreshToken:  tokens.RefreshToken,
		OrgSlug:       org.Slug,
		UserUID:       user.UID,
	}, nil
}

// resolveOrganization derives the org display name and slug candidates from
// the Slack workspace identity, then finds or creates the organization. It
// returns the organization and the resolved display name (also used for the
// user-provider metadata). Slug candidates are tried in priority order:
// workspace subdomain → workspace team_name → OAuth Team.Name → "org".
func (s *Service) resolveOrganization(
	ctx context.Context, oauthResp *OAuthResponse, userInfo *OpenIDUserInfo,
) (*models.Organization, string, error) {
	// Prefer the workspace team_name claim for the org display name; fall
	// back to the OAuth response's Team.Name.
	orgName := userInfo.SlackTeamName
	if orgName == "" {
		orgName = oauthResp.Team.Name
	}

	org, err := s.findOrCreateOrganizationByTeamID(
		ctx,
		oauthResp.Team.ID,
		orgName,
		userInfo.SlackTeamDomain,
		userInfo.SlackTeamName,
		oauthResp.Team.Name,
	)
	if err != nil {
		return nil, "", fmt.Errorf("failed to find or create organization: %w", err)
	}

	return org, orgName, nil
}

// createOrUpdateConnection creates or updates an integration connection for the Slack team.
func (s *Service) createOrUpdateConnection(
	ctx context.Context, orgUID string, oauthResp *OAuthResponse,
) (string, error) {
	// Check if a connection already exists for this team
	existingConn, err := s.db.GetChannelByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", oauthResp.Team.ID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Create Slack settings
	settings := &models.SlackSettings{
		TeamID:            oauthResp.Team.ID,
		TeamName:          oauthResp.Team.Name,
		BotUserID:         oauthResp.BotUserID,
		AccessToken:       oauthResp.AccessToken,
		InstalledByUserID: oauthResp.AuthedUser.ID,
		Scopes:            strings.Split(oauthResp.Scope, ","),
	}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return "", fmt.Errorf("failed to convert settings: %w", err)
	}

	if existingConn != nil {
		// Update existing connection
		update := &models.ChannelUpdate{
			Settings: &settingsMap,
		}
		name := oauthResp.Team.Name
		update.Name = &name

		if err := s.db.UpdateChannel(ctx, existingConn.UID, update); err != nil {
			return "", fmt.Errorf("failed to update connection: %w", err)
		}

		return existingConn.UID, nil
	}

	// Create new connection
	conn := models.NewChannel(orgUID, models.ConnectionTypeSlack, oauthResp.Team.Name)
	conn.Settings = settingsMap

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return "", fmt.Errorf("failed to create connection: %w", err)
	}

	return conn.UID, nil
}

// findOrCreateOrganizationByTeamID finds an existing organization by Slack Team
// ID or creates a new one. orgName is the display name; slugCandidates are
// tried in priority order by the shared slug generator.
func (s *Service) findOrCreateOrganizationByTeamID(
	ctx context.Context, teamID, orgName string, slugCandidates ...string,
) (*models.Organization, error) {
	// Primary lookup: check organization_providers table (single source of truth)
	orgProvider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)
	if err == nil && orgProvider != nil {
		org, getErr := s.db.GetOrganization(ctx, orgProvider.OrganizationUID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to get organization: %w", getErr)
		}

		return org, nil
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check organization provider: %w", err)
	}

	// Create new organization from Slack team
	slug := orgslug.GenerateUnique(ctx, s.db, slugCandidates...)
	org := models.NewOrganization(slug, orgName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create organization provider to link org to Slack team (single source of truth)
	orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, teamID)
	orgProvider.ProviderName = orgName

	if err := s.db.CreateOrganizationProvider(ctx, orgProvider); err != nil {
		return nil, fmt.Errorf("failed to create organization provider: %w", err)
	}

	slog.InfoContext(ctx, "Created new organization from Slack team",
		"org_uid", org.UID,
		"org_slug", org.Slug,
		"team_id", teamID,
		"team_name", orgName,
	)

	return org, nil
}

// findOrCreateUser finds an existing user by email or creates a new one.
func (s *Service) findOrCreateUser(
	ctx context.Context, userInfo *OpenIDUserInfo, teamID, teamName string,
) (*models.User, error) {
	email := userInfo.Email

	// Check if user already exists by email
	user, err := s.db.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check existing user: %w", err)
	}

	if user == nil {
		user, err = s.createUserFromSlack(ctx, userInfo)
		if err != nil {
			return nil, err
		}
	}

	// Link Slack identity via user_providers if not already linked
	// userInfo.Sub is the Slack user ID (e.g., U013ZGBT0SJ)
	provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, userInfo.Sub)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check user provider: %w", err)
	}

	if provider == nil {
		provider = models.NewUserProvider(user.UID, models.ProviderTypeSlack, userInfo.Sub)
		provider.Metadata = models.JSONMap{
			"team_id":   teamID,
			"team_name": teamName,
		}

		if err := s.db.CreateUserProvider(ctx, provider); err != nil {
			return nil, fmt.Errorf("failed to create user provider: %w", err)
		}

		slog.InfoContext(ctx, "Linked Slack identity to user",
			"user_uid", user.UID,
			"slack_user_id", userInfo.Sub,
		)
	}

	return user, nil
}

// createUserFromSlack creates a new user from Slack OpenID user info.
func (s *Service) createUserFromSlack(ctx context.Context, userInfo *OpenIDUserInfo) (*models.User, error) {
	user := models.NewUser(userInfo.Email)

	// Set name from OpenID profile
	user.Name = userInfo.Name

	// Set avatar URL from OpenID picture
	if userInfo.Picture != "" {
		user.AvatarURL = userInfo.Picture
	}

	// Mark email as verified if Slack confirmed it
	if userInfo.EmailVerified {
		now := time.Now()
		user.EmailVerifiedAt = &now
	}

	if err := s.db.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	slog.InfoContext(ctx, "Created new user from Slack",
		"user_uid", user.UID,
		"user_email", user.Email,
		"slack_user_id", userInfo.Sub,
	)

	return user, nil
}

// ensureOrganizationMembership ensures the user is a member of the organization.
func (s *Service) ensureOrganizationMembership(
	ctx context.Context, orgUID, userUID string,
) (*models.OrganizationMember, error) {
	// Check if user is already a member
	member, err := s.db.GetMemberByUserAndOrg(ctx, userUID, orgUID)
	if err == nil {
		return member, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check membership: %w", err)
	}

	// Determine role: first user becomes admin, others are regular users
	role := models.MemberRoleUser

	members, err := s.db.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("failed to list members: %w", err)
	}

	if len(members) == 0 {
		role = models.MemberRoleAdmin
	}

	// Add user to organization
	now := time.Now()
	member = models.NewOrganizationMember(orgUID, userUID, role)
	member.JoinedAt = &now

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to create membership: %w", err)
	}

	slog.InfoContext(ctx, "Added user to organization",
		"org_uid", orgUID,
		"user_uid", userUID,
		"role", role,
	)

	return member, nil
}

// CountInstalledTeams returns the number of distinct organizations that
// currently have a Slack integration connection. Used by the Socket Mode
// supervisor's status snapshot. Best-effort: returns (0, err) on failure.
func (s *Service) CountInstalledTeams(ctx context.Context) (int, error) {
	slackType := models.ConnectionTypeSlack

	channels, err := s.db.ListChannels(ctx, &models.ListChannelsFilter{
		Type: &slackType,
	})
	if err != nil {
		return 0, fmt.Errorf("list slack channels: %w", err)
	}

	return len(channels), nil
}

// HandleAppUninstalled handles the app_uninstalled event.
func (s *Service) HandleAppUninstalled(ctx context.Context, teamID string) error {
	conn, err := s.db.GetChannelByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Connection already deleted, ignore
			return nil
		}

		return err
	}

	if err := s.db.DeleteChannel(ctx, conn.UID); err != nil {
		return fmt.Errorf("failed to delete connection: %w", err)
	}

	slog.InfoContext(ctx, "Deleted Slack connection due to app_uninstalled",
		"team_id", teamID,
		"connection_uid", conn.UID,
	)

	return nil
}

// GetClient returns a Slack client for a team.
func (s *Service) GetClient(ctx context.Context, teamID string) (*Client, error) {
	conn, err := s.GetConnectionByTeamID(ctx, teamID)
	if err != nil {
		return nil, err
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, fmt.Errorf("failed to parse settings: %w", err)
	}

	return NewClient(settings.AccessToken), nil
}

// CreateCheckResult contains the result of creating a check via Slack.
type CreateCheckResult struct {
	Slug string
	Name string
}

// CreateCheck creates a new HTTP check for the organization associated with the team ID.
func (s *Service) CreateCheck(ctx context.Context, teamID, url string) (*CreateCheckResult, error) {
	return s.CreateCheckWithOptions(ctx, teamID, url, "", "")
}

// CreateCheckWithOptions creates a new HTTP check with optional slug and period.
func (s *Service) CreateCheckWithOptions(
	ctx context.Context, teamID, url, slug, period string,
) (*CreateCheckResult, error) {
	// Get the connection to find the organization
	conn, err := s.GetConnectionByTeamID(ctx, teamID)
	if err != nil {
		return nil, err
	}

	// Get the organization slug
	org, err := s.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}

	// Build the request
	req := checks.CreateCheckRequest{
		Type: "http",
		Config: map[string]any{
			"url": url,
		},
	}

	// Set optional fields if provided
	if slug != "" {
		req.Slug = slug
	}
	if period != "" {
		req.Period = &period
	}

	// Create the check using the checks service
	checkResp, err := s.checksService.CreateCheck(ctx, org.Slug, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create check: %w", err)
	}

	result := &CreateCheckResult{}
	if checkResp.Slug != nil {
		result.Slug = *checkResp.Slug
	}
	if checkResp.Name != nil {
		result.Name = *checkResp.Name
	}

	return result, nil
}

// SetDefaultChannel sets the default channel for Slack notifications.
// If sendWelcome is true, sends a welcome message to the channel.
//
//nolint:funlen // Complex due to channel lookup, settings update, and optional welcome message.
func (s *Service) SetDefaultChannel(ctx context.Context, teamID, channelID string, sendWelcome bool) error {
	conn, err := s.GetConnectionByTeamID(ctx, teamID)
	if err != nil {
		return err
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	// Get channel name for display (best effort)
	client := NewClient(settings.AccessToken)
	channelName := ""

	channels, err := client.ListChannels(ctx)
	if err == nil {
		for i := range channels {
			if channels[i].ID == channelID {
				channelName = "#" + channels[i].Name

				break
			}
		}
	}

	// Update settings
	settings.ChannelID = channelID
	settings.ChannelName = channelName

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return fmt.Errorf("failed to convert settings: %w", err)
	}

	update := &models.ChannelUpdate{
		Settings: &settingsMap,
	}

	if err := s.db.UpdateChannel(ctx, conn.UID, update); err != nil {
		return fmt.Errorf("failed to update connection: %w", err)
	}

	slog.InfoContext(ctx, "Set default Slack channel",
		"team_id", teamID,
		"channel_id", channelID,
		"channel_name", channelName,
	)

	// Send welcome message if requested
	if sendWelcome {
		welcomeMsg := &MessageResponse{
			Text: "SolidPing is ready!",
			Blocks: []Block{
				{
					Type: BlockTypeSection,
					Text: &Text{
						Type: BlockTypeMrkdwn,
						Text: ":wave: *SolidPing is ready!*\n\nI'll send incident notifications here by default.",
					},
				},
				{
					Type: BlockTypeContext,
					Elements: []any{
						ContextElement{
							Type: BlockTypeMrkdwn,
							Text: "Change the default channel anytime with `@solidping config default-channel #other-channel`",
						},
					},
				},
			},
		}

		if _, err := client.PostMessage(ctx, PostMessageOptions{
			Channel: channelID,
			Message: welcomeMsg,
		}); err != nil {
			slog.WarnContext(ctx, "Failed to send welcome message",
				"channel_id", channelID,
				"error", err,
			)
			// Don't fail the operation if welcome message fails
		}
	}

	return nil
}

// SlackChannel is the DTO returned by GetDestinations for a channel entry.
// The Slack prefix disambiguates from Channel (API response type) in the same package.
type SlackChannel struct { //nolint:revive // prefix is intentional disambiguation
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"isPrivate"`
	IsMember  bool   `json:"isMember"`
}

// SlackDestinationUser is the DTO returned by GetDestinations for a user entry.
// The Slack prefix disambiguates from other user types in the same package.
type SlackDestinationUser struct { //nolint:revive // prefix is intentional disambiguation
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"realName"`
}

// SlackDestinationsResponse is returned by GetDestinations.
// The Slack prefix disambiguates from other response types in the same package.
type SlackDestinationsResponse struct { //nolint:revive // prefix is intentional disambiguation
	Channels []SlackChannel         `json:"channels"`
	Users    []SlackDestinationUser `json:"users"`
}

// GetDestinations fetches the list of Slack channels and users available
// as notification destinations for the given channel (integration connection).
//
//nolint:funlen // resolving org, channel, and fetching both channels and users is inherently verbose
func (s *Service) GetDestinations(
	ctx context.Context, orgSlug, channelUID string,
) (*SlackDestinationsResponse, error) {
	// Resolve org slug to UID.
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, fmt.Errorf("get organization: %w", err)
	}

	// Load the channel row, asserting it belongs to this org and is of type slack.
	conn, err := s.db.GetChannel(ctx, channelUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, fmt.Errorf("get channel: %w", err)
	}

	if conn.OrganizationUID != org.UID || conn.DeletedAt != nil {
		return nil, ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeSlack {
		return nil, ErrNotSlackChannel
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, fmt.Errorf("parse slack settings: %w", err)
	}

	// Tokenless stubs (e.g. manually-created channels) have no bot token, so any
	// Slack API call would fail with invalid_auth and surface as a misleading
	// 502. Reject early so the handler can return a clear "not connected" state.
	if settings.AccessToken == "" {
		return nil, ErrSlackNotConnected
	}

	client := NewClient(settings.AccessToken)

	// Fetch channels and users in parallel.
	var (
		rawChannels []Channel
		rawUsers    []SlackUser
	)

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		var fetchErr error
		rawChannels, fetchErr = client.ListChannels(groupCtx)

		return fetchErr
	})

	group.Go(func() error {
		var fetchErr error
		rawUsers, fetchErr = client.ListUsers(groupCtx)

		return fetchErr
	})

	if err := group.Wait(); err != nil {
		return nil, fmt.Errorf("slack API error: %w", err)
	}

	channels := make([]SlackChannel, 0, len(rawChannels))

	for i := range rawChannels {
		channels = append(channels, SlackChannel(rawChannels[i]))
	}

	users := make([]SlackDestinationUser, 0, len(rawUsers))

	for i := range rawUsers {
		u := rawUsers[i]
		users = append(users, SlackDestinationUser{
			ID:       u.ID,
			Name:     u.Name,
			RealName: u.RealName,
		})
	}

	return &SlackDestinationsResponse{
		Channels: channels,
		Users:    users,
	}, nil
}
