package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Severity is the per-org channel-set primitive for escalation steps.
// Spec 2026-05-08-03 introduces it so a step can say "page critically"
// and fan out to email + sms + voice in a single tick rather than a
// chain of single-channel steps.
//
// Channels is stored as a JSON-encoded array of channel-type strings:
// "email", "slack", "discord", … from ConnectionType, plus the synthetic
// direct-channel types "sms"/"voice"/"push"/"critical_push" that gate
// behind future provider integrations.
type Severity struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Slug            string     `bun:"slug,notnull"`
	Name            string     `bun:"name,notnull"`
	Description     *string    `bun:"description"`
	Channels        string     `bun:"channels,notnull"`
	IsDefault       bool       `bun:"is_default,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewSeverity creates a Severity with a generated UID and the given
// channel-type list serialized to its `channels` column.
func NewSeverity(orgUID, slug, name string, channels []string, isDefault bool) *Severity {
	now := time.Now()
	encoded := "[]"
	if len(channels) > 0 {
		if data, err := json.Marshal(channels); err == nil {
			encoded = string(data)
		}
	}

	return &Severity{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Slug:            slug,
		Name:            name,
		Channels:        encoded,
		IsDefault:       isDefault,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// ChannelList returns the parsed channel-type list from the JSON-encoded
// Channels column. Returns an empty slice on parse error so callers can
// treat the row as "fan out to nothing" rather than crashing.
func (s *Severity) ChannelList() []string {
	var out []string
	if s.Channels == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s.Channels), &out)

	return out
}

// SeverityUpdate represents fields that can be updated on a severity.
// Pointer-typed fields use nil to mean "leave alone"; channel slices
// use a separate flag because []string{} is itself a meaningful value
// ("page nothing").
type SeverityUpdate struct {
	Slug         *string
	Name         *string
	Description  *string
	Channels     *[]string
	IsDefault    *bool
	ClearDefault bool
}

// ListSeveritiesFilter narrows a list query.
type ListSeveritiesFilter struct {
	OrganizationUID string
}

// SeveritySeed describes one of the per-org default severities seeded
// when an organization is first created. Kept in models so both the
// migrate path and the org-creation hook can share the source of truth.
type SeveritySeed struct {
	Slug      string
	Name      string
	Channels  []string
	IsDefault bool
}

// DefaultSeveritySeeds returns the three rows seeded for every new org.
// The "default" row is the org's default severity used by escalation
// steps that omit `severityUid` and target a user / all_admins.
func DefaultSeveritySeeds() []SeveritySeed {
	emailType := string(ConnectionTypeEmail)
	slackType := string(ConnectionTypeSlack)

	return []SeveritySeed{
		{Slug: "low", Name: "Low", Channels: []string{emailType}, IsDefault: false},
		{
			Slug: "default", Name: "Default",
			Channels: []string{emailType, slackType}, IsDefault: true,
		},
		{
			Slug: "critical", Name: "Critical",
			Channels:  []string{emailType, slackType, "sms", "voice", "critical_push"},
			IsDefault: false,
		},
	}
}
