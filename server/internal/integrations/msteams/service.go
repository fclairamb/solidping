package msteams

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

var (
	// ErrConnectionNotFound is returned when no connection matches.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrNotMSTeamsBotChannel is returned when the channel is not of type
	// msteams-bot.
	ErrNotMSTeamsBotChannel = errors.New("channel is not of type msteams-bot")
	// ErrTenantNotLinked is returned when an activity arrives from an Entra
	// tenant that no organization has claimed yet. The inbound handler turns
	// this into a self-service card telling the admin which tenant id to
	// paste into the dashboard.
	ErrTenantNotLinked = errors.New("microsoft teams tenant is not linked to an organization")
	// ErrNoTenantID is returned when an activity carries no tenant identity,
	// so it cannot be routed to any organization.
	ErrNoTenantID = errors.New("activity carries no tenant id")
	// ErrDestinationNotFound is returned when a destination id is not one of
	// the connection's captured conversation references.
	ErrDestinationNotFound = errors.New("destination not found")
	// ErrUninstalled is returned when the tenant removed the app; the
	// connection row is kept but must not be routed to.
	ErrUninstalled = errors.New("microsoft teams app is uninstalled for this tenant")
)

// Service holds the business logic of the Teams bot integration. It is the
// Teams counterpart of slack.Service, minus the OAuth/user-provisioning half:
// a Teams install carries no user identity we could turn into a SolidPing
// account, so org linkage is established by tenant id instead (see
// GetConnectionByTenantID / HandleInstall).
type Service struct {
	db            db.Service
	cfg           *config.Config
	checksService *checks.Service

	// newBotClient builds the Bot Connector client used for outbound calls.
	// Tests override it to point at an httptest fake connector (mirrors
	// slack.Service.newAPIClient).
	newBotClient func(serviceURL string) *Client
}

// NewService creates a new Microsoft Teams bot integration service.
func NewService(dbService db.Service, cfg *config.Config, checksService *checks.Service) *Service {
	svc := &Service{
		db:            dbService,
		cfg:           cfg,
		checksService: checksService,
	}

	svc.newBotClient = func(serviceURL string) *Client {
		return NewClient(cfg.MSTeams.AppID, cfg.MSTeams.AppSecret, serviceURL)
	}

	return svc
}

// Enabled reports whether the Teams bot is switched on for this instance.
// Default is false: Bot Framework has no Socket-Mode equivalent, so the
// messaging endpoint must be reachable from Microsoft over public HTTPS and
// an operator has to opt in deliberately.
func (s *Service) Enabled() bool {
	return s.cfg != nil && s.cfg.MSTeams.Enabled
}

// Configured reports whether credentials are present. Without an app ID no
// inbound token can be validated and no outbound token can be minted.
func (s *Service) Configured() bool {
	return s.cfg != nil && s.cfg.MSTeams.AppID != "" && s.cfg.MSTeams.AppSecret != ""
}

// GetConnectionByTenantID resolves the connection inbound activities for an
// Entra tenant should operate on.
//
// Unlike Slack, this deliberately does NOT consult organization_providers:
// that table's (microsoft, tenant_id) rows belong to the Microsoft SSO
// connector, and hijacking them here would make "who can sign in with
// Microsoft" and "whose Teams bot is this" the same decision — two settings
// an operator legitimately configures independently. So routing is purely
// connection-driven: the oldest connection for the tenant wins, with a
// warning when the tenant resolves to more than one org.
func (s *Service) GetConnectionByTenantID(ctx context.Context, tenantID string) (*models.Integration, error) {
	if tenantID == "" {
		return nil, ErrNoTenantID
	}

	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeMSTeamsBot), "tenant_id", tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("list msteams-bot connections: %w", err)
	}

	if len(conns) == 0 {
		return nil, ErrTenantNotLinked
	}

	if len(conns) > 1 {
		slog.WarnContext(ctx, "Microsoft Teams tenant resolves to multiple connections — "+
			"routing to the oldest one; ambiguous until the extra connections are removed",
			"tenant_id", tenantID,
			"connection_count", len(conns),
			"resolved_connection_uid", conns[0].UID,
			"resolved_org_uid", conns[0].OrganizationUID,
		)
	}

	return conns[0], nil
}

// settingsOf parses a connection's settings, wrapping the parse error.
func settingsOf(conn *models.Integration) (*models.MSTeamsBotSettings, error) {
	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, fmt.Errorf("parse msteams-bot settings: %w", err)
	}

	return settings, nil
}

