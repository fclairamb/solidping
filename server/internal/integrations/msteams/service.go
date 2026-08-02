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
	// ErrAmbiguousTenant is returned when more than one connection claims the
	// same tenant. Under the linking-code model this cannot happen (LinkTenant
	// refuses a tenant that is already bound), so it means the data was
	// tampered with or migrated in — fail safe rather than silently guessing
	// which organization should receive another tenant's data.
	ErrAmbiguousTenant = errors.New("microsoft teams tenant resolves to multiple connections")
	// ErrConnectionAlreadyLinked is returned when a link code targets a
	// connection that is already bound to a tenant.
	ErrConnectionAlreadyLinked = errors.New("this integration is already linked to a Microsoft 365 tenant")
	// ErrLinkRequiresChannel is returned when `link` is used from a personal
	// (1:1) chat. Linking a whole tenant is a consequential action, so it has
	// to happen somewhere the tenant's other members can see it.
	ErrLinkRequiresChannel = errors.New("run the link command in a team channel, not a private chat")
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
		return NewClientForTenant(
			cfg.MSTeams.AppID, cfg.MSTeams.AppSecret, serviceURL, cfg.MSTeams.TenantID,
		)
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
// an operator legitimately configures independently.
//
// A tenant maps to at most ONE connection: LinkTenant refuses to bind a
// tenant that another organization already holds, so ambiguity is impossible
// by construction. If it is ever observed anyway (hand-edited rows, a bad
// migration), this fails closed with ErrAmbiguousTenant rather than picking
// one — silently resolving to the oldest row is exactly how one org would end
// up receiving another org's Teams data.
func (s *Service) GetConnectionByTenantID(ctx context.Context, tenantID string) (*models.Integration, error) {
	if tenantID == "" {
		return nil, ErrNoTenantID
	}

	// The single-tenant pin is enforced here because this is the choke point
	// every inbound path funnels through — commands, installs and
	// conversation updates alike — so a foreign tenant cannot reach any of
	// them by taking a different route.
	if err := s.checkTenantAllowed(tenantID); err != nil {
		return nil, err
	}

	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeMSTeamsBot), "tenant_id", tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("list msteams-bot connections: %w", err)
	}

	return resolveSingleConnection(ctx, tenantID, conns)
}

// resolveSingleConnection turns the rows claiming a tenant into the one
// connection to route to, refusing rather than guessing when there is more
// than one.
//
// A unique partial index on the settings tenant_id makes duplicates
// unreachable through the application, so this is defense in depth for
// hand-edited or imported data. It is split out from GetConnectionByTenantID
// precisely because that state can no longer be produced through the database
// — the decision still has to be verifiable.
func resolveSingleConnection(
	ctx context.Context, tenantID string, conns []*models.Integration,
) (*models.Integration, error) {
	if len(conns) == 0 {
		return nil, ErrTenantNotLinked
	}

	if len(conns) > 1 {
		slog.ErrorContext(ctx, "Microsoft Teams tenant resolves to multiple connections — refusing to route",
			"tenant_id", tenantID,
			"connection_count", len(conns),
		)

		return nil, ErrAmbiguousTenant
	}

	return conns[0], nil
}

