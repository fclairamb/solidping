package slack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
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
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/support"
)

// IncidentService defines the interface for incident operations needed by Slack integration.
// This interface is implemented by handlers/incidents.Service via an adapter.
type IncidentService interface {
	// AcknowledgeIncidentFromSlack marks an incident as acknowledged via Slack.
	// slackTeamID names the workspace the press happened in, so the ack-notice
	// fan-out can skip the message that already shows the acknowledgment.
	AcknowledgeIncidentFromSlack(
		ctx context.Context, orgUID, incidentUID, slackUserID, slackUsername, slackTeamID string,
	) (*models.Incident, error)
	// GetIncidentByUID gets an incident by UID.
	GetIncidentByUID(ctx context.Context, orgUID, incidentUID string) (*models.Incident, error)
	// GetCheckByUID gets a check by UID.
	GetCheckByUID(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
	// AddCommentFromSlack appends a Slack-authored comment to an incident's
	// timeline, returning the created event.
	AddCommentFromSlack(
		ctx context.Context, orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID, slackTs string,
	) (*models.Event, error)
	// AddCommentFromSlackCommand appends a comment posted with the `/comment`
	// slash command. Separate from AddCommentFromSlack because a slash command
	// posts nothing visible in the channel, so — unlike a thread reply — the
	// originating workspace MUST still receive the fan-out.
	AddCommentFromSlackCommand(
		ctx context.Context, orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID string,
	) (*models.Event, error)
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
	// ChannelUID is set when the install was triggered from a specific channel
	// edit page. The frontend uses it to navigate back to that channel.
	ChannelUID   string
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	OrgSlug      string
	UserUID      string
	// Pending is true when the install succeeded but the organization did not
	// admit the installing user (e.g. the org opted out of Slack workspace
	// auto-join, or it is at its member cap): no membership was created, a
	// membership request is awaiting approval, and the tokens above are an
	// org-less session with no refresh token.
	Pending bool
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
	payloadKeyExpiresIn    = "expiresIn"
	payloadKeyOrgSlug      = "orgSlug"
	payloadKeyUserUID      = "userUID"
	payloadKeySource       = "source"
	// payloadKeyChannelUID and payloadKeyInstallOrg carry the channel context
	// from the install entry point through the CSRF state so the callback can
	// update the specific channel that triggered the install flow.
	payloadKeyChannelUID = "channelUid"
	payloadKeyInstallOrg = "installOrg"
)

// slackBotScopes / slackUserScopes are the scopes requested during install.
// Bot scopes drive the integration's runtime; user scopes drive the OpenID
// Connect lookup that identifies the installing user.
//
// scopeChatWrite is the base posting scope, referenced from more than one place.
const scopeChatWrite = "chat:write"

//nolint:gochecknoglobals // package-level constant scope lists
var (
	// slackBotScopes is the single source of truth at install time:
	// BuildInstallURL joins it verbatim into the `scope=` parameter. It must stay
	// equal to `oauth_config.scopes.bot` in wiki/slack/manifest-{dev,prod}.json —
	// a scope in the manifest but not here is never granted, and a scope here but
	// not in the manifest makes Slack reject the authorize request outright.
	// TestBotScopesMatchManifests pins the two together.
	//
	// SLACK DOES NOT GRANT NEW SCOPES TO EXISTING INSTALLS. Adding a line here is
	// a user-visible migration, not a deploy: every workspace connected before it
	// must re-run the install to gain the capability. Only the im:history case is
	// surfaced today — models.SlackSettings.DMCaptureAvailable, the reinstall
	// prompt in the integration UI, and the solidping_support_dm_unavailable
	// gauge all look at that one scope.
	slackBotScopes = []string{
		scopeChatWrite,
		"chat:write.public",
		"channels:read",
		// channels:history / groups:history are what make Slack deliver
		// message.channels / message.groups, which is how a human reply in the
		// thread under an incident alert becomes an incident comment
		// (handleMessage, events.go). Without them the reply never arrives.
		"channels:history",
		"groups:read",
		"groups:history",
		// im:write opens the DM used for onboarding, setup prompts and support
		// replies. im:history is what makes Slack deliver the user's side of that
		// conversation (`message.im`): without it a DM is not ignored, it never
		// arrives (spec 2026-08-22-02).
		"im:write",
		models.SlackScopeIMHistory,
		"users:read",
		"users:read.email",
		"commands",
		"app_mentions:read",
		"reactions:write",
		// links:read delivers link_shared for solidping.io URLs; links:write is
		// what chat.unfurl needs in order to answer it with a live status preview.
		"links:read",
		"links:write",
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

	// newAPIClient builds the Slack Web API client used for outbound calls.
	// Tests override it to point at an httptest fake Slack server (mirrors
	// SlackSocketSupervisor.dialClient).
	newAPIClient func(token string) *Client

	// oauthURL / userInfoURL are the Slack OAuth endpoints hit during the
	// install callback. Fields rather than constants so tests can drive the
	// real HandleOAuthCallback against httptest stand-ins.
	oauthURL    string
	userInfoURL string

	// identitySync auto-matches org members to workspace users after an
	// install. Nil until wired (see SetIdentitySync).
	identitySync IdentitySyncFn

	// support is the instance support inbox that captures DMs to the bot. Nil
	// disables DM capture entirely, which is exactly the behavior that
	// predates the feature (see SetSupport).
	support *support.Service
}

// SetSupport wires the support inbox after construction, mirroring
// SetIdentitySync. Late injection because the support service is built from the
// same registry that owns the mailer, and this package must stay importable by
// it without a cycle.
func (s *Service) SetSupport(svc *support.Service) {
	s.support = svc
}

// ReportDMCapability logs, once at startup, how many connected Slack workspaces
// still cannot deliver DMs because they were installed before im:history was
// requested, and publishes the same number as a gauge.
//
// This is the "degrade cleanly and observably" half of the decision to ship
// Slack DM support: such a workspace never delivers message.im at all, so the
// only symptom is an inbox that stays empty. One log line and one gauge turn
// that into something an operator can see.
func (s *Service) ReportDMCapability(ctx context.Context) {
	slackType := models.ConnectionTypeSlack

	connections, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{Type: &slackType})
	if err != nil {
		return
	}

	stale := 0

	for _, conn := range connections {
		settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
		if err != nil || settings == nil || settings.TeamID == "" {
			continue
		}

		if !settings.DMCaptureAvailable() {
			stale++
		}
	}

	prommetrics.SupportDMUnavailable.WithLabelValues(models.SupportChannelSlack).Set(float64(stale))

	if stale > 0 {
		slog.WarnContext(ctx,
			"Slack workspaces must reinstall the app before their direct messages reach the support inbox",
			"workspaces", stale, "scope", models.SlackScopeIMHistory)
	}
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
		newAPIClient:     NewClient,
		oauthURL:         SlackOAuthURL,
		userInfoURL:      SlackAPIBaseURL + "/openid.connect.userInfo",
	}
}

