// Package integrations provides HTTP handlers for integration management
// (Slack, Discord, webhook, email, etc.). The legacy `integration_connections`
// table backs this domain — see spec 2026-05-07-03 for the rename rationale.
package integrations

import (
	"context"
	crypto_rand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrConnectionNotFound is returned when a connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrInvalidConnectionType is returned when an invalid connection type is provided.
	ErrInvalidConnectionType = errors.New("invalid connection type")
	// ErrFreeboxPairingFailed is returned when the Freebox pairing flow
	// fails (transport error, denial, timeout). The wrapped error keeps
	// the underlying cause so the handler can surface a useful message.
	ErrFreeboxPairingFailed = errors.New("freebox pairing failed")
	// ErrFreeboxNotPairing is returned when a status-poll is requested
	// against a channel that isn't currently in the pairing state.
	ErrFreeboxNotPairing = errors.New("freebox channel is not in pairing state")
	// ErrFreeboxTypeMismatch is returned when a pairing endpoint targets
	// a non-Freebox channel.
	ErrFreeboxTypeMismatch = errors.New("channel is not a freebox connection")
	// ErrSlackManualCreate is returned when a Slack channel is created via the
	// manual create endpoint. Slack channels can only originate from the OAuth
	// install flow (which writes the bot token); a manually-created stub has no
	// token and is permanently broken.
	ErrSlackManualCreate = errors.New("slack channels are added by installing the Slack app")
	// ErrMSTeamsBotManualCreate is returned when a msteams-bot channel is
	// created through the generic API. Like Slack, this type carries a
	// provider-side identity that must be PROVEN, not asserted: its tenant_id
	// decides whose Teams conversations the connection receives, so allowing
	// a caller to supply one would let any org claim any Microsoft 365
	// tenant. Creation goes through
	// POST /orgs/:org/integrations/msteams/link-code, which issues a
	// one-time code that must be redeemed from inside the tenant itself.
	ErrMSTeamsBotManualCreate = errors.New(
		"microsoft teams bot channels are added with a link code, not created directly")
	// ErrMSTeamsBotUnknownDestination is returned when a destination
	// selection names a conversation the bot was never added to. The
	// conversation id is not proof of access — see
	// models.MSTeamsBotSettings.HasDestination.
	ErrMSTeamsBotUnknownDestination = errors.New(
		"the selected Microsoft Teams channel is not one this bot has been added to")
	// ErrNotWebhookChannel is returned when a webhook-only operation
	// (rotate-secret) targets a channel of a different type.
	ErrNotWebhookChannel = errors.New("channel is not a webhook connection")
	// ErrIntegrationNotNotifiable is returned when a test-notification is
	// requested for an integration whose type cannot send notifications
	// (e.g. a data source like Freebox).
	ErrIntegrationNotNotifiable = errors.New("integration type cannot send notifications")
	// ErrInvalidSettings is returned when a connection's type-specific settings
	// fail validation (e.g. a malformed Twilio account SID or phone number).
	ErrInvalidSettings = errors.New("invalid connection settings")
)

// Signing-secret lifecycle constants (Standard Webhooks). The secret format is
// `whsec_<base64url-unpadded-32-bytes>` (Svix convention). Keys mirror the
// names used by the WebhookSender so the public/private split stays consistent.
const (
	webhookSecretPrefix              = "whsec_"
	webhookSecretBytes               = 32
	webhookRotationGrace             = 24 * time.Hour
	settingsKeySigningSecret         = "signingSecret"
	settingsKeySigningSecretPrevious = "signingSecretPrevious"
	settingsKeySigningSecretExpiry   = "signingSecretPreviousExpiry"
)

// generateWebhookSecret returns a fresh `whsec_<base64url-unpadded-32-bytes>`
// signing secret using crypto/rand.
func generateWebhookSecret() (string, error) {
	raw := make([]byte, webhookSecretBytes)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating webhook secret: %w", err)
	}

	return webhookSecretPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ensureWebhookCreateSecret returns a settings map guaranteed to contain a
// signing secret, generating one when absent. Extracted from CreateIntegration to
// keep that function below the cyclomatic-complexity threshold.
func ensureWebhookCreateSecret(settings map[string]any) (map[string]any, error) {
	if settings == nil {
		settings = map[string]any{}
	}

	if existing, ok := settings[settingsKeySigningSecret].(string); ok && existing != "" {
		return settings, nil
	}

	secret, err := generateWebhookSecret()
	if err != nil {
		return nil, err
	}

	settings[settingsKeySigningSecret] = secret

	return settings, nil
}

// Service provides business logic for connection management.
type Service struct {
	db db.Service
	// creds is always non-nil — its .Enabled() reports whether a master
	// key is configured. Used to encrypt secret fields in Settings.
	creds credentials.Service
	// registry and appConfig are used by TestIntegration to build the same
	// JobContext the notification job runner uses, so senders that depend on
	// shared services (email, web push) work from the test path too. They may
	// be nil in unit tests that only exercise sender types which ignore the
	// JobContext (webhook, discord, …).
	registry  *services.Registry
	appConfig *config.Config
	// slackIdentityClient builds the Slack lookup client used by the member
	// identity auto-match. Per-instance seam (see SetSlackIdentityClient) so
	// parallel tests never race on a shared package-level override.
	slackIdentityClient SlackIdentityClientFactory
}

// NewService creates a new connections service. registry and cfg may be nil
// when test-notification dispatch is not required (e.g. unit tests).
func NewService(
	dbService db.Service,
	creds credentials.Service,
	registry *services.Registry,
	cfg *config.Config,
) *Service {
	return &Service{
		db:        dbService,
		creds:     creds,
		registry:  registry,
		appConfig: cfg,
	}
}

