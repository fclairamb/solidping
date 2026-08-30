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
	// UserContactTypeWhatsApp is a WhatsApp-reachable number. Deliberately
	// DISTINCT from UserContactTypePhone even when the digits are identical:
	// an SMS-verified number proves nothing about WhatsApp reachability, and
	// the WhatsApp verification round-trip doubles as Meta's required record
	// of the user's opt-in to receive business-initiated messages. Collapsing
	// the two would silently reuse an SMS consent as a WhatsApp consent.
	UserContactTypeWhatsApp = "whatsapp"
	// UserContactTypeTelegram is a Telegram chat id the instance bot can message.
	// The stored Value is the numeric chat id, never a @username: usernames can
	// be changed or reassigned, chat ids cannot.
	//
	// Deliberately NOT in VerifiableContactTypes: there is no code round-trip.
	// Pressing Start in Telegram (which delivers /start <token> to our webhook)
	// IS the proof of reachability and the opt-in, so the contact is created
	// already verified — and, correspondingly, a telegram contact may never be
	// created through the generic POST /notification-contacts endpoint, since
	// nothing there would stop a user typing a stranger's chat id.
	UserContactTypeTelegram = "telegram"
)

// VerifiableContactTypes are the contact types that require a code round-trip
// before they may be paged. Email and web push are self-verifying (delivery
// respectively subscription is the proof), so they are absent here.
func VerifiableContactTypes() map[string]bool {
	return map[string]bool{
		UserContactTypePhone:    true,
		UserContactTypeWhatsApp: true,
	}
}

// ContactRequiresVerification reports whether a contact type must complete the
// verification code round-trip before it can be used.
func ContactRequiresVerification(contactType string) bool {
	return VerifiableContactTypes()[contactType]
}

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
	VerifyAttempts  int        `bun:"verify_attempts,notnull"`
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

	UID        string `bun:"uid,pk,type:varchar(36)"`
	UserUID    string `bun:"user_uid,notnull,type:varchar(36)"`
	OrgUID     string `bun:"org_uid,notnull,type:varchar(36)"`
	ContactUID string `bun:"contact_uid,notnull,type:varchar(36)"`
	// No `default:true` on the tag even though the column has one — see the
	// StatusPage.AutoPublishDelaySeconds note: it made a route impossible to
	// CREATE disabled. NewUserNotificationRoute supplies the default.
	Enabled   bool      `bun:"enabled,notnull"`
	Position  int       `bun:"position,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`

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