// GetConnectionByTeamID retrieves the Slack connection that inbound
// commands/events/interactions for a workspace (team_id) should operate on.
//
// A workspace can be connected to several orgs (one connection row per
// org — see spec 2026-07-05-01), so this is the single choke point that
// decides which one is authoritative for inbound routing:
//
//  1. Home org: the org recorded in organization_providers for
//     (slack, team_id) — first install wins, same org Marketplace installs
//     and Sign-in-with-Slack already land in. If that org has its own
//     connection, use it.
//  2. Deterministic fallback (no provider row, or the home org's own
//     connection was deleted): the oldest connection for the team
//     (created_at ASC) across all orgs. A warning is logged when more than
//     one connection exists, since routing is then ambiguous.
//
// Callers are unchanged — every inbound handler already goes through this
// method or GetClient (which calls it), so fixing it here fixes routing
// everywhere.
func (s *Service) GetConnectionByTeamID(ctx context.Context, teamID string) (*models.Integration, error) {
	if provider, err := s.db.GetOrganizationProviderByProviderID(
		ctx, models.ProviderTypeSlack, teamID,
	); err == nil {
		conn, connErr := s.db.GetChannelByPropertyForOrg(
			ctx, provider.OrganizationUID, string(models.ConnectionTypeSlack), "team_id", teamID,
		)
		if connErr == nil {
			return conn, nil
		}

		if !errors.Is(connErr, sql.ErrNoRows) {
			return nil, connErr
		}
		// Home org has no connection of its own (e.g. deleted) — fall
		// through to the deterministic fallback below.
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID,
	)
	if err != nil {
		return nil, err
	}

	if len(conns) == 0 {
		return nil, ErrConnectionNotFound
	}

	if len(conns) > 1 {
		slog.WarnContext(ctx, "Slack team resolves to multiple connections with no home org — "+
			"routing to the oldest one; ambiguous until a home org is recorded",
			"team_id", teamID,
			"connection_count", len(conns),
			"resolved_connection_uid", conns[0].UID,
			"resolved_org_uid", conns[0].OrganizationUID,
		)
	}

	return conns[0], nil
}