// IntegrationResponse represents a connection in API responses.
type IntegrationResponse struct {
	UID       string         `json:"uid"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	IsDefault bool           `json:"isDefault"`
	Settings  map[string]any `json:"settings,omitempty"`
	// SettingsPrivateKeys lists which top-level Settings keys are stored
	// encrypted (and therefore stripped from the Settings map in the
	// response). Lets the dashboard render placeholder pills like
	// "•••• access_token" without echoing the secret value.
	SettingsPrivateKeys []string  `json:"settingsPrivateKeys,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// ListConnectionsResponse represents the response for listing connections.
type ListConnectionsResponse struct {
	Data []*IntegrationResponse `json:"data"`
}

// CreateIntegrationRequest represents the request to create a connection.
type CreateIntegrationRequest struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// UpdateIntegrationRequest represents the request to update a connection.
type UpdateIntegrationRequest struct {
	Name      *string        `json:"name,omitempty"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// toResponse converts a model to a response. The Settings map is the
// already-public side: secret keys live in SettingsPrivate (encrypted) and
// are exposed only as names via SettingsPrivateKeys so the dashboard can
// show placeholder pills.
func toResponse(conn *models.Integration, includeSettings bool) *IntegrationResponse {
	resp := &IntegrationResponse{
		UID:       conn.UID,
		Type:      string(conn.Type),
		Name:      conn.Name,
		Enabled:   conn.Enabled,
		IsDefault: conn.IsDefault,
		CreatedAt: conn.CreatedAt,
		UpdatedAt: conn.UpdatedAt,
	}

	secretSet := map[string]struct{}{}
	for _, k := range credentials.ConnectionSecretFields(conn.Type) {
		secretSet[k] = struct{}{}
	}

	// Defense-in-depth: track any declared-secret key that was (wrongly) found
	// in the public settings so it is redacted AND advertised, even for a
	// not-yet-migrated row whose storage split never ran.
	advertised := map[string]struct{}{}

	if includeSettings && conn.Settings != nil {
		settings := make(map[string]any, len(conn.Settings))
		for key, value := range conn.Settings {
			if _, isSecret := secretSet[key]; isSecret {
				advertised[key] = struct{}{}

				continue
			}
			settings[key] = value
		}
		resp.Settings = settings
	}

	if conn.SettingsPrivateKeys != nil && *conn.SettingsPrivateKeys != "" {
		var keys []string
		if err := json.Unmarshal([]byte(*conn.SettingsPrivateKeys), &keys); err == nil {
			for _, k := range keys {
				advertised[k] = struct{}{}
			}
		}
	}

	if len(advertised) > 0 {
		resp.SettingsPrivateKeys = credentials.SortedKeys(mapKeysToAny(advertised))
	}

	return resp
}

// mapKeysToAny adapts a set to the map[string]any that credentials.SortedKeys
// expects — a tiny bridge so the private-key list stays sorted/stable.
func mapKeysToAny(set map[string]struct{}) map[string]any {
	out := make(map[string]any, len(set))
	for k := range set {
		out[k] = struct{}{}
	}

	return out
}

// ListIntegrations returns all connections for an organization.
func (s *Service) ListIntegrations(
	ctx context.Context, orgSlug string, connType *string,
) (*ListConnectionsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	filter := &models.ListIntegrationsFilter{
		OrganizationUID: org.UID,
	}

	if connType != nil {
		ct := models.ConnectionType(*connType)
		filter.Type = &ct
	}

	connections, err := s.db.ListChannels(ctx, filter)
	if err != nil {
		return nil, err
	}

	response := &ListConnectionsResponse{
		Data: make([]*IntegrationResponse, 0, len(connections)),
	}

	for _, conn := range connections {
		response.Data = append(response.Data, toResponse(conn, false))
	}

	return response, nil
}

// GetIntegration returns a connection by UID.
func (s *Service) GetIntegration(
	ctx context.Context, orgSlug, connectionUID string,
) (*IntegrationResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	resp := toResponse(conn, true)

	// Webhook signing secrets are intentionally retrievable (not one-time
	// reveals) so the dashboard can show them. Decrypt and surface them on the
	// response settings for webhook channels.
	if conn.Type == models.ConnectionTypeWebhook {
		if err := s.injectSigningSecrets(ctx, conn, resp); err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// injectSigningSecrets decrypts a webhook channel's signing secrets and adds
// them to the response Settings, removing them from SettingsPrivateKeys so the
// dashboard renders the actual value rather than a placeholder pill.
func (s *Service) injectSigningSecrets(
	ctx context.Context, conn *models.Integration, resp *IntegrationResponse,
) error {
	effective, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return err
	}

	if resp.Settings == nil {
		resp.Settings = map[string]any{}
	}

	for _, key := range []string{settingsKeySigningSecret, settingsKeySigningSecretPrevious} {
		if v, ok := effective[key].(string); ok && v != "" {
			resp.Settings[key] = v
		}
	}

	if len(resp.SettingsPrivateKeys) > 0 {
		filtered := make([]string, 0, len(resp.SettingsPrivateKeys))
		for _, k := range resp.SettingsPrivateKeys {
			if k == settingsKeySigningSecret || k == settingsKeySigningSecretPrevious {
				continue
			}

			filtered = append(filtered, k)
		}

		resp.SettingsPrivateKeys = filtered
	}

	return nil
}

// CreateIntegration creates a new connection.
func (s *Service) CreateIntegration(
	ctx context.Context, orgSlug string, req CreateIntegrationRequest,
) (*IntegrationResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	// Validate connection type + per-type settings.
	connType := models.ConnectionType(req.Type)
	if err := validateConnectionType(connType, req.Settings); err != nil {
		return nil, err
	}

	if err := s.checkCreateTypeConstraints(ctx, connType, req.Settings); err != nil {
		return nil, err
	}

	conn := models.NewIntegration(org.UID, connType, req.Name)

	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}

	if req.IsDefault != nil {
		conn.IsDefault = *req.IsDefault
	}

	// Webhook channels always get a signing secret on creation so the first
	// delivery is signed without an auto-generation round-trip.
	if connType == models.ConnectionTypeWebhook {
		settings, secErr := ensureWebhookCreateSecret(req.Settings)
		if secErr != nil {
			return nil, secErr
		}

		req.Settings = settings
	}

	if err := s.applySettingsEncryption(ctx, conn, req.Settings); err != nil {
		return nil, err
	}

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, err
	}

	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		activation.SourceAPI, "", models.JSONMap{
			"channel_uid":  conn.UID,
			"channel_name": conn.Name,
			"channel_type": string(conn.Type),
		})

	// Record what KIND of integration was wired up, never its settings — a
	// connection's settings are exactly where the bot tokens, webhook URLs and
	// Twilio auth tokens live.
	audit.Record(ctx, s.db, org.UID, models.EventTypeIntegrationCreated,
		auditTarget(conn), models.JSONMap{
			auditKeyIntegrationType: string(conn.Type),
			"enabled":               conn.Enabled,
		})

	resp := toResponse(conn, true)

	if conn.Type == models.ConnectionTypeWebhook {
		if err := s.injectSigningSecrets(ctx, conn, resp); err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// validConnectionTypes is the set of accepted connection types.
//
//nolint:gochecknoglobals // constant lookup set.
var validConnectionTypes = map[models.ConnectionType]bool{
	models.ConnectionTypeSlack:      true,
	models.ConnectionTypeDiscord:    true,
	models.ConnectionTypeWebhook:    true,
	models.ConnectionTypeEmail:      true,
	models.ConnectionTypeGoogleChat: true,
	models.ConnectionTypeMattermost: true,
	models.ConnectionTypeMSTeams:    true,
	models.ConnectionTypeMSTeamsBot: true,
	models.ConnectionTypeNtfy:       true,
	models.ConnectionTypeMatrix:     true,
	models.ConnectionTypePagerduty:  true,
	models.ConnectionTypePushover:   true,
	models.ConnectionTypeFreebox:    true,
	models.ConnectionTypeWebPush:    true,
	models.ConnectionTypeKubernetes: true,
	models.ConnectionTypeTwilio:     true,
}

// validateConnectionType checks the type is known and, for types with per-type
// settings invariants (Twilio), validates those too.
func validateConnectionType(connType models.ConnectionType, settings models.JSONMap) error {
	if !validConnectionTypes[connType] {
		return ErrInvalidConnectionType
	}

	if connType == models.ConnectionTypeTwilio {
		// A bad SID or non-E.164 number silently no-ops at send time otherwise.
		return validateTwilioSettings(settings)
	}

	return nil
}

// checkCreateTypeConstraints enforces the per-type rules that gate whether a
// POST is even allowed to proceed for connType: Slack and Teams-bot channels
// can only originate from their respective provider-driven install flows, and
// a Twilio connection must have its credentials verified against Twilio
// before it is ever persisted.
func (s *Service) checkCreateTypeConstraints(
	ctx context.Context, connType models.ConnectionType, settings models.JSONMap,
) error {
	switch connType { //nolint:exhaustive // only Slack/Teams-bot/Twilio have creation-time constraints.
	case models.ConnectionTypeSlack:
		// Slack channels can only be created by the OAuth install flow (which
		// calls s.db.CreateChannel directly and writes the bot token). A
		// manually-created Slack channel has no token and is permanently
		// broken, so reject it here.
		return ErrSlackManualCreate
	case models.ConnectionTypeMSTeamsBot:
		return ErrMSTeamsBotManualCreate
	case models.ConnectionTypeTwilio:
		// A POST always sets the account SID / auth token (both required by
		// validateTwilioSettings above), so creation always verifies them
		// against Twilio before the connection is persisted.
		return s.verifyTwilioOnCreate(ctx, settings)
	default:
		return nil
	}
}

// validateTwilioSettings enforces the Twilio connection's settings invariants:
// an AC… account SID, a non-empty auth token, exactly one of from_number /
// messaging_service_sid, and E.164 for every configured number. Called against
// the effective (post-PATCH-merge) settings so a partial update is validated
// as a whole.
func validateTwilioSettings(settings models.JSONMap) error {
	parsed, err := models.TwilioSettingsFromJSONMap(settings)
	if err != nil {
		return fmt.Errorf("%w: malformed twilio settings", ErrInvalidSettings)
	}

	if !twilio.ValidAccountSID(parsed.AccountSID) {
		return fmt.Errorf("%w: account_sid must be a Twilio Account SID (AC…)", ErrInvalidSettings)
	}

	if strings.TrimSpace(parsed.AuthToken) == "" {
		return fmt.Errorf("%w: auth_token is required", ErrInvalidSettings)
	}

	hasFrom := parsed.FromNumber != ""
	hasMSS := parsed.MessagingServiceSID != ""
	if hasFrom == hasMSS {
		return fmt.Errorf("%w: exactly one of from_number or messaging_service_sid is required", ErrInvalidSettings)
	}

	if hasFrom && !twilio.ValidE164(parsed.FromNumber) {
		return fmt.Errorf("%w: from_number must be E.164 (e.g. +15551234567)", ErrInvalidSettings)
	}

	if parsed.VoiceFromNumber != "" && !twilio.ValidE164(parsed.VoiceFromNumber) {
		return fmt.Errorf("%w: voice_from_number must be E.164", ErrInvalidSettings)
	}

	for _, n := range parsed.ToNumbers {
		if !twilio.ValidE164(n) {
			return fmt.Errorf("%w: to_numbers entry %q must be E.164", ErrInvalidSettings, n)
		}
	}

	if !twilio.ValidRegion(parsed.Region) {
		return fmt.Errorf(
			"%w: region must be empty or match ^[a-z]{2}[0-9]+$ (e.g. us1, ie1, au1)", ErrInvalidSettings)
	}

	return nil
}

// verifyTwilioCredentials is the credential-verification seam used before a
// Twilio connection write that sets or changes the account SID, auth token,
// or region. Overridden in tests to avoid a live Twilio call.
//
//nolint:gochecknoglobals // test seam for the outbound Twilio credential check.
var verifyTwilioCredentials = twilio.VerifyCredentials

// verifyTwilioWrite makes one live authenticated call to Twilio, against the
// region resolved from parsed.Region, to confirm the account SID / auth token
// pair actually works before the caller persists the connection. Twilio
// rejecting the credentials and Twilio being unreachable are reported as
// distinct errors so the operator knows which situation they are in — both
// refuse the write.
//
// Skipped entirely in test-run mode (SP_RUNMODE=test): dev-test and the
// Playwright E2E suite create Twilio connections with placeholder
// credentials and no real Twilio account to verify against, and there is no
// way to intercept this server-side HTTP call from a browser-driven test.
// Production (RunMode == "") always verifies.
func (s *Service) verifyTwilioWrite(ctx context.Context, parsed *models.TwilioSettings) error {
	if s.appConfig != nil && s.appConfig.RunMode == "test" {
		return nil
	}

	region := parsed.Region
	if region == "" {
		region = "us1"
	}

	baseURL := twilio.BaseURLForRegion(parsed.Region)

	err := verifyTwilioCredentials(ctx, parsed.AccountSID, parsed.AuthToken, baseURL)
	if err == nil {
		return nil
	}

	if errors.Is(err, twilio.ErrCredentialsRejected) {
		return fmt.Errorf(
			"%w: Twilio rejected these credentials for region %s — "+
				"account SID and auth token are region-scoped, check you copied them from the %s account",
			ErrInvalidSettings, region, region)
	}

	return fmt.Errorf(
		"%w: could not verify Twilio credentials for region %s "+
			"(Twilio was unreachable, timed out, or returned an error) — the connection was not saved",
		ErrInvalidSettings, region)
}

// verifyTwilioOnCreate parses the create-request settings and verifies them
// against Twilio before the caller persists the new connection.
func (s *Service) verifyTwilioOnCreate(ctx context.Context, settings models.JSONMap) error {
	parsed, err := models.TwilioSettingsFromJSONMap(settings)
	if err != nil {
		return fmt.Errorf("%w: malformed twilio settings", ErrInvalidSettings)
	}

	return s.verifyTwilioWrite(ctx, parsed)
}

// verifyTwilioOnUpdate validates the merged (post-PATCH) Twilio settings and,
// only when the merge actually changes the account SID, auth token, or
// region relative to what's currently stored, re-verifies them against
// Twilio before the caller persists the update.
//
// Comparing resolved *values* (not raw request key presence) is deliberate:
// non-secret settings fields use PATCH's wholesale-replace semantics (see
// credentials.MergePatch) — a caller resending the same account_sid
// alongside a changed from_number would otherwise look like it "touched"
// account_sid even though nothing about the credentials changed.
func (s *Service) verifyTwilioOnUpdate(ctx context.Context, existing, merged map[string]any) error {
	if vErr := validateTwilioSettings(models.JSONMap(merged)); vErr != nil {
		return vErr
	}

	before, err := models.TwilioSettingsFromJSONMap(models.JSONMap(existing))
	if err != nil {
		return fmt.Errorf("%w: malformed twilio settings", ErrInvalidSettings)
	}

	after, err := models.TwilioSettingsFromJSONMap(models.JSONMap(merged))
	if err != nil {
		return fmt.Errorf("%w: malformed twilio settings", ErrInvalidSettings)
	}

	if before.AccountSID == after.AccountSID && before.AuthToken == after.AuthToken && before.Region == after.Region {
		return nil
	}

	return s.verifyTwilioWrite(ctx, after)
}

// UpdateIntegration updates a connection.
func (s *Service) UpdateIntegration(
	ctx context.Context, orgSlug, connectionUID string, req UpdateIntegrationRequest,
) (*IntegrationResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	update := &models.IntegrationUpdate{
		Name:      req.Name,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
	}

	if req.Settings != nil {
		if setErr := s.applyUpdateSettings(ctx, conn, req.Settings, update); setErr != nil {
			return nil, setErr
		}
	}

	if updateErr := s.db.UpdateChannel(ctx, connectionUID, update); updateErr != nil {
		return nil, updateErr
	}

	// Fetch updated connection
	updated, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		return nil, err
	}

	changed, safe := audit.Changes(auditSnapshot(conn), auditSnapshot(updated))

	var extraFields []string
	if req.Settings != nil {
		// The settings map is the connection's credential store. The audit
		// trail records THAT it was rewritten and which top-level keys the
		// request named — never any value, and never the merged result.
		extraFields = append(extraFields, "settings")
	}

	if len(changed) > 0 || len(extraFields) > 0 {
		audit.Record(ctx, s.db, org.UID, models.EventTypeIntegrationUpdated,
			auditTarget(updated), audit.ChangePayload(changed, safe, extraFields, models.JSONMap{
				auditKeyIntegrationType: string(updated.Type),
				"settings_keys_touched": settingsKeyNames(req.Settings),
			}))
	}

	return toResponse(updated, true), nil
}

// auditKeyIntegrationType names the audit payload field carrying the
// connection's type.
const auditKeyIntegrationType = "integration_type"

// auditTarget names an integration connection for the audit trail.
func auditTarget(conn *models.Integration) audit.Target {
	return audit.Target{Type: "integration", UID: conn.UID, Name: conn.Name}
}

// auditSnapshot is the scalar shape the audit trail diffs an update against.
// Settings are deliberately absent: the whole point of the redaction rule is
// that a connection's settings never reach the trail, not even as a diff.
func auditSnapshot(conn *models.Integration) map[string]any {
	return map[string]any{
		"name":       conn.Name,
		"enabled":    conn.Enabled,
		"is_default": conn.IsDefault,
	}
}

// settingsKeyNames returns the sorted top-level KEY NAMES a settings PATCH
// carried. Names only — "the auth_token was rewritten" is an audit fact worth
// having, and is exactly as much as may be stored about it.
func settingsKeyNames(settings models.JSONMap) []string {
	if len(settings) == 0 {
		return nil
	}

	names := make([]string, 0, len(settings))
	for key := range settings {
		names = append(names, key)
	}

	sort.Strings(names)

	return names
}

// applyUpdateSettings PATCH-merges the incoming settings over the existing
// (decrypted) settings, validates per-type invariants, re-encrypts, and writes
// the resulting public/private split onto update. Secret keys absent from the
// incoming map are preserved; explicit null/empty clears them.
func (s *Service) applyUpdateSettings(
	ctx context.Context, conn *models.Integration, reqSettings models.JSONMap, update *models.IntegrationUpdate,
) error {
	existing, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return err
	}

	secrets := credentials.ConnectionSecretFields(conn.Type)
	merged := credentials.MergePatch(existing, reqSettings, secrets)

	// The Teams bot's provider-side identity is server-owned: it is written
	// only from signature-verified Bot Framework activities. Restoring those
	// keys from the stored settings makes them immutable through this API, so
	// the creation guard above cannot be side-stepped by PATCHing a
	// tenant_id (or a forged destination) onto an existing connection.
	if conn.Type == models.ConnectionTypeMSTeamsBot {
		restoreMSTeamsBotServerFields(existing, merged)

		if vErr := validateMSTeamsBotDestination(merged); vErr != nil {
			return vErr
		}
	}

	// Validate the merged (post-PATCH) settings so a partial update can't leave
	// a Twilio connection in an unsendable state, and re-verify against Twilio
	// when the merge actually changes a credential-shaped field (renaming the
	// connection or changing the from-number must not trigger a live call).
	if conn.Type == models.ConnectionTypeTwilio {
		if vErr := s.verifyTwilioOnUpdate(ctx, existing, merged); vErr != nil {
			return vErr
		}
	}

	if encErr := s.applySettingsEncryption(ctx, conn, merged); encErr != nil {
		return encErr
	}

	settings := conn.Settings
	update.Settings = &settings
	update.SettingsPrivate = conn.SettingsPrivate
	update.SettingsPrivateKeys = conn.SettingsPrivateKeys
	update.ClearSettingsPrivate = conn.SettingsPrivate == nil

	return nil
}

// msTeamsBotServerOwnedKeys are the msteams-bot settings keys that only the
// inbound Bot Framework path may write. Everything else on the connection
// (the selected default destination) stays client-settable so the dashboard
// picker keeps working.
//
//nolint:gochecknoglobals // constant key list
var msTeamsBotServerOwnedKeys = []string{
	"tenant_id",
	"tenant_name",
	"bot_id",
	"app_id",
	"service_url",
	"destinations",
	"installed_by_user_id",
	"uninstalled_at",
}

// validateMSTeamsBotDestination rejects a default-destination selection that
// does not name one of the connection's captured conversation references.
//
// The destination keys stay client-writable (the dashboard picker writes
// them), so this is what stops a caller from simply typing in another
// tenant's conversation id and having the shared bot credential post there.
// The in-band `config default-channel` command enforces the same rule via
// Service.SetDefaultDestination.
func validateMSTeamsBotDestination(merged map[string]any) error {
	selected, _ := merged["channel_id"].(string)
	if selected == "" {
		return nil
	}

	settings, err := models.MSTeamsBotSettingsFromJSONMap(models.JSONMap(merged))
	if err != nil {
		return fmt.Errorf("parsing microsoft teams bot settings: %w", err)
	}

	if !settings.HasDestination(selected) {
		return ErrMSTeamsBotUnknownDestination
	}

	return nil
}

// restoreMSTeamsBotServerFields overwrites the server-owned keys in merged
// with their stored values (deleting them when unset), discarding whatever
// the client sent.
func restoreMSTeamsBotServerFields(existing, merged map[string]any) {
	for _, key := range msTeamsBotServerOwnedKeys {
		if stored, ok := existing[key]; ok {
			merged[key] = stored

			continue
		}

		delete(merged, key)
	}
}

// applySettingsEncryption splits Settings into public/private using the
// connection-type's declared secret keys and writes Settings + SettingsPrivate
// + SettingsPrivateKeys onto the connection. Secrets are ALWAYS split out of
// the public Settings column (spec 2026-07-18-06): an AES-GCM envelope when a
// master key is configured, a plaintext envelope (no-key self-hosted fallback)
// otherwise. The public Settings column never carries a secret value.
func (s *Service) applySettingsEncryption(
	ctx context.Context, conn *models.Integration, effective map[string]any,
) error {
	if effective == nil {
		effective = map[string]any{}
	}

	secrets := credentials.ConnectionSecretFields(conn.Type)
	public, private := credentials.SplitConfig(effective, secrets)
	conn.Settings = models.JSONMap(public)

	if len(private) == 0 {
		conn.SettingsPrivate = nil
		conn.SettingsPrivateKeys = nil

		return nil
	}

	// Secrets are ALWAYS split out of the public `settings` column and stored in
	// `settings_private`, in every mode — the public column must never carry a
	// secret value (the leak this fixes). With a master key we store an AES-GCM
	// envelope; without one, a clearly-marked plaintext envelope (the documented
	// V1 self-hosted fallback). `settings_private_keys` is set in both modes so
	// the dashboard renders placeholder pills.
	var (
		envelope string
		err      error
	)

	if s.creds.Enabled() {
		envelope, err = s.creds.EncryptForOrg(ctx, conn.OrganizationUID, private)
		if err != nil {
			return fmt.Errorf("encrypt connection settings: %w", err)
		}
	} else {
		envelope, err = credentials.SealPlaintext(private)
		if err != nil {
			return fmt.Errorf("seal plaintext connection settings: %w", err)
		}
	}

	conn.SettingsPrivate = &envelope

	keysJSON, err := json.Marshal(credentials.SortedKeys(private))
	if err != nil {
		return fmt.Errorf("marshal settings private keys: %w", err)
	}

	keysStr := string(keysJSON)
	conn.SettingsPrivateKeys = &keysStr

	return nil
}

// loadDecryptedSettings returns the merged plaintext effective Settings
// of a connection. Used by the PATCH path so secret-preservation has the
// existing values to merge into.
func (s *Service) loadDecryptedSettings(
	ctx context.Context, conn *models.Integration,
) (map[string]any, error) {
	if conn.SettingsPrivate == nil || *conn.SettingsPrivate == "" {
		out := make(map[string]any, len(conn.Settings))
		for k, v := range conn.Settings {
			out[k] = v
		}

		return out, nil
	}

	// A plaintext envelope opens with no master key; only AES-GCM / sealed
	// envelopes require one. Gate the disabled error on that so a no-key
	// self-hosted PATCH can still read (and preserve) its own secrets.
	if credentials.RequiresKey(*conn.SettingsPrivate) && !s.creds.Enabled() {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, credentials.ErrDisabled)
	}

	private, err := s.creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, err)
	}

	out := make(map[string]any, len(conn.Settings)+len(private))
	for k, v := range conn.Settings {
		out[k] = v
	}

	for k, v := range private {
		out[k] = v
	}

	return out, nil
}

// StartFreeboxPairingRequest is the body for the start-pairing endpoint.
type StartFreeboxPairingRequest struct {
	// Name is used for the IntegrationConnection row and for the
	// device_name shown on the Freebox LCD prompt. Optional — defaults
	// to "SolidPing" if absent.
	Name string `json:"name,omitempty"`
	// BaseURL of the Freebox API. Defaults to
	// "http://mafreebox.freebox.fr" when empty.
	BaseURL string `json:"baseUrl,omitempty"`
}

// FreeboxPairingResponse is the body returned by the start-pairing
// endpoint.
type FreeboxPairingResponse struct {
	ConnectionUID string `json:"connectionUid"`
	TrackID       int    `json:"trackId"`
	Status        string `json:"status"`
}

// FreeboxPairingStatusResponse is the body returned by the
// poll-status endpoint.
type FreeboxPairingStatusResponse struct {
	Status string `json:"status"`
}

// StartFreeboxPairing kicks off a Freebox pairing flow and persists the
// resulting permanent app_token (encrypted) onto a new IntegrationConnection
// row. The connection starts in `Status = "pairing"` until the operator
// approves the prompt on the Freebox LCD.
func (s *Service) StartFreeboxPairing(
	ctx context.Context, orgSlug string, req StartFreeboxPairingRequest,
) (*FreeboxPairingResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	settings := &models.FreeboxSettings{
		BaseURL:    req.BaseURL,
		AppID:      freebox.DefaultAppID,
		DeviceName: req.Name,
		Status:     models.FreeboxStatusPairing,
	}
	if settings.BaseURL == "" {
		settings.BaseURL = freebox.DefaultBaseURL
	}
	if settings.DeviceName == "" {
		settings.DeviceName = freebox.DefaultDeviceName
	}

	authResult, err := freebox.StartPairing(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFreeboxPairingFailed, err)
	}

	settings.TrackID = authResult.TrackID

	channelName := req.Name
	if channelName == "" {
		channelName = "Freebox"
	}

	conn := models.NewIntegration(org.UID, models.ConnectionTypeFreebox, channelName)

	effective, err := mergeFreeboxSettings(settings, &models.FreeboxPrivateSettings{
		AppToken: authResult.AppToken,
	})
	if err != nil {
		return nil, err
	}

	if err := s.applySettingsEncryption(ctx, conn, effective); err != nil {
		return nil, err
	}

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, err
	}

	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		activation.SourceAPI, "", models.JSONMap{
			"channel_uid":  conn.UID,
			"channel_name": conn.Name,
			"channel_type": string(conn.Type),
		})

	return &FreeboxPairingResponse{
		ConnectionUID: conn.UID,
		TrackID:       authResult.TrackID,
		Status:        models.FreeboxStatusPairing,
	}, nil
}

// CheckFreeboxPairingStatus polls the Freebox once for the current
// pairing status and reconciles the IntegrationConnection record:
//
//   - granted → clears TrackID, sets Status = granted
//   - denied / timeout → sets Status accordingly, leaves the row in
//     place so the operator can see the failure (and re-pair from the UI)
//   - pending / unknown → unchanged
func (s *Service) CheckFreeboxPairingStatus(
	ctx context.Context, orgSlug, connectionUID string,
) (*FreeboxPairingStatusResponse, error) {
	conn, settings, err := s.loadPairingChannel(ctx, orgSlug, connectionUID)
	if err != nil {
		return nil, err
	}

	if err := reconcilePairingStatus(ctx, settings); err != nil {
		return nil, err
	}

	if err := s.persistFreeboxSettings(ctx, conn, settings); err != nil {
		return nil, err
	}

	return &FreeboxPairingStatusResponse{Status: settings.Status}, nil
}

// loadPairingChannel resolves the org + connection and validates that
// the channel is a Freebox row currently in the pairing state. Pulled
// out of CheckFreeboxPairingStatus to keep that function below the
// cyclomatic-complexity threshold.
func (s *Service) loadPairingChannel(
	ctx context.Context, orgSlug, connectionUID string,
) (*models.Integration, *models.FreeboxSettings, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrOrganizationNotFound
		}

		return nil, nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrConnectionNotFound
		}

		return nil, nil, err
	}

	if conn.OrganizationUID != org.UID {
		return nil, nil, ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeFreebox {
		return nil, nil, ErrFreeboxTypeMismatch
	}

	settings, err := models.FreeboxSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, nil, fmt.Errorf("parse freebox settings: %w", err)
	}

	if settings.TrackID == 0 || settings.Status != models.FreeboxStatusPairing {
		return nil, nil, ErrFreeboxNotPairing
	}

	return conn, settings, nil
}

// reconcilePairingStatus polls the Freebox once and mutates `settings`
// in place to reflect the new state. The caller is responsible for
// persisting `settings` back to the channel row.
func reconcilePairingStatus(ctx context.Context, settings *models.FreeboxSettings) error {
	status, pollErr := freebox.CheckPairingStatus(ctx, settings, settings.TrackID)
	switch {
	case errors.Is(pollErr, freebox.ErrPairingDenied):
		settings.Status = models.FreeboxStatusDenied
	case errors.Is(pollErr, freebox.ErrPairingTimeout):
		settings.Status = models.FreeboxStatusTimeout
	case pollErr != nil:
		return fmt.Errorf("%w: %w", ErrFreeboxPairingFailed, pollErr)
	case status == freebox.StatusGranted:
		settings.Status = models.FreeboxStatusGranted
		settings.TrackID = 0
	}

	return nil
}

// mergeFreeboxSettings flattens the public/private structs into one
// effective map that applySettingsEncryption can split again. Keeping
// this in one place avoids leaking the freebox secret-key names into
// the channel handler.
func mergeFreeboxSettings(
	pub *models.FreeboxSettings, priv *models.FreeboxPrivateSettings,
) (map[string]any, error) {
	asMap, err := pub.ToJSONMap()
	if err != nil {
		return nil, fmt.Errorf("marshal freebox settings: %w", err)
	}

	out := make(map[string]any, len(asMap)+1)
	for k, v := range asMap {
		out[k] = v
	}

	if priv != nil && priv.AppToken != "" {
		out["appToken"] = priv.AppToken
	}

	return out, nil
}

// persistFreeboxSettings writes the freebox-specific Settings struct
// back to the channel without disturbing the encrypted app_token. It
// loads the existing decrypted secrets, replaces the public side, and
// goes back through the standard split-and-encrypt pipeline.
func (s *Service) persistFreeboxSettings(
	ctx context.Context, conn *models.Integration, settings *models.FreeboxSettings,
) error {
	existing, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return err
	}

	pubMap, err := settings.ToJSONMap()
	if err != nil {
		return fmt.Errorf("marshal freebox settings: %w", err)
	}

	merged := make(map[string]any, len(pubMap)+1)
	for k, v := range pubMap {
		merged[k] = v
	}
	// Preserve the encrypted secret if present.
	if v, ok := existing["appToken"]; ok {
		merged["appToken"] = v
	}

	if err := s.applySettingsEncryption(ctx, conn, merged); err != nil {
		return err
	}

	update := &models.IntegrationUpdate{
		Settings:             &conn.Settings,
		SettingsPrivate:      conn.SettingsPrivate,
		SettingsPrivateKeys:  conn.SettingsPrivateKeys,
		ClearSettingsPrivate: conn.SettingsPrivate == nil,
	}

	return s.db.UpdateChannel(ctx, conn.UID, update)
}

// DeleteIntegration deletes a connection.
func (s *Service) DeleteIntegration(ctx context.Context, orgSlug, connectionUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}

		return err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return ErrConnectionNotFound
	}

	if err := s.db.DeleteChannel(ctx, connectionUID); err != nil {
		return err
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeIntegrationDeleted,
		auditTarget(conn), models.JSONMap{"integration_type": string(conn.Type)})

	return nil
}

// IntegrationTestResult is returned by TestIntegration. Success reports whether
// the integration delivered the sample notification. StatusCode carries the HTTP
// status for HTTP-based integrations (e.g. webhooks); it is 0 for senders with
// no such concept. The handler always returns HTTP 200; the caller inspects
// Success.
type IntegrationTestResult struct {
	Success    bool   `json:"success"`
	StatusCode int    `json:"statusCode"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
	// Detail carries an optional human-readable success note (e.g. the
	// Kubernetes cluster's reported server version). Empty for notification
	// tests, which have nothing extra to report on success.
	Detail string `json:"detail,omitempty"`
}

// loadChannel resolves the org + connection and verifies the connection belongs
// to the org. Shared by the operations that act on a single connection.
func (s *Service) loadChannel(
	ctx context.Context, orgSlug, connectionUID string,
) (*models.Integration, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	return conn, nil
}

// loadWebhookChannel resolves the org + connection and asserts the channel is
// a webhook. Used by RotateWebhookSecret.
func (s *Service) loadWebhookChannel(
	ctx context.Context, orgSlug, connectionUID string,
) (*models.Integration, error) {
	conn, err := s.loadChannel(ctx, orgSlug, connectionUID)
	if err != nil {
		return nil, err
	}

	if conn.Type != models.ConnectionTypeWebhook {
		return nil, ErrNotWebhookChannel
	}

	return conn, nil
}

// persistChannelSettings re-splits/encrypts the given effective (decrypted)
// settings onto the channel and writes them. Mirrors the UpdateIntegration PATCH
// persistence path but for an already-merged settings map.
func (s *Service) persistChannelSettings(
	ctx context.Context, conn *models.Integration, effective map[string]any,
) error {
	if err := s.applySettingsEncryption(ctx, conn, effective); err != nil {
		return err
	}

	settings := conn.Settings
	update := &models.IntegrationUpdate{
		Settings:             &settings,
		SettingsPrivate:      conn.SettingsPrivate,
		SettingsPrivateKeys:  conn.SettingsPrivateKeys,
		ClearSettingsPrivate: conn.SettingsPrivate == nil,
	}

	return s.db.UpdateChannel(ctx, conn.UID, update)
}

// RotateWebhookSecret cycles a webhook channel's signing secret: the current
// secret becomes the previous one (valid for a 24 h grace window) and a fresh
// secret is generated. Receivers can verify against either during the window.
func (s *Service) RotateWebhookSecret(
	ctx context.Context, orgSlug, connectionUID string,
) (*IntegrationResponse, error) {
	conn, err := s.loadWebhookChannel(ctx, orgSlug, connectionUID)
	if err != nil {
		return nil, err
	}

	effective, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return nil, err
	}

	newSecret, err := generateWebhookSecret()
	if err != nil {
		return nil, err
	}

	// Move current → previous (only if a current secret actually exists).
	if current, ok := effective[settingsKeySigningSecret].(string); ok && current != "" {
		effective[settingsKeySigningSecretPrevious] = current
		effective[settingsKeySigningSecretExpiry] = time.Now().Add(webhookRotationGrace).
			UTC().Format(time.RFC3339)
	}

	effective[settingsKeySigningSecret] = newSecret

	if err = s.persistChannelSettings(ctx, conn, effective); err != nil {
		return nil, err
	}

	updated, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		return nil, err
	}

	resp := toResponse(updated, true)
	if err = s.injectSigningSecrets(ctx, updated, resp); err != nil {
		return nil, err
	}

	return resp, nil
}

