package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// ConnectionType represents the type of integration connection.
type ConnectionType string

// Connection types.
const (
	ConnectionTypeSlack      ConnectionType = "slack"
	ConnectionTypeDiscord    ConnectionType = "discord"
	ConnectionTypeWebhook    ConnectionType = "webhook"
	ConnectionTypeEmail      ConnectionType = "email"
	ConnectionTypeGoogleChat ConnectionType = "googlechat"
	ConnectionTypeMattermost ConnectionType = "mattermost"
	ConnectionTypeNtfy       ConnectionType = "ntfy"
	ConnectionTypeOpsgenie   ConnectionType = "opsgenie"
	ConnectionTypePushover   ConnectionType = "pushover"
	ConnectionTypeFreebox    ConnectionType = "freebox"
	ConnectionTypeWebPush    ConnectionType = "webpush"
	ConnectionTypeKubernetes ConnectionType = "kubernetes"
	ConnectionTypeTwilio     ConnectionType = "twilio"
	ConnectionTypeMSTeams    ConnectionType = "msteams"
	// ConnectionTypeMSTeamsBot is the two-way Microsoft Teams bot integration
	// (Azure Bot / Bot Framework). It is deliberately distinct from
	// ConnectionTypeMSTeams, which stays as the zero-infra, one-way Teams
	// Workflow webhook: the two coexist and an org may use either or both.
	ConnectionTypeMSTeamsBot ConnectionType = "msteams-bot"
)

// Capabilities describes what roles an integration type can play. The two
// flags are independent: a single type may be both a notification sink and a
// data source. They replace the former hard-coded "Freebox is a source, not a
// sink" carve-out in notifications.GetSender — capability is now data, not a
// special case.
type Capabilities struct {
	// CanNotify reports whether the integration can receive outbound
	// notifications (i.e. it can act as a "channel" / notification target).
	CanNotify bool
	// CanSource reports whether the integration provides data that checks
	// read from (e.g. the Freebox line-quality source).
	CanSource bool
}

// CapabilitiesFor returns the capabilities of an integration connection type.
// Every notification sink (slack, discord, webhook, email, googlechat,
// mattermost, msteams, ntfy, opsgenie, pushover) is CanNotify; freebox is a data source
// (CanSource) and cannot receive notifications. The default branch
// intentionally covers every current notification-sink type, so only data
// sources need an explicit case.
//
//nolint:exhaustive // default branch handles all notification-sink types.
func CapabilitiesFor(t ConnectionType) Capabilities {
	switch t {
	case ConnectionTypeFreebox, ConnectionTypeKubernetes:
		return Capabilities{CanSource: true}
	default: // all current notification sinks
		return Capabilities{CanNotify: true}
	}
}

