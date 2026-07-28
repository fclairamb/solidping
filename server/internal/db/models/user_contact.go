package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// UserContact type vocabulary.
const (
	UserContactTypeEmail     = "email"
	UserContactTypePhone     = "phone"
	UserContactTypeSlackUser = "slack_user"
	UserContactTypePushover  = "pushover_user"
	UserContactTypeNtfy      = "ntfy_topic"
	UserContactTypeWebPush   = "webpush"
)

// UserContact is one addressable endpoint for a user (email, phone, Slack DM, …).
// A single contact can belong to only one org — contacts are org-scoped so the
// correct bot token can be resolved at dispatch time.
type UserContact struct {
	bun.BaseModel `bun:"table:user_contacts"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	UserUID         string     `bun:"user_uid,notnull,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull,type:varchar(36)"`
	Type            string     `bun:"type,notnull"`
	Value           string     `bun:"value,notnull"`
	Label           string     `bun:"label,notnull"`
	VerifiedAt      *time.Time `bun:"verified_at"`
	// Verification state for contact types that require a code round-trip
	// (phone). VerifyCodeHash is the SHA-256 hex of the in-flight 6-digit
	// code; nil when no verification is pending or after a successful confirm.
	VerifyCodeHash  *string    `bun:"verify_code_hash"`
	VerifyExpiresAt *time.Time `bun:"verify_expires_at"`
	VerifyAttempts  int        `bun:"verify_attempts,notnull,default:0"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at,soft_delete"`
}

// NewUserContact builds a contact with a fresh UID.
func NewUserContact(userUID, orgUID, contactType, value, label string) *UserContact {
	now := time.Now()

	return &UserContact{
		UID:             uuid.New().String(),
		UserUID:         userUID,
		OrganizationUID: orgUID,
		Type:            contactType,
		Value:           value,
		Label:           label,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// UserNotificationRoute joins a UserContact to an ordered, toggle-able delivery
// slot. One route per contact (enforced by UNIQUE on contact_uid).
type UserNotificationRoute struct {
	bun.BaseModel `bun:"table:user_notification_routes"`

	UID        string    `bun:"uid,pk,type:varchar(36)"`
	UserUID    string    `bun:"user_uid,notnull,type:varchar(36)"`
	OrgUID     string    `bun:"org_uid,notnull,type:varchar(36)"`
	ContactUID string    `bun:"contact_uid,notnull,type:varchar(36)"`
	Enabled    bool      `bun:"enabled,notnull,default:true"`
	Position   int       `bun:"position,notnull,default:0"`
	CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt  time.Time `bun:"updated_at,notnull,default:current_timestamp"`

	Contact *UserContact `bun:"rel:belongs-to,join:contact_uid=uid"`
}

// NewUserNotificationRoute builds a route with a fresh UID.
func NewUserNotificationRoute(userUID, orgUID, contactUID string, position int) *UserNotificationRoute {
	now := time.Now()

	return &UserNotificationRoute{
		UID:        uuid.New().String(),
		UserUID:    userUID,
		OrgUID:     orgUID,
		ContactUID: contactUID,
		Enabled:    true,
		Position:   position,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}