// saveSettings writes settings back onto the connection row.
func (s *Service) saveSettings(
	ctx context.Context, conn *models.Integration, settings *models.MSTeamsBotSettings,
) error {
	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return fmt.Errorf("convert msteams-bot settings: %w", err)
	}

	if err := s.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Settings: &settingsMap}); err != nil {
		return fmt.Errorf("update connection %s: %w", conn.UID, err)
	}

	conn.Settings = settingsMap

	return nil
}

// HandleInstall processes an `installationUpdate` with action `add`, or a
// `conversationUpdate` in which the bot itself was added to a conversation.
// Both carry everything needed to register the conversation as a notification
// destination, so they share one code path.
//
// The org↔tenant link must already exist (an admin pastes the tenant id into
// the dashboard, or the single-org self-hosted auto-link below applies) —
// otherwise ErrTenantNotLinked is returned and the caller replies in Teams
// with the tenant id to paste. Auto-creating an organization the way the
// Slack Marketplace flow does is not possible here: a Teams install activity
// carries no user email, so there would be nobody to make an admin of it.
func (s *Service) HandleInstall(ctx context.Context, activity *Activity) (*models.Integration, error) {
	tenantID := activity.TenantID()
	if tenantID == "" {
		return nil, ErrNoTenantID
	}

	conn, err := s.GetConnectionByTenantID(ctx, tenantID)
	if errors.Is(err, ErrTenantNotLinked) {
		conn, err = s.autoLinkSingleOrg(ctx, tenantID)
	}

	if err != nil {
		return nil, err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return nil, err
	}

	settings.TenantID = tenantID
	settings.ServiceURL = normalizeServiceURL(activity.ServiceURL)
	settings.AppID = s.cfg.MSTeams.AppID
	// A reinstall clears the uninstalled marker so routing resumes.
	settings.UninstalledAt = ""

	if activity.Recipient != nil && activity.Recipient.ID != "" {
		settings.BotID = activity.Recipient.ID
	}

	if activity.From != nil && activity.From.ID != "" && settings.InstalledByUserID == "" {
		settings.InstalledByUserID = activity.From.ID
	}

	upsertDestination(settings, destinationFromActivity(activity))

	if err := s.saveSettings(ctx, conn, settings); err != nil {
		return nil, err
	}

	// The row may have been disabled by a previous uninstall.
	if !conn.Enabled {
		enabled := true
		if err := s.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Enabled: &enabled}); err != nil {
			return nil, fmt.Errorf("re-enable connection %s: %w", conn.UID, err)
		}

		conn.Enabled = true
	}

	slog.InfoContext(ctx, "Microsoft Teams bot installed",
		"tenant_id", tenantID,
		"connection_uid", conn.UID,
		"org_uid", conn.OrganizationUID,
		"conversation_id", activity.ConversationID(),
	)

	return conn, nil
}

// autoLinkSingleOrg is the self-hosted convenience path: when the operator
// pinned a tenant (msteams.tenant_id) and the instance hosts exactly one
// organization, an install from that tenant creates the connection without a
// dashboard round-trip. Both conditions are required — without the pin, any
// tenant on the internet that sideloads the manifest would attach itself to
// the instance's only org.
func (s *Service) autoLinkSingleOrg(ctx context.Context, tenantID string) (*models.Integration, error) {
	if s.cfg.MSTeams.TenantID == "" || s.cfg.MSTeams.TenantID != tenantID {
		return nil, ErrTenantNotLinked
	}

	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}

	if len(orgs) != 1 {
		return nil, ErrTenantNotLinked
	}

	conn := models.NewIntegration(orgs[0].UID, models.ConnectionTypeMSTeamsBot, "Microsoft Teams")

	settings := &models.MSTeamsBotSettings{TenantID: tenantID}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return nil, fmt.Errorf("convert msteams-bot settings: %w", err)
	}

	conn.Settings = settingsMap

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, fmt.Errorf("create msteams-bot connection: %w", err)
	}

	slog.InfoContext(ctx, "Auto-linked Microsoft Teams tenant to the instance's only organization",
		"tenant_id", tenantID, "org_uid", orgs[0].UID, "connection_uid", conn.UID)

	return conn, nil
}

