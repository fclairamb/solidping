package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Identity source vocabulary. `auto` rows were produced by the email
// auto-match (Slack `users.lookupByEmail`); `manual` rows were picked by an
// admin. A re-sync never overwrites a `manual` row — the admin's choice is the
// stronger statement about who this person is.
const (
	// IdentitySourceAuto marks an identity created by the email auto-match.
	IdentitySourceAuto = "auto"
	// IdentitySourceManual marks an identity an admin picked explicitly.
	IdentitySourceManual = "manual"
)

// UserIntegrationIdentity answers "who is this org member on this integration
// instance" — e.g. `U123ABC` on a specific Slack workspace. It is deliberately
// NOT a user_contact: contacts are "how to page me" (and carry verification
// state), identities are "who I am there" and are used for mentions and
// attribution only. Paging never reads this table.
//
// Scoped per integration (not per integration *type*) because an org can
// connect several Slack workspaces, and the same person has a different user
// id in each.
type UserIntegrationIdentity struct {
	bun.BaseModel `bun:"table:user_integration_identities,alias:uii"`

	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull,type:varchar(36)"`
	IntegrationUID  string `bun:"integration_uid,notnull,type:varchar(36)"`
	UserUID         string `bun:"user_uid,notnull,type:varchar(36)"`
	// ExternalID is the provider-side identifier used to address the person in
	// a message (a Slack user id, rendered as `<@U123ABC>`).
	ExternalID string `bun:"external_id,notnull"`
	// DisplayName is the provider-side display name captured at match time.
	// Purely cosmetic — used for the admin UI and for the plain-text fallback.
	DisplayName string `bun:"display_name,notnull,default:''"`
	// Source is IdentitySourceAuto or IdentitySourceManual.
	Source    string    `bun:"source,notnull,default:'auto'"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewUserIntegrationIdentity builds an identity row with a fresh UID.
func NewUserIntegrationIdentity(
	orgUID, integrationUID, userUID, externalID, displayName, source string,
) *UserIntegrationIdentity {
	now := time.Now()

	if source != IdentitySourceManual {
		source = IdentitySourceAuto
	}

	return &UserIntegrationIdentity{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		IntegrationUID:  integrationUID,
		UserUID:         userUID,
		ExternalID:      externalID,
		DisplayName:     displayName,
		Source:          source,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
