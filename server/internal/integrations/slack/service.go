package slack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
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
}

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
func (s *Service) GetConnectionByTeamID(ctx context.Context, teamID string) (*models.IntegrationConnection, error) {
	conn, err := s.db.GetIntegrationConnectionByProperty(
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

// HandleOAuthCallback handles the OAuth callback from Slack.
// It creates/updates the integration connection and also creates user and organization if needed.
func (s *Service) HandleOAuthCallback(ctx context.Context, code, _ string) (*OAuthResult, error) {
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

	// Find or create organization by Slack Team ID
	org, err := s.findOrCreateOrganizationByTeamID(ctx, oauthResp.Team.ID, oauthResp.Team.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create organization: %w", err)
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, oauthResp.Team.ID, oauthResp.Team.Name)
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
	}, nil
}

// createOrUpdateConnection creates or updates an integration connection for the Slack team.
func (s *Service) createOrUpdateConnection(
	ctx context.Context, orgUID string, oauthResp *OAuthResponse,
) (string, error) {
	// Check if a connection already exists for this team
	existingConn, err := s.db.GetIntegrationConnectionByProperty(
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
		update := &models.IntegrationConnectionUpdate{
			Settings: &settingsMap,
		}
		name := oauthResp.Team.Name
		update.Name = &name

		if err := s.db.UpdateIntegrationConnection(ctx, existingConn.UID, update); err != nil {
			return "", fmt.Errorf("failed to update connection: %w", err)
		}

		return existingConn.UID, nil
	}

	// Create new connection
	conn := models.NewIntegrationConnection(orgUID, models.ConnectionTypeSlack, oauthResp.Team.Name)
	conn.Settings = settingsMap

	if err := s.db.CreateIntegrationConnection(ctx, conn); err != nil {
		return "", fmt.Errorf("failed to create connection: %w", err)
	}

	return conn.UID, nil
}

// findOrCreateOrganizationByTeamID finds an existing organization by Slack Team ID or creates a new one.
func (s *Service) findOrCreateOrganizationByTeamID(
	ctx context.Context, teamID, teamName string,
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
	slug := s.generateUniqueSlug(ctx, teamName)
	org := models.NewOrganization(slug, teamName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create organization provider to link org to Slack team (single source of truth)
	orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeSlack, teamID)
	orgProvider.ProviderName = teamName

	if err := s.db.CreateOrganizationProvider(ctx, orgProvider); err != nil {
		return nil, fmt.Errorf("failed to create organization provider: %w", err)
	}

	slog.InfoContext(ctx, "Created new organization from Slack team",
		"org_uid", org.UID,
		"org_slug", org.Slug,
		"team_id", teamID,
		"team_name", teamName,
	)

	return org, nil
}

// generateUniqueSlug generates a unique organization slug from a team name.
func (s *Service) generateUniqueSlug(ctx context.Context, teamName string) string {
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

	// Fallback if empty
	if base == "" {
		base = "org"
	}

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

// HandleAppUninstalled handles the app_uninstalled event.
func (s *Service) HandleAppUninstalled(ctx context.Context, teamID string) error {
	conn, err := s.db.GetIntegrationConnectionByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Connection already deleted, ignore
			return nil
		}

		return err
	}

	if err := s.db.DeleteIntegrationConnection(ctx, conn.UID); err != nil {
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

// GetOAuthURL returns the OAuth authorization URL.
func (s *Service) GetOAuthURL(orgUID, redirectURI string) string {
	// Bot scopes - what the bot can do in the workspace
	botScopes := []string{
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

	// User scopes - needed to fetch the installing user's details via OpenID Connect
	userScopes := []string{
		"openid",
		"email",
		"profile",
	}

	// Generate a simple nonce (in production, store this for verification)
	nonce := "0" // TODO: Generate proper nonce

	state := orgUID + "_" + nonce

	return fmt.Sprintf(
		"https://slack.com/oauth/v2/authorize?client_id=%s&scope=%s&user_scope=%s&redirect_uri=%s&state=%s",
		s.cfg.Slack.ClientID,
		strings.Join(botScopes, ","),
		strings.Join(userScopes, ","),
		redirectURI,
		state,
	)
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

	update := &models.IntegrationConnectionUpdate{
		Settings: &settingsMap,
	}

	if err := s.db.UpdateIntegrationConnection(ctx, conn.UID, update); err != nil {
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