// destinationFromActivity builds the conversation reference to store.
func destinationFromActivity(activity *Activity) *models.MSTeamsDestination {
	return &models.MSTeamsDestination{
		ID:         activity.ConversationID(),
		Name:       activity.ConversationName(),
		TeamID:     activity.TeamID(),
		TeamName:   activity.TeamName(),
		ServiceURL: normalizeServiceURL(activity.ServiceURL),
		Type:       DestinationTypeChannel,
	}
}

// upsertDestination adds or refreshes a conversation reference and promotes
// the first one to the connection's default notification destination — the
// same "first channel the bot lands in becomes the default" behavior as
// slack.handleMemberJoinedChannel.
func upsertDestination(settings *models.MSTeamsBotSettings, dest *models.MSTeamsDestination) {
	if dest.ID == "" {
		return
	}

	replaced := false

	for i := range settings.Destinations {
		if settings.Destinations[i].ID != dest.ID {
			continue
		}

		// Keep a previously-known name when the update carries none.
		if dest.Name == "" {
			dest.Name = settings.Destinations[i].Name
		}

		settings.Destinations[i] = *dest
		replaced = true

		break
	}

	if !replaced {
		settings.Destinations = append(settings.Destinations, *dest)
	}

	if settings.ChannelID == "" {
		settings.ChannelID = dest.ID
		settings.ChannelName = dest.Name
		settings.TeamID = dest.TeamID
		settings.DisplayName = dest.Name
	}
}

// HandleUninstall processes an `installationUpdate` with action `remove`.
//
// Unlike Slack's app_uninstalled (which deletes the connection outright,
// because the bot token is revoked and the row is then worthless), the row is
// kept here and marked uninstalled: the credentials are instance-level, so a
// reinstall restores service without the org losing its notification wiring.
// Routing stops because Enabled is set to false and the destinations are
// cleared.
func (s *Service) HandleUninstall(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return ErrNoTenantID
	}

	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeMSTeamsBot), "tenant_id", tenantID,
	)
	if err != nil {
		return fmt.Errorf("list msteams-bot connections: %w", err)
	}

	for _, conn := range conns {
		settings, parseErr := settingsOf(conn)
		if parseErr != nil {
			slog.WarnContext(ctx, "Skipping msteams-bot connection with unparseable settings on uninstall",
				"connection_uid", conn.UID, "error", parseErr)

			continue
		}

		settings.UninstalledAt = time.Now().UTC().Format(time.RFC3339)
		settings.Destinations = nil
		settings.ChannelID = ""
		settings.ChannelName = ""
		settings.DisplayName = ""

		if err := s.saveSettings(ctx, conn, settings); err != nil {
			return err
		}

		enabled := false
		if err := s.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Enabled: &enabled}); err != nil {
			return fmt.Errorf("disable connection %s: %w", conn.UID, err)
		}

		slog.InfoContext(ctx, "Marked Microsoft Teams connection uninstalled",
			"tenant_id", tenantID,
			"connection_uid", conn.UID,
			"org_uid", conn.OrganizationUID,
		)
	}

	return nil
}

// RegisterConversation captures (or refreshes) a conversation reference for
// an already-linked tenant. Used by inbound message activities so a channel
// the bot is talked to in becomes addressable even if the original
// conversationUpdate was missed.
func (s *Service) RegisterConversation(ctx context.Context, activity *Activity) error {
	conn, err := s.GetConnectionByTenantID(ctx, activity.TenantID())
	if err != nil {
		return err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return err
	}

	before := len(settings.Destinations)
	serviceURL := normalizeServiceURL(activity.ServiceURL)

	upsertDestination(settings, destinationFromActivity(activity))

	// Avoid a write on every single inbound message: only persist when
	// something actually changed.
	if before == len(settings.Destinations) && settings.ServiceURL == serviceURL {
		return nil
	}

	settings.ServiceURL = serviceURL

	return s.saveSettings(ctx, conn, settings)
}

// SetDefaultDestination sets the connection's default notification
// destination. destID must already be one of the captured conversation
// references — Teams has no cross-team channel reference syntax, so there is
// nothing to resolve a free-text channel name against.
func (s *Service) SetDefaultDestination(
	ctx context.Context, tenantID, destID string,
) (*models.MSTeamsDestination, error) {
	conn, err := s.GetConnectionByTenantID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return nil, err
	}

	idx := slices.IndexFunc(settings.Destinations, func(d models.MSTeamsDestination) bool {
		return d.ID == destID
	})
	if idx == -1 {
		return nil, ErrDestinationNotFound
	}

	dest := settings.Destinations[idx]
	settings.ChannelID = dest.ID
	settings.ChannelName = dest.Name
	settings.TeamID = dest.TeamID
	settings.DisplayName = dest.Name

	if err := s.saveSettings(ctx, conn, settings); err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "Set default Microsoft Teams destination",
		"tenant_id", tenantID, "conversation_id", dest.ID, "name", dest.Name)

	return &dest, nil
}

