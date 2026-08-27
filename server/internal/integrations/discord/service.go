package discord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
	"github.com/fclairamb/solidping/server/internal/orgslug"
	"github.com/fclairamb/solidping/server/internal/support"
)

// IncidentService is the subset of the incidents service the Discord
// integration needs. Declared here (rather than imported) so the dependency
// points one way — handlers/incidents must never import this package.
type IncidentService interface {
	// guildID names the guild the press happened in, so the ack-notice fan-out
	// can skip the message that already shows the acknowledgment.
	AcknowledgeIncidentFromDiscord(
		ctx context.Context, orgUID, incidentUID, discordUserID, discordUsername, guildID string,
	) (*models.Incident, error)
	GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error)
	GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
	AddCommentFromDiscord(
		ctx context.Context, orgUID, incidentUID, text, discordUserID, discordUserName, guildID, messageID string,
	) (*models.Event, error)
	AddCommentFromDiscordCommand(
		ctx context.Context, orgUID, incidentUID, text, discordUserID, discordUserName, guildID string,
	) (*models.Event, error)
}

// Service-level sentinel errors.
var (
	// ErrConnectionNotFound is returned when a connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrNotDiscordChannel is returned when the channel is not of type discord.
	ErrNotDiscordChannel = errors.New("channel is not of type discord")
	// ErrDiscordNotConnected is returned when a Discord integration has no
	// guild (a legacy webhook-only row, or a manually-created stub). Such a
	// channel must be connected through the bot install before its guild
	// channels can be listed.
	ErrDiscordNotConnected = errors.New("discord channel has no guild — install the bot")
	// ErrInvalidState is returned when the OAuth state is invalid.
	ErrInvalidState = errors.New("invalid OAuth state")
	// ErrOAuthFailed is returned when the token exchange fails.
	ErrOAuthFailed = errors.New("OAuth exchange failed")
	// ErrGuildMissing is returned when the install callback carries no guild —
	// the user completed an authorization that did not add the bot anywhere.
	ErrGuildMissing = errors.New("discord install returned no guild")
	// ErrBotNotConfigured is returned when the instance has no Discord bot
	// token, so nothing can be installed.
	ErrBotNotConfigured = errors.New("discord bot is not configured on this instance")
)

// Install-state kinds and lifetimes, mirroring the Slack install flow.
const (
	installStateKind = "discord-install"
	installStateTTL  = 10 * time.Minute
)

// payloadKey* are the keys stored in the install state payload.
const (
	payloadKeyChannelUID = "channelUid"
	payloadKeyInstallOrg = "installOrg"
)

// Bot install scopes.
//
//   - `bot` adds the application to the guild.
//   - `applications.commands` registers the slash commands.
//   - `identify` names the human who installed it, so the settings blob can
//     record installed_by_user_id instead of an anonymous install.
const botInstallScopes = "bot applications.commands identify"

// botPermissions is the permission integer requested at install. It is the
// minimum the bot actually uses — see each bit below — and is composed from
// named constants rather than written as a magic integer so an operator (or a
// reviewer) can audit exactly what the "Add to Server" dialog will ask for.
const botPermissions = permViewChannel |
	permSendMessages |
	permEmbedLinks |
	permReadMessageHistory |
	permManageThreads |
	permCreatePublicThreads |
	permSendMessagesInThreads

// Discord permission bits (see Discord's Permissions reference).
const (
	// permViewChannel — read the destination channel at all.
	permViewChannel = 1 << 10
	// permSendMessages — post the incident message.
	permSendMessages = 1 << 11
	// permEmbedLinks — render the incident card as an embed.
	permEmbedLinks = 1 << 14
	// permReadMessageHistory — required to reply to / thread off a message.
	permReadMessageHistory = 1 << 16
	// permManageThreads — un-archive a thread Discord auto-archived, which is
	// what lets a late resolve message still land in the incident's thread.
	permManageThreads = 1 << 34
	// permCreatePublicThreads — open the incident thread.
	permCreatePublicThreads = 1 << 35
	// permSendMessagesInThreads — post the follow-ups into it.
	permSendMessagesInThreads = 1 << 38
)

