package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// SubscriberScope determines how broadly a subscriber is notified.
type SubscriberScope string

const (
	// SubscriberScopePage notifies on every published update for the page.
	SubscriberScopePage SubscriberScope = "page"
	// SubscriberScopeIncident notifies only for updates threaded under a
	// specific incident.
	SubscriberScopeIncident SubscriberScope = "incident"
)

// IsValid reports whether the scope is one of the recognized values.
func (s SubscriberScope) IsValid() bool {
	switch s {
	case SubscriberScopePage, SubscriberScopeIncident:
		return true
	default:
		return false
	}
}

// StatusPageSubscriber is a public visitor who has asked to be notified about a
// status page's updates by email. Subscriptions are double opt-in: a row is
// inactive (ConfirmedAt nil) until the visitor clicks the confirm link.
//
// Email is PII: never log it in clear, never expose it in public API responses.
type StatusPageSubscriber struct {
	bun.BaseModel `bun:"table:status_page_subscriber"`

	UID              string          `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID  string          `bun:"organization_uid,notnull"`
	StatusPageUID    string          `bun:"status_page_uid,notnull"`
	Email            string          `bun:"email,notnull"`
	ConfirmedAt      *time.Time      `bun:"confirmed_at"`
	ConfirmToken     string          `bun:"confirm_token,notnull"`
	UnsubscribeToken string          `bun:"unsubscribe_token,notnull"`
	Scope            SubscriberScope `bun:"scope,notnull"`
	IncidentUID      *string         `bun:"incident_uid"`
	CreatedAt        time.Time       `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt        *time.Time      `bun:"deleted_at"`
}

// NewStatusPageSubscriber creates an unconfirmed subscriber with a generated
// UID and CreatedAt. Tokens are set by the service.
func NewStatusPageSubscriber(orgUID, statusPageUID, email string, scope SubscriberScope) *StatusPageSubscriber {
	return &StatusPageSubscriber{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		StatusPageUID:   statusPageUID,
		Email:           email,
		Scope:           scope,
		CreatedAt:       time.Now(),
	}
}