// BuildInstallURL mints a fresh CSRF state and returns the Slack OAuth
// authorization URL the user should be redirected to. `source` (when set)
// is stashed in the state payload for install-source analytics on the
// callback side. `channelUID` and `orgSlug` are stashed when the install is
// triggered from a specific channel edit page, so the callback can update
// that channel directly instead of creating a new one.
func (s *Service) BuildInstallURL(ctx context.Context, source, channelUID, orgSlug string) (string, error) {
	payload := map[string]any{}
	if source != "" {
		payload[payloadKeySource] = source
	}
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

	redirectURI := s.cfg.Server.BaseURL + "/api/v1/integrations/slack/oauth"

	params := url.Values{}
	params.Set("client_id", s.cfg.Slack.ClientID)
	params.Set("scope", strings.Join(slackBotScopes, ","))
	params.Set("user_scope", strings.Join(slackUserScopes, ","))
	params.Set("redirect_uri", redirectURI)
	params.Set("state", nonce)

	return "https://slack.com/oauth/v2/authorize?" + params.Encode(), nil
}

// BuildOrgInstallURL is the authenticated, org-scoped counterpart to
// BuildInstallURL. orgUID/orgSlug come from the authenticated route context
// (never from client-controlled input), so org targeting cannot be forged
// the way the legacy `?org=` query param could. When channelUID is set, the
// channel must exist, be a Slack integration, and belong to orgUID —
// otherwise ErrConnectionNotFound is returned so the handler can respond
// 404 instead of letting an install take over a channel in another org.
func (s *Service) BuildOrgInstallURL(ctx context.Context, orgUID, orgSlug, channelUID string) (string, error) {
	if channelUID != "" {
		conn, err := s.db.GetChannel(ctx, channelUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrConnectionNotFound
			}

			return "", fmt.Errorf("get channel: %w", err)
		}

		if conn.OrganizationUID != orgUID || conn.Type != models.ConnectionTypeSlack || conn.DeletedAt != nil {
			return "", ErrConnectionNotFound
		}
	}

	return s.BuildInstallURL(ctx, "dashboard", channelUID, orgSlug)
}

// IssueExchangeCode persists a single-use code that the dashboard will
// trade in (server-to-server) for the freshly minted access/refresh tokens.
// The 60-second TTL is intentionally tight — the dashboard hits the
// exchange endpoint immediately after the post-install redirect.
func (s *Service) IssueExchangeCode(ctx context.Context, result *OAuthResult) (string, error) {
	payload := map[string]any{
		payloadKeyAccessToken:  result.AccessToken,
		payloadKeyRefreshToken: result.RefreshToken,
		payloadKeyExpiresIn:    result.ExpiresIn,
		payloadKeyOrgSlug:      result.OrgSlug,
		payloadKeyUserUID:      result.UserUID,
	}
	if result.ChannelUID != "" {
		payload[payloadKeyChannelUID] = result.ChannelUID
	}

	code, err := oauthstate.Generate(ctx, s.db, exchangeStateKind, payload, exchangeStateTTL)
	if err != nil {
		return "", fmt.Errorf("issue exchange code: %w", err)
	}

	return code, nil
}

