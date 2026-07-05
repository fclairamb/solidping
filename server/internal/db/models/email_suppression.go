package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Email suppression source constants — where the suppression came from.
const (
	// EmailSuppressionSourceLink means the recipient followed the
	// unsubscribe link (either the RFC 8058 one-click POST or the GET
	// confirmation page).
	EmailSuppressionSourceLink = "link"
	// EmailSuppressionSourceHeader means an inbox provider auto-submitted the
	// List-Unsubscribe-Post one-click request on the recipient's behalf
	// without them visiting the confirmation page. Same code path as "link"
	// today (the POST handler cannot distinguish the two) — kept as a
	// distinct value for when/if that becomes distinguishable, and so the
	// audit trail's vocabulary already has a slot for it.
	EmailSuppressionSourceHeader = "header"
	// EmailSuppressionSourceDashboard means an org admin added the
	// suppression manually from the dashboard (not currently exposed by the
	// API — creation is always recipient-initiated — but modeled so a future
	// admin-initiated mute doesn't need a schema change).
	EmailSuppressionSourceDashboard = "dashboard"
)

// EmailSuppression records that a recipient address has opted out of
// incident/alert emails, either for one specific check (CheckUID set) or for
// every check in the org (CheckUID nil). Backed by the `email_suppressions`
// table. Transactional emails (registration, password reset, invitation,
// password-changed) never consult this table — suppression only applies to
// incident/alert emails (spec acceptance criterion 6).
type EmailSuppression struct {
	bun.BaseModel `bun:"table:email_suppressions,alias:email_suppression"`

	UID             string    `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string    `bun:"organization_uid,notnull"`
	Email           string    `bun:"email,notnull"`
	CheckUID        *string   `bun:"check_uid"` // NULL = suppresses all checks in the org
	Source          string    `bun:"source,notnull"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewEmailSuppression creates a new suppression row with a generated UID.
// checkUID is nil for an org-wide suppression.
func NewEmailSuppression(orgUID, email string, checkUID *string, source string) *EmailSuppression {
	return &EmailSuppression{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Email:           email,
		CheckUID:        checkUID,
		Source:          source,
		CreatedAt:       time.Now(),
	}
}
