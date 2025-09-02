package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// StateEntry represents a key-value state entry.
// Used for storing notification state (Slack threads, Discord messages),
// user-scoped tokens (email confirmation, password reset),
// and distributed locking (preventing duplicate notifications).
type StateEntry struct {
	UID             string     `bun:"uid,pk"`
	OrganizationUID *string    `bun:"organization_uid"`
	UserUID         *string    `bun:"user_uid"`
	Key             string     `bun:"key,notnull"`
	Value           *JSONMap   `bun:"value,type:jsonb"`
	ExpiresAt       *time.Time `bun:"expires_at"`
	CreatedAt       time.Time  `bun:"created_at,notnull"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewStateEntry creates a new state entry with generated UID.
func NewStateEntry(orgUID *string, key string) *StateEntry {
	now := time.Now()

	return &StateEntry{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Key:             key,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// NewUserStateEntry creates a new user-scoped state entry.
func NewUserStateEntry(userUID, key string) *StateEntry {
	now := time.Now()

	return &StateEntry{
		UID:       uuid.New().String(),
		UserUID:   &userUID,
		Key:       key,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// StateKey joins parts with colons to create namespaced keys.
// Example: StateKey("incident", incidentUID, "slack_notification") returns "incident:abc123:slack_notification".
func StateKey(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}

	var result strings.Builder

	result.WriteString(parts[0])

	for i := 1; i < len(parts); i++ {
		result.WriteString(":")
		result.WriteString(parts[i])
	}

	return result.String()
}