// resolveResultChannelUID decides what OAuthResult.ChannelUID should be —
// the channel/connection the frontend (auth.slack.complete.tsx) navigates to
// after the exchange. For a channel-edit-page install (targetChannelUID
// set) it's that specific channel, which was just updated in place. For a
// dashboard-origin, org-scoped install with no specific channel (the
// "Install Slack app" tile CTA — targetOrgSlug set, targetChannelUID empty),
// it's connUID, the connection that was just created/updated in that org,
// so the user lands on it instead of the org home page. Marketplace
// installs (targetOrgSlug empty — no authenticated org context) return ""
// and land on the org home page for onboarding.
func resolveResultChannelUID(targetChannelUID, targetOrgSlug, connUID string) string {
	if targetChannelUID != "" {
		return targetChannelUID
	}

	if targetOrgSlug != "" {
		return connUID
	}

	return ""
}

// HandleOAuthCallback handles the OAuth callback from Slack.
// It validates the CSRF state up front, then creates/updates the integration
// connection and creates user and organization if needed.
func (s *Service) HandleOAuthCallback(ctx context.Context, code, state string) (*OAuthResult, error) {
	entry, err := oauthstate.Validate(ctx, s.db, installStateKind, state)
	if err != nil {
		return nil, ErrInvalidState
	}

	// Extract optional channel context stored when the install was triggered
	// from a specific channel edit page.
	var targetChannelUID, targetOrgSlug string
	if entry.Payload != nil {
		targetChannelUID, _ = entry.Payload[payloadKeyChannelUID].(string)
		targetOrgSlug, _ = entry.Payload[payloadKeyInstallOrg].(string)
	}

	oauthResp, userInfo, err := s.exchangeCodeAndFetchUser(ctx, code)
	if err != nil {
		return nil, err
	}

	// Resolve organization: when triggered from a channel page, use that
	// channel's org directly so the user stays in their existing workspace.
	// For marketplace installs (no channel context), find/create from Slack identity.
	org, orgName, err := s.resolveOrganizationForOAuth(ctx, targetOrgSlug, oauthResp, userInfo)
	if err != nil {
		return nil, err
	}

	// Find or create user
	user, err := s.findOrCreateUser(ctx, userInfo, oauthResp.Team.ID, orgName)
	if err != nil {
		return nil, fmt.Errorf("failed to find or create user: %w", err)
	}

	// Admission + session minting go through the shared chokepoint every
	// federated connector uses (auth.Service.JoinOrgViaLogin), so an install
	// can no longer mint a membership that bypasses the org's rules or the
	// MaxUsers cap. The team ID from the token exchange is passed as a
	// workspace attestation: the policy admits the installer when this org is
	// the one linked to that workspace, and otherwise leaves them pending
	// with a membership request.
	login, err := s.authService.CompleteOrgLogin(ctx, org, user,
		auth.WithSlackWorkspace(oauthResp.Team.ID), auth.WithLoginMethod(auth.SignupMethodSlack))
	if err != nil {
		return nil, fmt.Errorf("failed to complete organization login: %w", err)
	}

	// Create or update the integration connection. This happens even when the
	// installer was left pending: the bot credentials belong to the
	// organization, not to the human who clicked install, and dropping them
	// would leave the workspace with an app that cannot talk to SolidPing.
	// When triggered from a channel page, update THAT channel's settings
	// directly so the existing channel record becomes "connected" (gains a
	// team_id).
	var connUID string
	if targetChannelUID != "" {
		connUID, err = s.updateExistingChannel(ctx, targetChannelUID, oauthResp)
	} else {
		connUID, err = s.createOrUpdateConnection(ctx, org.UID, oauthResp)
	}

	if err != nil {
		return nil, err
	}

	resultChannelUID := resolveResultChannelUID(targetChannelUID, targetOrgSlug, connUID)

	// Auto-match org members to workspace users so the very first alert can
	// already mention the on-call person. Detached and best-effort: it makes
	// one Slack call per member, and the human is waiting on a browser
	// redirect — a slow or failing workspace must never break the install.
	s.runIdentitySync(ctx, org.Slug, connUID)

	slog.InfoContext(ctx, "Slack OAuth completed successfully",
		"org_uid", org.UID,
		"org_slug", org.Slug,
		"user_uid", user.UID,
		"user_email", user.Email,
		"team_id", oauthResp.Team.ID,
		"team_name", oauthResp.Team.Name,
		"membership_pending", login.Pending,
	)

	return &OAuthResult{
		ConnectionUID: connUID,
		ChannelUID:    resultChannelUID,
		AccessToken:   login.AccessToken,
		RefreshToken:  login.RefreshToken,
		ExpiresIn:     login.ExpiresIn,
		OrgSlug:       org.Slug,
		UserUID:       user.UID,
		Pending:       login.Pending,
	}, nil
}