// Service provides business logic for the Discord bot integration.
type Service struct {
	db               db.Service
	cfg              *config.Config
	checksService    *checks.Service
	incidentsService IncidentService

	// newBotClient builds the Discord REST client used for outbound calls.
	// Tests override it to point at an httptest fake Discord.
	newBotClient func(token string) *BotClient

	// oauthTokenURL is the code→token endpoint. A field rather than a constant
	// so tests can drive the real callback against a stand-in.
	oauthTokenURL string
	// apiBaseURL is where identity lookups during the install go.
	apiBaseURL string

	// support is the instance support inbox that captures DMs to the bot. Nil
	// disables DM capture, which is the behavior that predates the feature.
	support *support.Service
}

// SetSupport wires the support inbox after construction. Late injection so this
// package stays importable by the support wiring without an import cycle.
func (s *Service) SetSupport(svc *support.Service) {
	s.support = svc
}

// NewService creates a new Discord integration service.
func NewService(
	dbService db.Service,
	cfg *config.Config,
	checksService *checks.Service,
	incidentsService IncidentService,
) *Service {
	return &Service{
		db:               dbService,
		cfg:              cfg,
		checksService:    checksService,
		incidentsService: incidentsService,
		newBotClient:     NewBotClient,
		oauthTokenURL:    OAuthTokenURL,
		apiBaseURL:       APIBaseURL,
	}
}

// botToken returns the instance-level bot token.
func (s *Service) botToken() string {
	if s.cfg == nil {
		return ""
	}

	return s.cfg.Discord.BotToken
}

// GetConnectionByGuildID resolves which Discord integration inbound traffic for
// a guild operates on.
//
// Same two-step rule as Slack's GetConnectionByTeamID, and for the same reason:
// a guild may be connected to several orgs (one integration row per org), so
// exactly one place must decide which is authoritative.
//
//  1. Home org: the org recorded in organization_providers for
//     (discord, guild_id) — the mapping "Sign in with Discord" already writes.
//     If that org has its own connection, use it.
//  2. Deterministic fallback: the oldest connection for the guild across all
//     orgs, with a warning, since routing is then ambiguous.
func (s *Service) GetConnectionByGuildID(ctx context.Context, guildID string) (*models.Integration, error) {
	if provider, err := s.db.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeDiscord, guildID,
	); err == nil && provider != nil {
		conn, connErr := s.db.GetChannelByPropertyForOrg(
			ctx, provider.OrganizationUID, string(models.ConnectionTypeDiscord), "guild_id", guildID,
		)
		if connErr == nil {
			return conn, nil
		}

		if !errors.Is(connErr, sql.ErrNoRows) {
			return nil, connErr
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeDiscord), "guild_id", guildID,
	)
	if err != nil {
		return nil, err
	}

	if len(conns) == 0 {
		return nil, ErrConnectionNotFound
	}

	if len(conns) > 1 {
		slog.WarnContext(ctx, "Discord guild resolves to multiple connections with no home org — "+
			"routing to the oldest one; ambiguous until a home org is recorded",
			"guild_id", guildID,
			"connection_count", len(conns),
			"resolved_connection_uid", conns[0].UID,
			"resolved_org_uid", conns[0].OrganizationUID,
		)
	}

	return conns[0], nil
}

// BuildInstallURL mints a CSRF state and returns the Discord bot authorization
// URL. channelUID/orgSlug are stashed in the state so the callback can update
// the specific integration the install was triggered from.
func (s *Service) BuildInstallURL(ctx context.Context, channelUID, orgSlug string) (string, error) {
	if s.cfg == nil || s.cfg.Discord.ClientID == "" {
		return "", ErrBotNotConfigured
	}

	payload := map[string]any{}
	if channelUID != "" {
		payload[payloadKeyChannelUID] = channelUID
	}

	if orgSlug != "" {
		payload[payloadKeyInstallOrg] = orgSlug
	}

	nonce, err := oauthstate.Generate(ctx, s.db, installStateKind, payload, installStateTTL)
	if err != nil {
		return "", fmt.Errorf("generate install state: %w", err)
	}

	params := url.Values{}
	params.Set("client_id", s.cfg.Discord.ClientID)
	params.Set("scope", botInstallScopes)
	params.Set("permissions", strconv.Itoa(botPermissions))
	params.Set("response_type", "code")
	params.Set("redirect_uri", s.installRedirectURI())
	params.Set("state", nonce)

	return OAuthAuthorizeURL + "?" + params.Encode(), nil
}