// TestIntegration sends a sample notification through the integration's real
// sender — the same delivery path a live incident takes — and reports the
// outcome. Works for every notification type; data-source-only types (e.g.
// Freebox) are rejected with ErrIntegrationNotNotifiable.
func (s *Service) TestIntegration(
	ctx context.Context, orgSlug, connectionUID string,
) (*IntegrationTestResult, error) {
	conn, err := s.loadChannel(ctx, orgSlug, connectionUID)
	if err != nil {
		return nil, err
	}

	// Kubernetes cluster connections are data sources, not notification sinks,
	// so they can't go through the notification "send a test alert" path. Their
	// "test connection" instead probes the cluster API (/version) to surface
	// credential/RBAC failures at registration time.
	if conn.Type == models.ConnectionTypeKubernetes {
		return s.testKubernetesConnection(ctx, conn)
	}

	if !models.CapabilitiesFor(conn.Type).CanNotify {
		return nil, ErrIntegrationNotNotifiable
	}

	// Merge decrypted secrets into Settings so the sender has its tokens /
	// signing secret, exactly like the notification job runner does before
	// dispatch.
	effective, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return nil, err
	}

	conn.Settings = models.JSONMap(effective)

	// Web Push is special: WebPushSender.Send swallows per-subscription errors
	// as non-fatal (production behavior — keep trying remaining subs). For a
	// manual test we want strict feedback: fail loudly on the first error so the
	// user knows something is wrong.
	if conn.Type == models.ConnectionTypeWebPush {
		return s.testWebPushDelivery(ctx, conn, orgSlug)
	}

	sender, ok := notifications.GetSender(conn.Type)
	if !ok {
		return nil, ErrIntegrationNotNotifiable
	}

	// Senders that mutate their settings on delivery (webhook signing-secret
	// auto-generation/rotation, web push dead-subscription pruning) need a
	// persistence callback so the change is written back to the channel row.
	s.injectChannelUpdater(sender)

	payload := buildTestPayload(conn, orgSlug)
	jctx := s.testJobContext(conn)

	start := time.Now()
	sendErr := sender.Send(ctx, jctx, payload)
	durationMs := time.Since(start).Milliseconds()

	result := &IntegrationTestResult{
		Success:    sendErr == nil,
		DurationMs: durationMs,
	}

	if sendErr != nil {
		result.Error = sendErr.Error()
		result.StatusCode = statusCodeFromErr(sendErr)

		return result, nil
	}

	result.StatusCode = 200

	return result, nil
}

