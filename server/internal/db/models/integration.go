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
)

// Channel represents a notification target — a Slack channel, Discord
// webhook, email recipient list, generic webhook URL, etc. The legacy
// table name `integration_connections` is preserved via the bun tag
// until the follow-up DB-rename spec ships; the model name and all
// callers use `Channel` to match the user-facing terminology.
type Channel struct {
	// Both the table name and the SQL alias are pinned to the legacy name so
	// existing raw queries and joins keep matching. The DB-rename spec
	// (Phase 3) drops these tags when it renames the underlying table.
	bun.BaseModel `bun:"table:integration_connections,alias:integration_connection"`

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

// NewChannel creates a new integration connection with generated UID.
func NewChannel(orgUID string, connType ConnectionType, name string) *Channel {
	now := time.Now()

	return &Channel{
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

// ChannelUpdate represents fields that can be updated.
type ChannelUpdate struct {
	Name                 *string
	Enabled              *bool
	IsDefault            *bool
	Settings             *JSONMap
	SettingsPrivate      *string
	SettingsPrivateKeys  *string
	ClearSettingsPrivate bool
}

// ListChannelsFilter represents filter options for listing connections.
type ListChannelsFilter struct {
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