// installRedirectURI is the callback Discord returns the install to.
func (s *Service) installRedirectURI() string {
	base := ""
	if s.cfg != nil {
		base = s.cfg.Server.BaseURL
	}

	return base + "/api/v1/integrations/discord/oauth"
}

// BuildOrgInstallURL is the authenticated, org-scoped counterpart to
// BuildInstallURL: orgUID/orgSlug come from the verified route context, never
// from client input, so install targeting cannot be forged. When channelUID is
// set it must exist, be a Discord integration, and belong to orgUID.
func (s *Service) BuildOrgInstallURL(ctx context.Context, orgUID, orgSlug, channelUID string) (string, error) {
	if channelUID != "" {
		conn, err := s.db.GetChannel(ctx, channelUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrConnectionNotFound
			}

			return "", fmt.Errorf("get channel: %w", err)
		}

		if conn.OrganizationUID != orgUID ||
			conn.Type != models.ConnectionTypeDiscord ||
			conn.DeletedAt != nil {
			return "", ErrConnectionNotFound
		}
	}

	return s.BuildInstallURL(ctx, channelUID, orgSlug)
}

// InstallResult is what a completed bot install produced.
type InstallResult struct {
	ConnectionUID string
	OrgSlug       string
	GuildID       string
	GuildName     string
}

// HandleOAuthCallback completes a bot install: validates the CSRF state,
// exchanges the code, resolves the organization and creates/updates the
// integration connection.
func (s *Service) HandleOAuthCallback(ctx context.Context, code, state string) (*InstallResult, error) {
	entry, err := oauthstate.Validate(ctx, s.db, installStateKind, state)
	if err != nil {
		return nil, ErrInvalidState
	}

	var targetChannelUID, targetOrgSlug string
	if entry.Payload != nil {
		targetChannelUID, _ = entry.Payload[payloadKeyChannelUID].(string)
		targetOrgSlug, _ = entry.Payload[payloadKeyInstallOrg].(string)
	}

	tokenResp, err := ExchangeCode(
		ctx, s.oauthTokenURL,
		s.cfg.Discord.ClientID, s.cfg.Discord.ClientSecret,
		code, s.installRedirectURI(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOAuthFailed, err)
	}

	if tokenResp.Guild == nil || tokenResp.Guild.ID == "" {
		return nil, ErrGuildMissing
	}

	guild := tokenResp.Guild

	org, err := s.resolveOrganization(ctx, targetOrgSlug, guild)
	if err != nil {
		return nil, err
	}

	// Q2: the bot install lands in the SAME organization_providers mapping
	// Discord auth writes. linkGuildToOrg never creates a second row for a
	// guild that already has one.
	if linkErr := s.linkGuildToOrg(ctx, org.UID, guild.ID, guild.Name); linkErr != nil {
		return nil, linkErr
	}

	installedBy := s.fetchInstallerID(ctx, tokenResp.AccessToken)

	connUID, err := s.createOrUpdateConnection(ctx, org.UID, guild, installedBy, targetChannelUID)
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "Discord bot install completed",
		"org_uid", org.UID, "org_slug", org.Slug,
		"guild_id", guild.ID, "guild_name", guild.Name,
		"connection_uid", connUID,
	)

	return &InstallResult{
		ConnectionUID: connUID,
		OrgSlug:       org.Slug,
		GuildID:       guild.ID,
		GuildName:     guild.Name,
	}, nil
}