// kubernetesTestTimeout bounds the cluster "test connection" probe so a
// stuck/unreachable API server can't hang the request indefinitely.
const kubernetesTestTimeout = 15 * time.Second

// testKubernetesConnection probes a Kubernetes cluster connection by resolving
// its (decrypted) credentials to a clientset and calling the cluster's
// /version endpoint. Always returns HTTP-200-style IntegrationTestResult: a
// failed probe is reported as Success=false with the error, matching how
// notification tests report delivery failures.
func (s *Service) testKubernetesConnection(
	ctx context.Context, conn *models.Integration,
) (*IntegrationTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, kubernetesTestTimeout)
	defer cancel()

	start := time.Now()

	clientset, err := integrationk8s.ResolveClientset(ctx, s.db, s.creds, conn.OrganizationUID, conn.UID)
	if err != nil {
		return &IntegrationTestResult{
			Success:    false,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      err.Error(),
		}, nil
	}

	version, err := integrationk8s.ValidateConnection(ctx, clientset)
	if err != nil {
		return &IntegrationTestResult{
			Success:    false,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      err.Error(),
		}, nil
	}

	return &IntegrationTestResult{
		Success:    true,
		DurationMs: time.Since(start).Milliseconds(),
		Detail:     "Connected to cluster " + version.GitVersion,
	}, nil
}

