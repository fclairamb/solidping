package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Device authorization request statuses (RFC 8628 §3.3-3.5). A request starts
// pending and moves exactly once to approved or denied; the row is then
// consumed (hard-deleted) by the client's next poll.
const (
	DeviceAuthStatusPending  = "pending"
	DeviceAuthStatusApproved = "approved"
	DeviceAuthStatusDenied   = "denied"
)

// DeviceAuthRequest is a pending OAuth 2.0 Device Authorization Grant request
// (RFC 8628) opened by a CLI and approved by a human in the dashboard.
//
// DeviceCode is the requesting client's secret and the real capability (32
// random bytes); UserCode is the short, human-typed code and therefore the
// brute-forceable surface — the consent lookup is rate limited on top of the
// global limiter. TokenValue holds the PAT minted at consent time until the
// client's next poll picks it up, at which point the whole row is deleted so
// the token can only ever be delivered once.
//
// OrganizationUID is the org the approving user SELECTED on the consent page,
// not implicitly their session org: solidping is multi-tenant and the minted
// PAT is scoped to that choice.
type DeviceAuthRequest struct {
	bun.BaseModel `bun:"table:device_auth_requests,alias:device_auth_request"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	DeviceCode      string     `bun:"device_code,notnull"`
	UserCode        string     `bun:"user_code,notnull"`
	ClientName      string     `bun:"client_name,notnull"`
	Status          string     `bun:"status,notnull"`
	OrganizationUID *string    `bun:"organization_uid"`
	UserUID         *string    `bun:"user_uid"`
	TokenUID        *string    `bun:"token_uid"`
	TokenValue      *string    `bun:"token_value"`
	LastPolledAt    *time.Time `bun:"last_polled_at"`
	ExpiresAt       time.Time  `bun:"expires_at,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewDeviceAuthRequest builds a pending request with a generated UID.
// deviceCode and userCode are caller-generated; userCode must already be in
// canonical (uppercase, dashless) form.
func NewDeviceAuthRequest(clientName, deviceCode, userCode string, expiresAt time.Time) *DeviceAuthRequest {
	now := time.Now()

	return &DeviceAuthRequest{
		UID:        uuid.New().String(),
		DeviceCode: deviceCode,
		UserCode:   userCode,
		ClientName: clientName,
		Status:     DeviceAuthStatusPending,
		ExpiresAt:  expiresAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// DeviceAuthResolution carries the outcome of a consent decision. Status is
// DeviceAuthStatusApproved or DeviceAuthStatusDenied; the token fields are set
// only on approval.
type DeviceAuthResolution struct {
	Status          string
	UserUID         string
	OrganizationUID string
	TokenUID        string
	TokenValue      string
}