// fetchInstallerID resolves the Discord user id of whoever completed the
// install. Best-effort: an install with no `identify` grant, or a transient
// API failure, simply records no installer rather than failing the install.
func (s *Service) fetchInstallerID(ctx context.Context, userAccessToken string) string {
	if userAccessToken == "" {
		return ""
	}

	user, err := FetchCurrentUser(ctx, s.apiBaseURL, userAccessToken)
	if err != nil || user == nil {
		slog.DebugContext(ctx, "Discord install: could not identify installer", "error", err)

		return ""
	}

	return user.ID
}

// resolveOrganization picks the org the install belongs to. An org-scoped
// (authenticated) install names its org; a direct "Add to Server" install from
// Discord's app directory has none, and falls back to the guild→org mapping —
// creating an org from the guild only when no mapping exists yet.
func (s *Service) resolveOrganization(
	ctx context.Context, targetOrgSlug string, guild *Guild,
) (*models.Organization, error) {
	if targetOrgSlug != "" {
		org, err := s.db.GetOrganizationBySlug(ctx, targetOrgSlug)
		if err == nil {
			return org, nil
		}
	}

	return s.findOrCreateOrganizationByGuildID(ctx, guild.ID, guild.Name)
}

// findOrCreateOrganizationByGuildID resolves a guild to its organization
// through organization_providers — the SAME table, provider type and provider
// id that auth.DiscordOAuthService.findOrCreateOrganization uses, so auth and
// reporting can never disagree about which org a guild belongs to.
func (s *Service) findOrCreateOrganizationByGuildID(
	ctx context.Context, guildID, guildName string,
) (*models.Organization, error) {
	provider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, guildID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check organization provider: %w", err)
	}

	if err == nil && provider != nil {
		// Through the SHARED healer rather than a local GetOrganization: a link
		// left pointing at a soft-deleted org wins this lookup on every retry,
		// and the bare dereference behind it used to fail the install forever
		// (spec 2026-08-27-02, found on the Slack twin of this function). A
		// cleared link returns (nil, nil) and we create a fresh org below.
		org, resolveErr := auth.ResolveLinkedOrganization(
			ctx, s.db, models.ProviderTypeDiscord, guildID, provider,
		)
		if resolveErr != nil {
			return nil, fmt.Errorf("failed to get organization: %w", resolveErr)
		}

		if org != nil {
			return org, nil
		}
	}

	slug := orgslug.GenerateUnique(ctx, s.db, guildName)
	org := models.NewOrganization(slug, guildName)

	if err := s.db.CreateOrganization(ctx, org); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	slog.InfoContext(ctx, "Created new organization from Discord guild",
		"org_uid", org.UID, "org_slug", org.Slug, "guild_id", guildID)

	return org, nil
}

// linkGuildToOrg records the guild→org mapping, and is idempotent by design.
//
// If a mapping already exists — because "Sign in with Discord" created it, or
// because the bot was installed before — it is LEFT ALONE. A second row for the
// same guild is exactly the divergence resolved question 2 forbids: auth and
// reporting would then disagree about which org a guild is.
func (s *Service) linkGuildToOrg(ctx context.Context, orgUID, guildID, guildName string) error {
	existing, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeDiscord, guildID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check organization provider: %w", err)
	}

	if err == nil && existing != nil {
		// "Already mapped" only counts when the mapping still points somewhere.
		// An org-scoped install never goes through findOrCreateOrganizationByGuildID,
		// so without this heal a link to a soft-deleted org would take the
		// keep-the-existing-mapping branch below and leave the guild with no
		// live mapping at all — permanently (spec 2026-08-27-02).
		home, resolveErr := auth.ResolveLinkedOrganization(
			ctx, s.db, models.ProviderTypeDiscord, guildID, existing,
		)
		if resolveErr != nil {
			return fmt.Errorf("failed to check organization provider: %w", resolveErr)
		}

		if home != nil {
			if existing.OrganizationUID != orgUID {
				// Not an error: an org may install the bot into a guild whose home
				// org is another one (the same "two orgs, one workspace" case Slack
				// supports). The mapping stays with the first org; this install
				// still gets its own connection row.
				slog.InfoContext(ctx, "Discord guild already mapped to another org — keeping the existing mapping",
					"guild_id", guildID, "home_org_uid", existing.OrganizationUID, "installing_org_uid", orgUID)
			}

			return nil
		}
		// Stale link cleared — fall through and map the guild to this install's
		// org instead.
	}

	provider := models.NewOrganizationProvider(orgUID, models.ProviderTypeDiscord, guildID)
	provider.ProviderName = guildName

	if err := s.db.CreateOrganizationProvider(ctx, provider); err != nil {
		return fmt.Errorf("failed to create organization provider: %w", err)
	}

	return nil
}