// Integration represents a stored, per-org, credentialed connection to a
// third-party system — Slack, Discord, email, generic webhook, Freebox, etc.
// It is the umbrella entity; when an integration can receive notifications
// (CanNotify) it plays the "channel" role. Backed by the `integrations` table.
type Integration struct {
	bun.BaseModel `bun:"table:integrations,alias:integration"`

	UID             string         `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string         `bun:"organization_uid,notnull"`
	Type            ConnectionType `bun:"type,notnull"`
	Name            string         `bun:"name,notnull"`
	Enabled         bool           `bun:"enabled,notnull,default:true"`
	IsDefault       bool           `bun:"is_default,notnull,default:false"`
	Settings        JSONMap        `bun:"settings,type:jsonb,notnull"`
	// SettingsPrivate / SettingsPrivateKeys mirror the credential-encryption
	// shape used on Check.Config. Tokens, webhook URLs, API keys live here
	// as an AES-GCM envelope at rest.
	SettingsPrivate     *string    `bun:"settings_private,type:text,nullzero"`
	SettingsPrivateKeys *string    `bun:"settings_private_keys,type:text,nullzero"`
	CreatedAt           time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt           time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt           *time.Time `bun:"deleted_at"`

	// Relations
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// NewIntegration creates a new integration with generated UID.
func NewIntegration(orgUID string, connType ConnectionType, name string) *Integration {
	now := time.Now()

	return &Integration{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Type:            connType,
		Name:            name,
		Enabled:         true,
		IsDefault:       false,
		Settings:        make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// IntegrationUpdate represents fields that can be updated.
type IntegrationUpdate struct {
	Name                 *string
	Enabled              *bool
	IsDefault            *bool
	Settings             *JSONMap
	SettingsPrivate      *string
	SettingsPrivateKeys  *string
	ClearSettingsPrivate bool
}

// ListIntegrationsFilter represents filter options for listing integrations.
type ListIntegrationsFilter struct {
	OrganizationUID string
	Type            *ConnectionType
	Enabled         *bool
}

// SlackSettings represents Slack-specific settings stored in the Settings JSONB.
//
//nolint:tagliatelle // JSON tags must match Slack API field names
type SlackSettings struct {
	TeamID            string   `json:"team_id"`
	TeamName          string   `json:"team_name"`
	BotUserID         string   `json:"bot_user_id"`
	AccessToken       string   `json:"access_token"`
	ChannelID         string   `json:"channel_id,omitempty"`
	ChannelName       string   `json:"channel_name,omitempty"`
	DestinationType   string   `json:"destination_type,omitempty"` // "channel" | "dm" | ""
	DisplayName       string   `json:"display_name,omitempty"`     // "#alerts" or "@alice"
	InstalledByUserID string   `json:"installed_by_user_id"`
	Scopes            []string `json:"scopes"`
	// MentionOnCall makes channel alerts ping the humans the escalation policy
	// would page first (`<@U123ABC>`), so a channel message tells the
	// responsible person it is theirs.
	//
	// Zero value is deliberately false: every integration stored before this
	// field existed has no such key in its settings JSON, decodes to false, and
	// keeps behaving exactly as before — no backfill, no migration. New Slack
	// integrations get `true` written explicitly by the install flow (see
	// slack.Service.createOrUpdateConnection).
	MentionOnCall bool `json:"mention_on_call"`
	// CommentIngestion selects how inbound Slack thread replies are treated:
	//
	//   - SlackCommentIngestionExplicit (default, and the meaning of an empty
	//     value): only an explicit `/comment` becomes an incident comment.
	//     Triage chatter in the thread — "lunch?", "who's on call?" — stays
	//     chatter instead of becoming permanent incident-timeline content.
	//   - SlackCommentIngestionAll: every human thread reply is ingested, the
	//     historical behavior, kept for teams that want it.
	//
	// Zero value is deliberately the safe one: an integration stored before
	// this field existed decodes to "" and therefore stops over-capturing,
	// which is the whole point of the change.
	CommentIngestion string `json:"comment_ingestion,omitempty"`
}

// Slack comment-ingestion modes. See SlackSettings.CommentIngestion.
const (
	// SlackCommentIngestionExplicit ingests only `/comment` invocations.
	SlackCommentIngestionExplicit = "explicit"
	// SlackCommentIngestionAll ingests every human thread reply.
	SlackCommentIngestionAll = "all"
)

// IngestsAllThreadReplies reports whether this Slack integration captures
// every human thread reply as an incident comment. Anything other than an
// explicit "all" — including an absent value on a pre-existing row — means no.
func (s *SlackSettings) IngestsAllThreadReplies() bool {
	return s != nil && s.CommentIngestion == SlackCommentIngestionAll
}

// ToJSONMap converts SlackSettings to JSONMap for storage.
func (s *SlackSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// SlackSettingsFromJSONMap parses SlackSettings from a JSONMap.
func SlackSettingsFromJSONMap(m JSONMap) (*SlackSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var s SlackSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

// DiscordSettings represents Discord-specific settings stored in the Settings JSONB.
//
//nolint:tagliatelle // JSON tags must match Discord webhook field names
type DiscordSettings struct {
	WebhookURL string `json:"webhook_url"`
}

// ToJSONMap converts DiscordSettings to JSONMap for storage.
func (ds *DiscordSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(ds)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// DiscordSettingsFromJSONMap parses DiscordSettings from a JSONMap.
func DiscordSettingsFromJSONMap(m JSONMap) (*DiscordSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var ds DiscordSettings
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, err
	}

	return &ds, nil
}

// MSTeamsSettings represents Microsoft Teams-specific settings stored in the
// Settings JSONB. WebhookURL is the Teams Workflow ("When a Teams webhook
// request is received") URL — the legacy Office 365 Connector format is not
// supported. Uses webhook_url (matching Discord, see DiscordSettings above)
// end-to-end on both the frontend form and this Go struct tag; unlike
// googlechat/mattermost, there is no key mismatch here by design.
//
//nolint:tagliatelle // JSON tag matches Discord's webhook field naming convention
type MSTeamsSettings struct {
	WebhookURL string `json:"webhook_url"`
}

// ToJSONMap converts MSTeamsSettings to JSONMap for storage.
func (ms *MSTeamsSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(ms)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// MSTeamsSettingsFromJSONMap parses MSTeamsSettings from a JSONMap.
func MSTeamsSettingsFromJSONMap(m JSONMap) (*MSTeamsSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var ms MSTeamsSettings
	if err := json.Unmarshal(data, &ms); err != nil {
		return nil, err
	}

	return &ms, nil
}

// MSTeamsDestination is a single captured Bot Framework conversation
// reference — the Teams equivalent of a Slack channel in the destinations
// picker. `ID` is the Bot Framework conversation id (for a channel-scoped
// install this is the channel's thread id), `ServiceURL` is the regional Bot
// Connector base URL that conversation must be addressed through, and
// `TeamID` / `TeamName` carry the owning team so the dashboard can group
// channels per team.
//
//nolint:tagliatelle // snake_case matches the settings JSONB convention (see SlackSettings)
type MSTeamsDestination struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	TeamID     string `json:"team_id,omitempty"`
	TeamName   string `json:"team_name,omitempty"`
	ServiceURL string `json:"service_url,omitempty"`
	// Type is "channel" today. Personal-scope DMs are phase 2 and would add
	// "personal" here without changing the shape.
	Type string `json:"type,omitempty"`
}

// MSTeamsBotSettings represents the settings JSONB of a `msteams-bot`
// connection. It is the Teams analog of SlackSettings: TenantID replaces
// TeamID as the workspace identity, and Destinations holds the conversation
// references captured when the bot was added to a team/channel (Teams has no
// "list all channels" API for a bot, so destinations are accumulated from
// install/conversationUpdate activities rather than fetched on demand).
//
// No credential lives here: the Entra app id/secret are instance-level system
// config (SaaS: SolidPing's multi-tenant app; self-hosted: the operator's own),
// so a stolen settings blob grants nothing. `app_secret` is still registered in
// credentials.ConnectionSecretFields as defense in depth for any future
// per-connection override.
//
//nolint:tagliatelle // snake_case matches the settings JSONB convention (see SlackSettings)
type MSTeamsBotSettings struct {
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name,omitempty"`
	// BotID is the bot's own Bot Framework user id inside this tenant
	// (`28:<app-id>`), used to recognize "the member added is us".
	BotID string `json:"bot_id,omitempty"`
	// AppID records which Entra app performed the install, so a credential
	// rotation that changes the app id is visible rather than silent.
	AppID string `json:"app_id,omitempty"`
	// ServiceURL is the tenant's regional Bot Connector base URL, captured at
	// install and refreshed on every inbound activity.
	ServiceURL string `json:"service_url,omitempty"`
	// ChannelID / ChannelName / TeamID are the default notification
	// destination — the Teams counterpart of SlackSettings.ChannelID.
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`

	InstalledByUserID string `json:"installed_by_user_id,omitempty"`
	// UninstalledAt is set (RFC3339) when the tenant removes the app. The row
	// is kept so the dashboard can render "uninstalled — reinstall to resume"
	// instead of the integration silently vanishing.
	UninstalledAt string `json:"uninstalled_at,omitempty"`

	Destinations []MSTeamsDestination `json:"destinations,omitempty"`
}

// HasDestination reports whether conversationID is one of the conversation
// references this connection actually captured from Bot Framework.
//
// This is the authorization rule for every destination selection, wherever it
// is made: the dashboard PATCH, the per-check override, and the in-band
// `config default-channel` command all funnel through it. A Teams conversation
// id is discoverable (it appears in "Get link to channel" URLs), so treating
// one as usable just because a client named it would let an org post into a
// channel belonging to a different tenant — the same "asserted vs proven
// identifier" mistake that made tenant_id exploitable.
func (s *MSTeamsBotSettings) HasDestination(conversationID string) bool {
	if conversationID == "" {
		return false
	}

	for i := range s.Destinations {
		if s.Destinations[i].ID == conversationID {
			return true
		}
	}

	return false
}

// FindDestination returns the captured conversation reference with this id.
func (s *MSTeamsBotSettings) FindDestination(conversationID string) *MSTeamsDestination {
	for i := range s.Destinations {
		if s.Destinations[i].ID == conversationID {
			return &s.Destinations[i]
		}
	}

	return nil
}

// ToJSONMap converts MSTeamsBotSettings to JSONMap for storage.
func (s *MSTeamsBotSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// MSTeamsBotSettingsFromJSONMap parses MSTeamsBotSettings from a JSONMap.
func MSTeamsBotSettingsFromJSONMap(m JSONMap) (*MSTeamsBotSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var s MSTeamsBotSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

// Freebox pairing status values that live in FreeboxSettings.Status. They are
// declared here (rather than in the integrations/freebox package) so model
// callers — channel forms, list responses, audit logs — can branch on them
// without importing the higher-level integration package.
const (
	FreeboxStatusPairing = "pairing"
	FreeboxStatusGranted = "granted"
	FreeboxStatusDenied  = "denied"
	FreeboxStatusTimeout = "timeout"
)

// FreeboxSettings represents the public (queryable) side of a Freebox
// integration connection's Settings JSONB. The matching secret — the
// permanent app_token granted by the Freebox after LCD approval — lives
// encrypted in SettingsPrivate under the "appToken" key.
type FreeboxSettings struct {
	BaseURL    string `json:"baseUrl"`              // e.g. "http://mafreebox.freebox.fr"
	AppID      string `json:"appId"`                // "io.solidping"
	DeviceName string `json:"deviceName,omitempty"` // user-visible label on the Freebox admin
	TrackID    int    `json:"trackId,omitempty"`    // only set while pairing; cleared on grant
	Status     string `json:"status,omitempty"`     // pairing | granted | denied | timeout
}

// ToJSONMap converts FreeboxSettings to JSONMap for storage.
func (fs *FreeboxSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(fs)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// FreeboxSettingsFromJSONMap parses FreeboxSettings from a JSONMap.
func FreeboxSettingsFromJSONMap(m JSONMap) (*FreeboxSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var fs FreeboxSettings
	if err := json.Unmarshal(data, &fs); err != nil {
		return nil, err
	}

	return &fs, nil
}

// FreeboxPrivateSettings carries the encrypted secret half of a Freebox
// connection. The app_token is permanent across Freebox reboots; we only
// ever store it once, on a successful pairing grant.
type FreeboxPrivateSettings struct {
	AppToken string `json:"appToken"`
}

// KubernetesSettings is the public (queryable) side of a Kubernetes cluster
// connection's Settings JSONB. The matching secret — a bearer token or a
// pasted kubeconfig — lives encrypted in SettingsPrivate under the "token" /
// "kubeconfig" keys (see KubernetesPrivateSettings). An in-cluster connection
// (InCluster=true) stores no secret and is resolved via the mounted service
// account at connect time.
//
//nolint:tagliatelle // keep the TLS acronym uppercase (matches spec + kubeconfig).
type KubernetesSettings struct {
	// APIServer is the cluster API server URL (e.g. "https://10.0.0.1:6443").
	// Empty when InCluster is true.
	APIServer string `json:"apiServer,omitempty"`
	// CACert is the PEM-encoded cluster CA bundle used to verify the API
	// server certificate. Optional; ignored when InsecureSkipTLSVerify is set.
	CACert string `json:"caCert,omitempty"`
	// InsecureSkipTLSVerify disables API-server certificate verification.
	// Use only for clusters with self-signed certs you cannot pin.
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
	// InCluster resolves the connection from the pod's mounted service-account
	// token (rest.InClusterConfig). Only works when solidping runs inside the
	// target cluster; needs no stored secret.
	InCluster bool `json:"inCluster,omitempty"`
}

// ToJSONMap converts KubernetesSettings to JSONMap for storage.
func (ks *KubernetesSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(ks)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// KubernetesSettingsFromJSONMap parses KubernetesSettings from a JSONMap.
func KubernetesSettingsFromJSONMap(m JSONMap) (*KubernetesSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var ks KubernetesSettings
	if err := json.Unmarshal(data, &ks); err != nil {
		return nil, err
	}

	return &ks, nil
}

// KubernetesPrivateSettings carries the encrypted secret half of a Kubernetes
// cluster connection. Exactly one of Token or Kubeconfig is set for a remote
// connection; both are empty for an in-cluster connection.
type KubernetesPrivateSettings struct {
	// Token is a bearer token (typically a service-account token) presented to
	// the API server.
	Token string `json:"token,omitempty"`
	// Kubeconfig is a full kubeconfig YAML document that resolves to an API
	// server + credentials. Takes precedence over Token when set.
	Kubeconfig string `json:"kubeconfig,omitempty"`
}

// KubernetesPrivateSettingsFromMap parses the decrypted secret half from a
// plaintext map (the shape returned by credentials.DecryptForOrg or the
// plaintext fallback on Settings).
func KubernetesPrivateSettingsFromMap(decrypted map[string]any) *KubernetesPrivateSettings {
	priv := &KubernetesPrivateSettings{}
	if v, ok := decrypted["token"].(string); ok {
		priv.Token = v
	}

	if v, ok := decrypted["kubeconfig"].(string); ok {
		priv.Kubeconfig = v
	}

	return priv
}

// TwilioSettings is the public (queryable) side of a Twilio connection's
// Settings JSONB. The matching secret — the account auth token — lives
// encrypted in SettingsPrivate under the "auth_token" key (see
// connectionSecretFields). Exactly one of FromNumber / MessagingServiceSID is
// set for SMS; VoiceFromNumber (optional) enables voice calls; ToNumbers are
// shared recipients for direct-channel (registry-path) sends. Region is
// public/non-secret: it picks which Twilio API host requests go to (see
// twilio.BaseURLForRegion) but carries no credential material itself.
//
//nolint:tagliatelle // JSON keys match the Twilio settings wire format (snake_case).
type TwilioSettings struct {
	AccountSID          string   `json:"account_sid"`
	AuthToken           string   `json:"auth_token,omitempty"`
	FromNumber          string   `json:"from_number,omitempty"`
	MessagingServiceSID string   `json:"messaging_service_sid,omitempty"`
	VoiceFromNumber     string   `json:"voice_from_number,omitempty"`
	ToNumbers           []string `json:"to_numbers,omitempty"`
	// Region is the Twilio regional edition this account was provisioned in
	// ("" or "us1" = the default global/US1 edge, "ie1" = Ireland, "au1" =
	// Australia, ...). Empty behaves exactly as before this field existed.
	Region string `json:"region,omitempty"`
}

// ToJSONMap converts TwilioSettings to JSONMap for storage.
func (ts *TwilioSettings) ToJSONMap() (JSONMap, error) {
	data, err := json.Marshal(ts)
	if err != nil {
		return nil, err
	}

	var m JSONMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	return m, nil
}

// TwilioSettingsFromJSONMap parses TwilioSettings from a JSONMap. The map is
// expected to be the decrypt-and-merged Settings (auth_token present).
func TwilioSettingsFromJSONMap(m JSONMap) (*TwilioSettings, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	var ts TwilioSettings
	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, err
	}

	return &ts, nil
}