// DestinationsResponse is returned by GetDestinations, mirroring
// slack.SlackDestinationsResponse.
type DestinationsResponse struct {
	Destinations []models.MSTeamsDestination `json:"destinations"`
	// TenantID / Connected / Uninstalled let the dashboard render the setup
	// state without a second round-trip.
	TenantID    string `json:"tenantId"`
	Connected   bool   `json:"connected"`
	Uninstalled bool   `json:"uninstalled"`
}

// GetDestinations lists the conversation references available as
// notification destinations for a connection.
//
// Unlike Slack there is no live API call: a Teams bot cannot enumerate the
// channels of a team it was not added to, so destinations are exactly the
// conversations captured from install / conversationUpdate activities.
func (s *Service) GetDestinations(ctx context.Context, orgSlug, channelUID string) (*DestinationsResponse, error) {
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

	if conn.Type != models.ConnectionTypeMSTeamsBot {
		return nil, ErrNotMSTeamsBotChannel
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return nil, err
	}

	dests := make([]models.MSTeamsDestination, len(settings.Destinations))
	copy(dests, settings.Destinations)

	// Teams delivers conversationUpdate activities in arbitrary order, so
	// sort by team then channel name to keep the picker findable — same
	// reasoning as slack.GetDestinations.
	slices.SortFunc(dests, func(a, b models.MSTeamsDestination) int {
		if c := strings.Compare(strings.ToLower(a.TeamName), strings.ToLower(b.TeamName)); c != 0 {
			return c
		}

		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	return &DestinationsResponse{
		Destinations: dests,
		TenantID:     settings.TenantID,
		Connected:    settings.TenantID != "" && settings.UninstalledAt == "",
		Uninstalled:  settings.UninstalledAt != "",
	}, nil
}

// CountInstalledTenants returns the number of distinct Entra tenants with at
// least one live (not uninstalled) connection. Used by the status endpoint,
// mirroring slack.CountInstalledTeams. Best-effort: connections whose
// settings fail to parse are skipped rather than failing the whole count.
func (s *Service) CountInstalledTenants(ctx context.Context) (int, error) {
	connType := models.ConnectionTypeMSTeamsBot

	channels, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{Type: &connType})
	if err != nil {
		return 0, fmt.Errorf("list msteams-bot channels: %w", err)
	}

	tenants := make(map[string]struct{}, len(channels))

	for _, ch := range channels {
		settings, parseErr := models.MSTeamsBotSettingsFromJSONMap(ch.Settings)
		if parseErr != nil {
			slog.WarnContext(ctx, "Skipping msteams-bot channel with unparseable settings in tenant count",
				"connection_uid", ch.UID, "error", parseErr)

			continue
		}

		if settings.TenantID == "" || settings.UninstalledAt != "" {
			continue
		}

		tenants[settings.TenantID] = struct{}{}
	}

	return len(tenants), nil
}

// CreateCheckResult contains the result of creating a check via a Teams
// command.
type CreateCheckResult struct {
	Slug string
	Name string
}

// CreateCheckWithOptions creates an HTTP check in the organization the tenant
// is linked to.
func (s *Service) CreateCheckWithOptions(
	ctx context.Context, tenantID, url, slug, period string,
) (*CreateCheckResult, error) {
	conn, err := s.GetConnectionByTenantID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}

	req := checks.CreateCheckRequest{
		Type:   "http",
		Config: map[string]any{"url": url},
	}

	if slug != "" {
		req.Slug = slug
	}

	if period != "" {
		req.Period = &period
	}

	checkResp, err := s.checksService.CreateCheck(ctx, org.Slug, req)
	if err != nil {
		return nil, fmt.Errorf("create check: %w", err)
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

// orgForTenant resolves the organization behind a tenant, refusing tenants
// whose app has been uninstalled.
func (s *Service) orgForTenant(ctx context.Context, tenantID string) (*models.Organization, error) {
	conn, err := s.GetConnectionByTenantID(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return nil, err
	}

	if settings.UninstalledAt != "" {
		return nil, ErrUninstalled
	}

	org, err := s.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}

	return org, nil
}