// operatorChoices are the settings an operator owns, carried across a
// re-install so reconnecting never silently flips a decision they made.
type operatorChoices struct {
	MentionOnCall    bool
	CommentIngestion string
	ChannelID        string
	ChannelName      string
	WebhookURL       string
}

// existingOperatorChoices reads the operator-owned settings off a stored
// Discord integration. Every failure path returns the zero value, which is the
// conservative default for each field.
func (s *Service) existingOperatorChoices(ctx context.Context, channelUID string) operatorChoices {
	conn, err := s.db.GetChannel(ctx, channelUID)
	if err != nil || conn == nil {
		return operatorChoices{}
	}

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return operatorChoices{}
	}

	return operatorChoices{
		MentionOnCall:    settings.MentionOnCall,
		CommentIngestion: settings.CommentIngestion,
		ChannelID:        settings.ChannelID,
		ChannelName:      settings.ChannelName,
		// A legacy webhook URL is deliberately preserved: adding the bot to an
		// org that was posting through a webhook must not throw the webhook
		// away, so a failed bot install can still be rolled back by clearing
		// the guild rather than by re-pasting a URL.
		WebhookURL: settings.WebhookURL,
	}
}

// createOrUpdateConnection creates or updates the Discord integration for a
// guild, scoped to orgUID. Like Slack, the existing-connection lookup is
// org-scoped, so a second org installing the same guild gets its own row.
func (s *Service) createOrUpdateConnection(
	ctx context.Context, orgUID string, guild *Guild, installedByUserID, targetChannelUID string,
) (string, error) {
	existingConn, err := s.resolveExistingConnection(ctx, orgUID, guild.ID, targetChannelUID)
	if err != nil {
		return "", err
	}

	settings := &models.DiscordSettings{
		GuildID:           guild.ID,
		GuildName:         guild.Name,
		InstalledByUserID: installedByUserID,
		MentionOnCall:     true,
		CommentIngestion:  models.DiscordCommentIngestionExplicit,
	}

	if existingConn != nil {
		choices := s.existingOperatorChoices(ctx, existingConn.UID)
		settings.MentionOnCall = choices.MentionOnCall
		settings.CommentIngestion = choices.CommentIngestion
		settings.ChannelID = choices.ChannelID
		settings.ChannelName = choices.ChannelName
		settings.WebhookURL = choices.WebhookURL
	}

	// Best-effort: knowing our own user id lets the Gateway ignore our posts.
	if botUser, botErr := s.newBotClient(s.botToken()).GetCurrentUser(ctx); botErr == nil && botUser != nil {
		settings.BotUserID = botUser.ID
	}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return "", fmt.Errorf("failed to convert settings: %w", err)
	}

	if existingConn != nil {
		name := guild.Name
		update := &models.IntegrationUpdate{Settings: &settingsMap, Name: &name}

		if err := s.db.UpdateChannel(ctx, existingConn.UID, update); err != nil {
			return "", fmt.Errorf("failed to update connection: %w", err)
		}

		return existingConn.UID, nil
	}

	conn := models.NewIntegration(orgUID, models.ConnectionTypeDiscord, guild.Name)
	conn.Settings = settingsMap

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return "", fmt.Errorf("failed to create connection: %w", err)
	}

	return conn.UID, nil
}