// exchangeCodeAndFetchUser exchanges the OAuth code for tokens and fetches
// the authenticated user's identity via OpenID Connect.
func (s *Service) exchangeCodeAndFetchUser(
	ctx context.Context, code string,
) (*OAuthResponse, *OpenIDUserInfo, error) {
	oauthResp, err := ExchangeCode(
		ctx,
		s.oauthURL,
		s.cfg.Slack.ClientID,
		s.cfg.Slack.ClientSecret,
		code,
		"", // redirect_uri is optional for token exchange
	)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to exchange OAuth code", "error", err)

		return nil, nil, fmt.Errorf("%w: %w", ErrOAuthFailed, err)
	}

	userInfo, err := FetchOpenIDUserInfo(ctx, s.userInfoURL, oauthResp.AuthedUser.AccessToken)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"Failed to fetch user info from Slack via OpenID Connect",
			"error", err,
			"user_id", oauthResp.AuthedUser.ID,
		)

		return nil, nil, fmt.Errorf("%w: failed to fetch user info: %w", ErrOAuthFailed, err)
	}

	if userInfo.Email == "" {
		return nil, nil, ErrEmailRequired
	}

	return oauthResp, userInfo, nil
}

// resolveOrganizationForOAuth resolves the target organization during a Slack
// OAuth callback. When a targetOrgSlug is present (install triggered from a
// channel edit page), it fetches that org directly, falling back to the Slack
// workspace identity if the slug is not found. Otherwise it delegates to
// resolveOrganization to find or create from the Slack team.
func (s *Service) resolveOrganizationForOAuth(
	ctx context.Context,
	targetOrgSlug string,
	oauthResp *OAuthResponse,
	userInfo *OpenIDUserInfo,
) (*models.Organization, string, error) {
	if targetOrgSlug == "" {
		return s.resolveOrganization(ctx, oauthResp, userInfo)
	}

	org, err := s.db.GetOrganizationBySlug(ctx, targetOrgSlug)
	if err != nil {
		// Target org not found — fall back to Slack workspace identity.
		return s.resolveOrganization(ctx, oauthResp, userInfo)
	}

	return org, org.Name, nil
}

// updateExistingChannel writes the Slack bot credentials from oauthResp into
// the channel identified by channelUID. Used when install is triggered from
// the channel edit page so the existing channel becomes "connected".
func (s *Service) updateExistingChannel(
	ctx context.Context, channelUID string, oauthResp *OAuthResponse,
) (string, error) {
	// The row already exists, so this is not a "new integration": carry the
	// operator's current choices over instead of resetting them to defaults.
	choices := s.existingOperatorChoices(ctx, channelUID)

	settings := &models.SlackSettings{
		TeamID:            oauthResp.Team.ID,
		TeamName:          oauthResp.Team.Name,
		BotUserID:         oauthResp.BotUserID,
		AccessToken:       oauthResp.AccessToken,
		InstalledByUserID: oauthResp.AuthedUser.ID,
		Scopes:            strings.Split(oauthResp.Scope, ","),
		MentionOnCall:     choices.MentionOnCall,
		CommentIngestion:  choices.CommentIngestion,
	}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return "", fmt.Errorf("failed to convert settings: %w", err)
	}

	update := &models.IntegrationUpdate{Settings: &settingsMap}
	if err := s.db.UpdateChannel(ctx, channelUID, update); err != nil {
		return "", fmt.Errorf("failed to update channel %s: %w", channelUID, err)
	}

	return channelUID, nil
}

// IdentitySyncFn re-runs the member identity auto-match for one integration.
// Wired at server startup to integrations.Service.SyncIdentities — a function
// seam rather than a direct call because handlers/integrations already imports
// this package, so the dependency can only point one way.
type IdentitySyncFn func(ctx context.Context, orgSlug, integrationUID string) error

// SetIdentitySync installs the post-install member identity auto-match. Optional:
// with no sync wired, an install simply leaves the mapping empty until an admin
// presses "Re-sync" in the Slack panel.
func (s *Service) SetIdentitySync(fn IdentitySyncFn) {
	s.identitySync = fn
}

