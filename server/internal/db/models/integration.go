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
// mattermost, ntfy, opsgenie, pushover) is CanNotify; freebox is a data source
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
func KubernetesPrivateSettingsFromMap(m map[string]any) *KubernetesPrivateSettings {
	priv := &KubernetesPrivateSettings{}
	if v, ok := m["token"].(string); ok {
		priv.Token = v
	}

	if v, ok := m["kubeconfig"].(string); ok {
		priv.Kubeconfig = v
	}

	return priv
}