// resolveExistingConnection finds the row an install should update: the
// explicitly targeted integration when the install came from an edit page,
// otherwise this org's existing connection for the guild (if any).
func (s *Service) resolveExistingConnection(
	ctx context.Context, orgUID, guildID, targetChannelUID string,
) (*models.Integration, error) {
	if targetChannelUID != "" {
		conn, err := s.db.GetChannel(ctx, targetChannelUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrConnectionNotFound
			}

			return nil, fmt.Errorf("get channel: %w", err)
		}

		if conn.OrganizationUID != orgUID || conn.Type != models.ConnectionTypeDiscord {
			return nil, ErrConnectionNotFound
		}

		return conn, nil
	}

	conn, err := s.db.GetChannelByPropertyForOrg(
		ctx, orgUID, string(models.ConnectionTypeDiscord), "guild_id", guildID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	return conn, nil
}

// GetClient returns a bot client for a guild's connection.
func (s *Service) GetClient(_ context.Context, _ string) (*BotClient, error) {
	token := s.botToken()
	if token == "" {
		return nil, ErrBotNotConfigured
	}

	return s.newBotClient(token), nil
}

// CountInstalledGuilds returns how many distinct guilds have the bot.
func (s *Service) CountInstalledGuilds(ctx context.Context) (int, error) {
	discordType := models.ConnectionTypeDiscord

	// Empty OrganizationUID lists across ALL orgs — a genuinely global count.
	conns, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{Type: &discordType})
	if err != nil {
		return 0, fmt.Errorf("list discord channels: %w", err)
	}

	guilds := make(map[string]struct{}, len(conns))

	for _, conn := range conns {
		if guildID, ok := conn.Settings["guild_id"].(string); ok && guildID != "" {
			guilds[guildID] = struct{}{}
		}
	}

	return len(guilds), nil
}

