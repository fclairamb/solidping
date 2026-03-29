package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
)

// IntegrationConnection represents a connection to an external integration.
type IntegrationConnection struct {
	UID             string         `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string         `bun:"organization_uid,notnull"`
	Type            ConnectionType `bun:"type,notnull"`
	Name            string         `bun:"name,notnull"`
	Enabled         bool           `bun:"enabled,notnull,default:true"`
	IsDefault       bool           `bun:"is_default,notnull,default:false"`
	Settings        JSONMap        `bun:"settings,type:jsonb,notnull"`
	CreatedAt       time.Time      `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time      `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time     `bun:"deleted_at"`

	// Relations
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// NewIntegrationConnection creates a new integration connection with generated UID.
func NewIntegrationConnection(orgUID string, connType ConnectionType, name string) *IntegrationConnection {
	now := time.Now()

	return &IntegrationConnection{
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

// IntegrationConnectionUpdate represents fields that can be updated.
type IntegrationConnectionUpdate struct {
	Name      *string
	Enabled   *bool
	IsDefault *bool
	Settings  *JSONMap
}

// ListIntegrationConnectionsFilter represents filter options for listing connections.
type ListIntegrationConnectionsFilter struct {
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