// runIdentitySync fires the auto-match without blocking the OAuth redirect.
// Every failure is logged and swallowed: a workspace that rate-limits us, or a
// bot missing users:read.email, must not turn a successful install into an
// error page.
func (s *Service) runIdentitySync(ctx context.Context, orgSlug, integrationUID string) {
	if s.identitySync == nil || orgSlug == "" || integrationUID == "" {
		return
	}

	// Detached from the request: the HTTP handler returns a redirect long
	// before a per-member Slack lookup loop finishes.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), identitySyncTimeout)

	go func() {
		defer cancel()

		if err := s.identitySync(syncCtx, orgSlug, integrationUID); err != nil {
			slog.WarnContext(syncCtx, "Slack member identity auto-match failed",
				"org_slug", orgSlug, "integration_uid", integrationUID, "error", err)
		}
	}()
}

// identitySyncTimeout bounds the detached post-install auto-match.
const identitySyncTimeout = 2 * time.Minute

// operatorSlackChoices are the SlackSettings fields an OPERATOR decides, as
// opposed to the ones OAuth hands us. Both re-install paths rebuild the
// settings blob from the OAuth response, so anything in here must be read off
// the stored row and carried over — a re-install must never flip a setting the
// operator already decided.
//
// Add a field here (not to one call site) when a new operator-owned Slack
// setting ships, or a reconnect will silently discard it.
type operatorSlackChoices struct {
	// MentionOnCall defaults to false: for a row that already exists, "no
	// opinion recorded" means the historical behavior (no mentions), which is
	// what spec 2026-08-12-03 asks for existing integrations.
	MentionOnCall bool
	// CommentIngestion defaults to "" (== explicit), the safe direction: a
	// pre-2026-08-15-08 row never opted into capturing every thread reply.
	// A workspace that DID opt into "all" keeps it across a reconnect.
	CommentIngestion string
}