// DiscordChannel is a destination entry returned by GetDestinations.
type DiscordChannel struct { //nolint:revive // prefix is intentional disambiguation
	ID   string `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
}

// DiscordDestinationsResponse is returned by GetDestinations.
type DiscordDestinationsResponse struct { //nolint:revive // prefix is intentional disambiguation
	Channels  []DiscordChannel `json:"channels"`
	GuildID   string           `json:"guildId"`
	GuildName string           `json:"guildName"`
	Connected bool             `json:"connected"`
}

// GetDestinations lists the guild's postable text channels for the picker.
func (s *Service) GetDestinations(
	ctx context.Context, orgSlug, channelUID string,
) (*DiscordDestinationsResponse, error) {
	settings, err := s.loadConnectionSettings(ctx, orgSlug, channelUID)
	if err != nil {
		return nil, err
	}

	if !settings.UsesBot() && settings.GuildID == "" {
		return nil, ErrDiscordNotConnected
	}

	if s.botToken() == "" {
		return nil, ErrBotNotConfigured
	}

	raw, err := s.newBotClient(s.botToken()).ListGuildChannels(ctx, settings.GuildID)
	if err != nil {
		return nil, fmt.Errorf("discord API error: %w", err)
	}

	channels := make([]DiscordChannel, 0, len(raw))

	for i := range raw {
		if !IsPostableChannelType(raw[i].Type) {
			continue
		}

		channels = append(channels, DiscordChannel{
			ID:   raw[i].ID,
			Name: raw[i].Name,
			Type: raw[i].Type,
		})
	}

	// Discord returns channels in category/position order; the picker is a flat
	// searchable list, so sort by name to make entries findable.
	slices.SortFunc(channels, func(a, b DiscordChannel) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return &DiscordDestinationsResponse{
		Channels:  channels,
		GuildID:   settings.GuildID,
		GuildName: settings.GuildName,
		Connected: true,
	}, nil
}

// loadConnectionSettings resolves org + integration and returns its settings,
// asserting the integration belongs to the org and is a Discord one.
func (s *Service) loadConnectionSettings(
	ctx context.Context, orgSlug, channelUID string,
) (*models.DiscordSettings, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, fmt.Errorf("get organization: %w", err)
	}

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

	if conn.Type != models.ConnectionTypeDiscord {
		return nil, ErrNotDiscordChannel
	}

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, fmt.Errorf("parse discord settings: %w", err)
	}

	return settings, nil
}

// SetDefaultChannel points a guild's integration at a channel, optionally
// posting a welcome message there. Used by the in-band
// `config default-channel` command.
func (s *Service) SetDefaultChannel(
	ctx context.Context, guildID, channelID string, sendWelcome bool,
) error {
	conn, err := s.GetConnectionByGuildID(ctx, guildID)
	if err != nil {
		return err
	}

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return fmt.Errorf("failed to parse settings: %w", err)
	}

	client := s.newBotClient(s.botToken())

	// The channel MUST be one of this guild's own channels. A Discord channel
	// id is discoverable (it is in every "Copy Link" URL), so accepting one
	// just because a client named it would let a command in one guild aim
	// another guild's channel — the same asserted-vs-proven identifier mistake
	// the Teams integration guards against.
	name, ok := s.resolveGuildChannelName(ctx, client, settings.GuildID, channelID)
	if !ok {
		return ErrConnectionNotFound
	}

	settings.ChannelID = channelID
	settings.ChannelName = name

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return fmt.Errorf("failed to convert settings: %w", err)
	}

	if err := s.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Settings: &settingsMap}); err != nil {
		return fmt.Errorf("failed to update connection: %w", err)
	}

	slog.InfoContext(ctx, "Set default Discord channel",
		"guild_id", guildID, "channel_id", channelID, "channel_name", name)

	if sendWelcome {
		if _, err := client.CreateMessage(ctx, channelID, &Message{
			Embeds: []Embed{{
				Title:       "SolidPing is ready",
				Description: "I'll send incident notifications here by default.",
				Color:       ColorBlue,
				Footer:      &Footer{Text: "Change it anytime with `/solidping config default-channel`"},
			}},
			Components: []Component{},
		}); err != nil {
			slog.WarnContext(ctx, "Failed to send Discord welcome message",
				"channel_id", channelID, "error", err)
		}
	}

	return nil
}

// resolveGuildChannelName returns the channel's name when it belongs to the
// guild, and (,"" false) when it does not — which is the authorization gate.
func (s *Service) resolveGuildChannelName(
	ctx context.Context, client *BotClient, guildID, channelID string,
) (string, bool) {
	channels, err := client.ListGuildChannels(ctx, guildID)
	if err != nil {
		return "", false
	}

	for i := range channels {
		if channels[i].ID == channelID && IsPostableChannelType(channels[i].Type) {
			return channels[i].Name, true
		}
	}

	return "", false
}

// CreateCheckResult contains the result of creating a check via Discord.
type CreateCheckResult struct {
	Slug string
	Name string
}

// CreateCheck creates an HTTP check for the org a guild maps to.
func (s *Service) CreateCheck(ctx context.Context, guildID, target string) (*CreateCheckResult, error) {
	conn, err := s.GetConnectionByGuildID(ctx, guildID)
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}

	checkResp, err := s.checksService.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:   "http",
		Config: map[string]any{optionNameURL: target},
	})
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

// HandleGuildRemoved deletes every connection for a guild the bot was kicked
// from. Mirrors Slack's HandleAppUninstalled: all orgs' rows go, not just one,
// or the others keep rendering a stale "connected" integration.
func (s *Service) HandleGuildRemoved(ctx context.Context, guildID string) error {
	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeDiscord), "guild_id", guildID,
	)
	if err != nil {
		return err
	}

	for _, conn := range conns {
		if err := s.db.DeleteChannel(ctx, conn.UID); err != nil {
			return fmt.Errorf("failed to delete connection %s: %w", conn.UID, err)
		}

		slog.InfoContext(ctx, "Deleted Discord connection after guild removal",
			"guild_id", guildID, "connection_uid", conn.UID, "org_uid", conn.OrganizationUID)
	}

	return nil
}