// webPushTestSub mirrors the per-subscription shape stored in
// Settings["subscriptions"] — minimal fields needed for webpush.Send.
type webPushTestSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// webPushSubsFromSettings extracts the subscriptions slice from the
// integration's Settings map.
func webPushSubsFromSettings(settings models.JSONMap) ([]webPushTestSub, error) {
	raw, ok := settings["subscriptions"]
	if !ok {
		return nil, nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal webpush subscriptions: %w", err)
	}

	var subs []webPushTestSub
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil, fmt.Errorf("unmarshal webpush subscriptions: %w", err)
	}

	return subs, nil
}

// testWebPushDelivery sends a test push to every subscription and fails on the
// first error, giving the user immediate, accurate feedback. This is stricter
// than WebPushSender.Send (which swallows per-sub errors for production use).
func (s *Service) testWebPushDelivery(
	ctx context.Context, conn *models.Integration, orgSlug string,
) (*IntegrationTestResult, error) {
	if s.registry == nil || s.registry.WebPushOptions.VAPIDPublicKey == "" {
		return nil, notifications.ErrWebPushNotConfigured
	}

	subs, err := webPushSubsFromSettings(conn.Settings)
	if err != nil {
		return nil, err
	}

	if len(subs) == 0 {
		return &IntegrationTestResult{
			Success: false,
			Error:   "no subscriptions — subscribe a browser on the integration page first",
		}, nil
	}

	opts := s.registry.WebPushOptions

	msg := webpush.Message{
		Title: "[TEST] SolidPing",
		Body:  "Test push notification from SolidPing. If you see this, push delivery is working.",
		URL:   "/dash0/orgs/" + orgSlug,
	}

	start := time.Now()

	var successCount, failCount int
	var firstErrMsg string

	for i := range subs {
		subJSON, marshalErr := json.Marshal(subs[i])
		if marshalErr != nil {
			slog.WarnContext(ctx, "webpush test: failed to marshal subscription; skipping",
				"endpoint", subs[i].Endpoint, "error", marshalErr)

			continue
		}

		if errMsg := webPushSendErrMsg(webpush.Send(ctx, opts, string(subJSON), msg)); errMsg != "" {
			failCount++

			if firstErrMsg == "" {
				firstErrMsg = errMsg
			}
		} else {
			successCount++
		}
	}

	durationMs := time.Since(start).Milliseconds()

	if successCount == 0 {
		// Every subscription failed.
		return &IntegrationTestResult{
			Success:    false,
			DurationMs: durationMs,
			Error:      firstErrMsg,
		}, nil
	}

	result := &IntegrationTestResult{
		Success:    true,
		StatusCode: 201,
		DurationMs: durationMs,
	}

	if failCount > 0 {
		// Partial success: at least one browser received the push, others failed.
		// Surface the failure so the user can re-subscribe the affected browser.
		result.Error = fmt.Sprintf("%d/%d delivered; %d failed — %s",
			successCount, successCount+failCount, failCount, firstErrMsg)
	}

	return result, nil
}