// existingOperatorChoices reads the operator-owned settings off a stored Slack
// integration. Every failure path returns the zero value, which is the
// conservative default for each field.
func (s *Service) existingOperatorChoices(
	ctx context.Context, channelUID string,
) operatorSlackChoices {
	conn, err := s.db.GetChannel(ctx, channelUID)
	if err != nil || conn == nil {
		return operatorSlackChoices{}
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return operatorSlackChoices{}
	}

	return operatorSlackChoices{
		MentionOnCall:    settings.MentionOnCall,
		CommentIngestion: settings.CommentIngestion,
	}
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

// createOrUpdateConnection creates or updates an integration connection for
// the Slack team, scoped to orgUID. A workspace already connected to another
// org is untouched — the existing-connection lookup is org-scoped, so a
// second org installing the same Slack workspace gets its own row instead of
// silently taking over (or being redirected into) the first org's
// connection.
func (s *Service) createOrUpdateConnection(
	ctx context.Context, orgUID string, oauthResp *OAuthResponse,
) (string, error) {
	// Check if a connection already exists for this team IN THIS ORG.
	existingConn, err := s.db.GetChannelByPropertyForOrg(
		ctx, orgUID, string(models.ConnectionTypeSlack), "team_id", oauthResp.Team.ID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Create Slack settings.
	//
	// Operator-owned fields (mention_on_call, comment_ingestion) take their
	// NEW-integration defaults here and are overwritten with the stored choice
	// when the row already exists (spec 2026-08-12-03 resolved question 1, and
	// spec 2026-08-15-08): a re-install must never flip a setting the operator
	// already decided, and no stored row is ever backfilled.
	settings := &models.SlackSettings{
		TeamID:            oauthResp.Team.ID,
		TeamName:          oauthResp.Team.Name,
		BotUserID:         oauthResp.BotUserID,
		AccessToken:       oauthResp.AccessToken,
		InstalledByUserID: oauthResp.AuthedUser.ID,
		Scopes:            strings.Split(oauthResp.Scope, ","),
		MentionOnCall:     true,
		CommentIngestion:  models.SlackCommentIngestionExplicit,
	}

	if existingConn != nil {
		choices := s.existingOperatorChoices(ctx, existingConn.UID)
		settings.MentionOnCall = choices.MentionOnCall
		settings.CommentIngestion = choices.CommentIngestion
	}

	settingsMap, err := settings.ToJSONMap()
	if err != nil {
		return "", fmt.Errorf("failed to convert settings: %w", err)
	}

	if existingConn != nil {
		// Update existing connection
		update := &models.IntegrationUpdate{
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
	conn := models.NewIntegration(orgUID, models.ConnectionTypeSlack, oauthResp.Team.Name)
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
			settingsKeyTeamID: teamID,
			"team_name":       teamName,
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

	// Routed through this package's account-creation chokepoint so the
	// user_signed_up product event fires for Sign-in-with-Slack too.
	if err := createUserAndCapture(ctx, s.db, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	slog.InfoContext(ctx, "Created new user from Slack",
		"user_uid", user.UID,
		"user_email", user.Email,
		"slack_user_id", userInfo.Sub,
	)

	return user, nil
}

// CountInstalledTeams returns the number of distinct Slack workspaces
// (team_id) that currently have at least one integration connection —
// NOT the number of connection rows, since one workspace can now have a
// connection in several orgs (spec 2026-07-05-01). Used by the Socket Mode
// supervisor's status snapshot. Best-effort: returns (0, err) on failure;
// connections whose settings fail to parse are skipped rather than failing
// the whole count.
func (s *Service) CountInstalledTeams(ctx context.Context) (int, error) {
	slackType := models.ConnectionTypeSlack

	// Empty OrganizationUID lists across ALL orgs — this is a genuinely
	// global count, not scoped to one org.
	channels, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{
		Type: &slackType,
	})
	if err != nil {
		return 0, fmt.Errorf("list slack channels: %w", err)
	}

	teamIDs := make(map[string]struct{}, len(channels))

	for _, ch := range channels {
		settings, parseErr := models.SlackSettingsFromJSONMap(ch.Settings)
		if parseErr != nil {
			slog.WarnContext(ctx, "Skipping Slack channel with unparseable settings in team count",
				"connection_uid", ch.UID,
				"error", parseErr,
			)

			continue
		}

		if settings.TeamID == "" {
			continue
		}

		teamIDs[settings.TeamID] = struct{}{}
	}

	return len(teamIDs), nil
}

// HandleAppUninstalled handles the app_uninstalled event. Uninstalling the
// app in Slack revokes the bot token for the ENTIRE workspace, so every
// org's connection for this team_id shares the same now-dead credentials —
// all of them must be cleaned up, not just one, or the other orgs keep
// rendering a stale "connected" integration.
func (s *Service) HandleAppUninstalled(ctx context.Context, teamID string) error {
	conns, err := s.db.ListChannelsByProperty(
		ctx, string(models.ConnectionTypeSlack), "team_id", teamID,
	)
	if err != nil {
		return err
	}

	for _, conn := range conns {
		if err := s.db.DeleteChannel(ctx, conn.UID); err != nil {
			return fmt.Errorf("failed to delete connection %s: %w", conn.UID, err)
		}

		slog.InfoContext(ctx, "Deleted Slack connection due to app_uninstalled",
			"team_id", teamID,
			"connection_uid", conn.UID,
			"org_uid", conn.OrganizationUID,
		)
	}

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

	return s.newAPIClient(settings.AccessToken), nil
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
	client := s.newAPIClient(settings.AccessToken)
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

	update := &models.IntegrationUpdate{
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

// displayName is the label the dashboard shows for a DM target (real name,
// falling back to the Slack username) and therefore the picker sort key.
func (u SlackDestinationUser) displayName() string {
	if u.RealName != "" {
		return u.RealName
	}

	return u.Name
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

	client := s.newAPIClient(settings.AccessToken)

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

	// Slack returns both lists in arbitrary (roughly creation) order and the
	// dashboard picker mirrors it — sort so entries are findable.
	slices.SortFunc(channels, func(a, b SlackChannel) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	slices.SortFunc(users, func(a, b SlackDestinationUser) int {
		return strings.Compare(strings.ToLower(a.displayName()), strings.ToLower(b.displayName()))
	})

	return &SlackDestinationsResponse{
		Channels: channels,
		Users:    users,
	}, nil
}
