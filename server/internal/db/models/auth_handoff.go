package models

import (
	"time"

	"github.com/uptrace/bun"
)

// AuthHandoffCode is a single-use, short-lived code that hands a federated
// login's freshly minted session to the dashboard (spec 2026-09-25-12).
//
// The provider callback used to redirect the browser with the tokens in the
// query string. It now stores the session here and redirects with the code
// only; the dashboard trades the code once via POST
// /api/v1/auth/handoff/exchange.
//
// Neither the code nor the tokens are stored in clear: CodeHash is the hex
// SHA-256 of the code, and Payload is sealed under a key derived from the code
// (see internal/authhandoff). A row read from a dump can be neither redeemed
// nor opened.
type AuthHandoffCode struct {
	bun.BaseModel `bun:"table:auth_handoff_codes,alias:auth_handoff_code"`

	CodeHash        string    `bun:"code_hash,pk"`
	UserUID         string    `bun:"user_uid,notnull"`
	OrganizationUID *string   `bun:"organization_uid"`
	Payload         string    `bun:"payload,notnull"`
	ExpiresAt       time.Time `bun:"expires_at,notnull"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp"`
}
