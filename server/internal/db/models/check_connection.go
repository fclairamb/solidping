package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// CheckConnection represents the many-to-many relationship between checks and
// the integrations they notify through. Backed by the `check_channels` table
// (the binding keeps the "channel" name to match the notify-role taxonomy); the
// FK column is `integration_uid`. The Go field name ConnectionUID is retained.
type CheckConnection struct {
	bun.BaseModel `bun:"table:check_channels,alias:check_channel"`

	UID             string    `bun:"uid,pk,type:varchar(36)"`
	CheckUID        string    `bun:"check_uid,notnull"`
	ConnectionUID   string    `bun:"integration_uid,notnull"`
	OrganizationUID string    `bun:"organization_uid,notnull"`
	Settings        *JSONMap  `bun:"settings,type:jsonb"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time `bun:"updated_at,notnull,default:current_timestamp"`

	// Relations (optional, for eager loading)
	Check        *Check        `bun:"rel:belongs-to,join:check_uid=uid"`
	Connection   *Integration  `bun:"rel:belongs-to,join:integration_uid=uid"`
	Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// CheckConnectionUpdate represents fields that can be updated for a check-connection.
type CheckConnectionUpdate struct {
	Settings *JSONMap
}

// NewCheckConnection creates a new check-connection relationship with generated UID.
func NewCheckConnection(checkUID, connectionUID, organizationUID string) *CheckConnection {
	now := time.Now()

	return &CheckConnection{
		UID:             uuid.New().String(),
		CheckUID:        checkUID,
		ConnectionUID:   connectionUID,
		OrganizationUID: organizationUID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// GetEffectiveSettings merges connection defaults with check-level overrides.
// The check-connection settings take precedence over connection defaults.
func GetEffectiveSettings(connection *Integration, checkConnection *CheckConnection) JSONMap {
	effective := make(JSONMap)

	// Copy connection defaults
	for k, v := range connection.Settings {
		effective[k] = v
	}

	// Apply check-connection overrides if present
	if checkConnection != nil && checkConnection.Settings != nil {
		for k, v := range *checkConnection.Settings {
			effective[k] = v
		}
	}

	return effective
}