// webPushSendErrMsg converts a webpush send error to a string, returning ""
// when err is nil. Extracted to avoid nilerr false-positives on the return path.
func webPushSendErrMsg(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// injectChannelUpdater wires a persistence callback into senders that mutate
// their channel settings during delivery. Other sender types are left untouched.
func (s *Service) injectChannelUpdater(sender notifications.Sender) {
	updater := func(ctx context.Context, channel *models.Integration) error {
		merged := make(map[string]any, len(channel.Settings))
		for k, v := range channel.Settings {
			merged[k] = v
		}

		return s.persistChannelSettings(ctx, channel, merged)
	}

	switch sn := sender.(type) {
	case *notifications.WebhookSender:
		sn.UpdateChannel = updater
	case *notifications.WebPushSender:
		sn.UpdateChannel = updater
	}
}

// testJobContext builds the JobContext senders receive during a test send. It
// mirrors the notification job runner's context so service-backed senders
// (email, web push, Slack threading) work from the test path. registry and
// appConfig may be nil in unit tests that only exercise context-free senders.
func (s *Service) testJobContext(conn *models.Integration) *jobdef.JobContext {
	orgUID := conn.OrganizationUID

	var bunDB *bun.DB
	if s.db != nil {
		bunDB = s.db.DB()
	}

	return &jobdef.JobContext{
		OrganizationUID: &orgUID,
		DB:              bunDB,
		DBService:       s.db,
		Services:        s.registry,
		AppConfig:       s.appConfig,
		Logger: slog.Default().With(
			"op", "integration-test", "integrationType", string(conn.Type)),
	}
}

// buildTestPayload returns a sample "incident created" payload describing a
// fake check, used by TestIntegration. The check is named so recipients can
// tell at a glance it's a manual test rather than a real outage.
func buildTestPayload(conn *models.Integration, orgSlug string) *notifications.Payload {
	now := time.Now()
	checkName := "SolidPing test"
	title := "Test notification from SolidPing"

	return &notifications.Payload{
		EventType: "incident.created",
		Incident: &models.Incident{
			UID:             uuid.NewString(),
			OrganizationUID: conn.OrganizationUID,
			State:           models.IncidentStateActive,
			StartedAt:       now,
			FailureCount:    1,
			Title:           &title,
		},
		Check: &models.Check{
			UID:  uuid.NewString(),
			Name: &checkName,
			Type: "http",
		},
		Integration: conn,
		OrgSlug:     orgSlug,
	}
}

// statusCodeFromErr extracts the HTTP status from a webhook request-failed
// error, defaulting to 0 for transport-level failures (DNS, refused, timeout).
// The error is formatted as "webhook request failed: status <code>: <body>".
func statusCodeFromErr(err error) int {
	if !errors.Is(err, notifications.ErrWebhookRequestFailed) {
		return 0
	}

	const marker = "status "

	msg := err.Error()
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return 0
	}

	rest := msg[idx+len(marker):]

	end := strings.IndexByte(rest, ':')
	if end >= 0 {
		rest = rest[:end]
	}

	code, convErr := strconv.Atoi(strings.TrimSpace(rest))
	if convErr != nil {
		return 0
	}

	return code
}