// checkTenantAllowed enforces the self-hosted single-tenant pin
// (`msteams.tenant_id`).
//
// This is deliberately NOT done during JWT verification: Microsoft documents
// the Connector-to-Bot token as carrying only aud/iss/nbf/exp/serviceurl —
// there is no `tid` claim — so a check there would compare the pin against an
// always-empty value and reject every legitimate activity, disabling the very
// deployment mode the pin exists to serve. The tenant is instead read from
// the activity body, which is trustworthy *because* the request carrying it
// already passed the signature check.
func (s *Service) checkTenantAllowed(tenantID string) error {
	if s.cfg == nil || s.cfg.MSTeams.TenantID == "" {
		return nil
	}

	if tenantID != s.cfg.MSTeams.TenantID {
		return fmt.Errorf("%w: %q", ErrTenantNotAllowed, tenantID)
	}

	return nil
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
// The org↔tenant link must ALREADY exist — either because the admin redeemed
// a linking code (LinkTenant), or because the self-hosted single-tenant
// auto-link below applies. Otherwise ErrTenantNotLinked is returned and the
// caller tells the channel how to link. This function never invents the link
// itself: doing so from a tenant id alone is precisely the cross-tenant
// hijack the linking code exists to prevent.
//
// Auto-creating an organization the way the Slack Marketplace flow does is
// also not possible here: a Teams install activity carries no user email, so
// there would be nobody to make an admin of it.
func (s *Service) HandleInstall(ctx context.Context, activity *Activity) (*models.Integration, error) {
	tenantID := activity.TenantID()
	if tenantID == "" {
		return nil, ErrNoTenantID
	}

	if err := s.checkTenantAllowed(tenantID); err != nil {
		return nil, err
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

// PendingLink is what the dashboard shows after the admin clicks "Connect
// Microsoft Teams".
type PendingLink struct {
	ConnectionUID string `json:"connectionUid"`
	Code          string `json:"code"`
	ExpiresInSecs int    `json:"expiresInSeconds"`
}

// StartLink creates (or reuses) an unlinked msteams-bot connection for the
// organization and mints a one-time linking code for it.
//
// The connection deliberately starts with NO tenant id. It gains one only
// when someone inside a Microsoft 365 tenant sends the bot this code — which
// is the proof that the org asking for the link and the tenant being linked
// are the same actor.
func (s *Service) StartLink(ctx context.Context, orgUID, connectionUID string) (*PendingLink, error) {
	conn, err := s.resolveLinkTarget(ctx, orgUID, connectionUID)
	if err != nil {
		return nil, err
	}

	code, err := generateLinkCode(ctx, s.db, orgUID, conn.UID)
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "Minted Microsoft Teams link code",
		"org_uid", orgUID, "connection_uid", conn.UID)

	return &PendingLink{
		ConnectionUID: conn.UID,
		Code:          code,
		ExpiresInSecs: int(LinkCodeTTL.Seconds()),
	}, nil
}

// resolveLinkTarget returns the connection a new link code should authorize:
// the caller-specified one when given (validated to belong to the org, be of
// the right type, and not already be linked), otherwise a freshly created
// unlinked connection.
func (s *Service) resolveLinkTarget(
	ctx context.Context, orgUID, connectionUID string,
) (*models.Integration, error) {
	if connectionUID == "" {
		return s.createUnlinkedConnection(ctx, orgUID)
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, fmt.Errorf("get channel: %w", err)
	}

	if conn.OrganizationUID != orgUID || conn.DeletedAt != nil {
		return nil, ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeMSTeamsBot {
		return nil, ErrNotMSTeamsBotChannel
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return nil, err
	}

	// Re-linking a live connection would silently move an active tenant's
	// notifications; require an explicit delete instead.
	if settings.TenantID != "" && settings.UninstalledAt == "" {
		return nil, ErrConnectionAlreadyLinked
	}

	return conn, nil
}

// createUnlinkedConnection creates a tenant-less msteams-bot connection.
func (s *Service) createUnlinkedConnection(ctx context.Context, orgUID string) (*models.Integration, error) {
	conn := models.NewIntegration(orgUID, models.ConnectionTypeMSTeamsBot, "Microsoft Teams")

	settings := &models.MSTeamsBotSettings{}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return nil, fmt.Errorf("convert msteams-bot settings: %w", err)
	}

	conn.Settings = settingsMap

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, fmt.Errorf("create msteams-bot connection: %w", err)
	}

	return conn, nil
}

// LinkTenant redeems a linking code presented from inside Teams and binds the
// activity's tenant to the organization that minted it.
//
// This is the ONLY path that ever writes tenant_id from scratch, and the
// tenant it writes comes from a signature-verified Bot Framework activity —
// never from user input. A tenant already held by another connection is
// refused, which keeps the tenant→org mapping one-to-one and inbound routing
// unambiguous.
func (s *Service) LinkTenant(ctx context.Context, code string, activity *Activity) (*models.Integration, error) {
	tenantID := activity.TenantID()
	if tenantID == "" {
		return nil, ErrNoTenantID
	}

	// Bot Framework cannot tell us whether the sender is a team owner without
	// Microsoft Graph consent this integration does not request (see the
	// security section of the notification docs). Requiring the command to be
	// issued in a shared conversation is the mitigation available here: the
	// link cannot be performed silently in a private chat, so the tenant's
	// other members can see it happen.
	if activity.IsPersonalConversation() {
		return nil, ErrLinkRequiresChannel
	}

	if err := s.checkTenantAllowed(tenantID); err != nil {
		return nil, err
	}

	// Look the tenant up first so the "already claimed" answer is computed
	// from pre-redemption state. The code is still consumed below whatever
	// the outcome — a code that reached a real, verified activity has been
	// exposed to that tenant's users, so burning it and asking the admin to
	// mint a fresh one is the safer failure mode than allowing retries.
	existing, err := s.GetConnectionByTenantID(ctx, tenantID)
	if err != nil && !errors.Is(err, ErrTenantNotLinked) {
		return nil, err
	}

	orgUID, connUID, err := redeemLinkCode(ctx, s.db, code)
	if err != nil {
		return nil, err
	}

	if claimErr := s.clearDisplacedClaim(ctx, existing, connUID); claimErr != nil {
		return nil, claimErr
	}

	conn, err := s.loadLinkTarget(ctx, orgUID, connUID)
	if err != nil {
		return nil, err
	}

	if bindErr := s.bindTenant(ctx, conn, tenantID, activity); bindErr != nil {
		return nil, bindErr
	}

	slog.InfoContext(ctx, "Linked Microsoft Teams tenant to organization",
		"tenant_id", tenantID, "org_uid", orgUID, "connection_uid", conn.UID)

	// Now that the tenant is bound, register the conversation the code was
	// sent from and re-enable the row.
	return s.HandleInstall(ctx, activity)
}

// clearDisplacedClaim decides what happens when another connection already
// holds this tenant.
//
// A LIVE claim wins: whoever holds it is receiving this tenant's notifications
// right now. An UNINSTALLED claim does not — the party that controls the
// tenant has already revoked the app there, which is the strongest signal
// available that the binding is unwanted, and it is the recovery path for a
// tenant whose admin was beaten to the link: remove the app in Teams,
// reinstall it, and link to the right organization. Without this a wrong link
// would be permanent for the tenant, since only the holding org can delete its
// own integration.
func (s *Service) clearDisplacedClaim(ctx context.Context, existing *models.Integration, connUID string) error {
	if existing == nil || existing.UID == connUID {
		return nil
	}

	released, err := s.releaseStaleClaim(ctx, existing)
	if err != nil {
		return err
	}

	if !released {
		return ErrTenantAlreadyLinked
	}

	return nil
}

// loadLinkTarget fetches and validates the connection a redeemed code names.
func (s *Service) loadLinkTarget(ctx context.Context, orgUID, connUID string) (*models.Integration, error) {
	conn, err := s.db.GetChannel(ctx, connUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, fmt.Errorf("get channel: %w", err)
	}

	if conn.OrganizationUID != orgUID || conn.DeletedAt != nil ||
		conn.Type != models.ConnectionTypeMSTeamsBot {
		return nil, ErrConnectionNotFound
	}

	return conn, nil
}

// bindTenant writes the tenant onto the connection, recording who linked it.
func (s *Service) bindTenant(
	ctx context.Context, conn *models.Integration, tenantID string, activity *Activity,
) error {
	settings, err := settingsOf(conn)
	if err != nil {
		return err
	}

	settings.TenantID = tenantID

	if activity.From != nil && activity.From.ID != "" {
		settings.InstalledByUserID = activity.From.ID
	}

	if err := s.saveSettings(ctx, conn, settings); err != nil {
		// The claim lookup above and this write are not serialized, so two
		// simultaneous redemptions for the same tenant could both get past
		// the check. A unique partial index on the settings tenant_id makes
		// that impossible at the storage layer; translate its violation into
		// the same answer the application-level check gives.
		if isUniqueViolation(err) {
			return ErrTenantAlreadyLinked
		}

		return err
	}

	return nil
}

// releaseStaleClaim clears the tenant binding from a connection whose app has
// been uninstalled from the tenant, freeing the tenant to be linked again.
// Returns false (leaving the row untouched) when the claim is still live.
func (s *Service) releaseStaleClaim(ctx context.Context, existing *models.Integration) (bool, error) {
	settings, err := settingsOf(existing)
	if err != nil {
		return false, err
	}

	if settings.UninstalledAt == "" {
		return false, nil
	}

	settings.TenantID = ""
	settings.Destinations = nil
	settings.ChannelID = ""
	settings.ChannelName = ""
	settings.DisplayName = ""

	if err := s.saveSettings(ctx, existing, settings); err != nil {
		return false, err
	}

	slog.InfoContext(ctx, "Released an uninstalled Microsoft Teams tenant claim so the tenant can be re-linked",
		"connection_uid", existing.UID, "org_uid", existing.OrganizationUID)

	return true, nil
}

// uniqueViolationMarkers are the dialect-specific signatures of a unique
// index violation. Bun surfaces the driver error verbatim, and the two
// supported drivers word it differently, so both spellings are matched.
//
//nolint:gochecknoglobals // constant marker list
var uniqueViolationMarkers = []string{
	"unique constraint", // PostgreSQL (SQLSTATE 23505)
	"unique violation",  // PostgreSQL, alternate wording
	"sqlstate=23505",    // PostgreSQL, structured form
	"unique_violation",  // PostgreSQL, structured form
	"unique index",      // SQLite
	"constraint failed", // SQLite (UNIQUE constraint failed: ...)
}

// isUniqueViolation reports whether err came from a unique index conflict.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	for _, marker := range uniqueViolationMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}

	return false
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

	// Defense in depth: honor the single-tenant pin here too. A pinned
	// instance cannot currently hold a row for another tenant, so this is
	// unreachable today — but it keeps "every tenant-scoped path goes through
	// checkTenantAllowed" a real invariant rather than an advisory one, so a
	// future change to pin semantics cannot open a hole here.
	if err := s.checkTenantAllowed(tenantID); err != nil {
		return err
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
