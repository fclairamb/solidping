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

// SubscriberChannel is where a subscription delivers.
//
// `email` is the public self-serve channel and the only one a visitor can
// create; `webhook` and `slack` are operator-created from the dashboard,
// because a random visitor pasting an incoming-webhook URL has no verification
// story — see the spec's note on operator-side management.
type SubscriberChannel string

// Subscriber channels.
const (
	SubscriberChannelEmail   SubscriberChannel = "email"
	SubscriberChannelWebhook SubscriberChannel = "webhook"
	SubscriberChannelSlack   SubscriberChannel = "slack"
)

// IsValid reports whether the channel is one of the recognized values.
func (c SubscriberChannel) IsValid() bool {
	switch c {
	case SubscriberChannelEmail, SubscriberChannelWebhook, SubscriberChannelSlack:
		return true
	default:
		return false
	}
}

// IsEndpoint reports whether the channel delivers to a URL rather than a
// mailbox — i.e. whether the row carries an encrypted endpoint.
func (c SubscriberChannel) IsEndpoint() bool {
	return c == SubscriberChannelWebhook || c == SubscriberChannelSlack
}

// StatusPageSubscriber is a subscription to a status page's updates. The
// original (and only public self-serve) kind is an email address, double
// opt-in: a row is inactive (ConfirmedAt nil) until the visitor clicks the
// confirm link. Operators can additionally register webhook and Slack
// deliveries, which are created already confirmed — the operator IS the
// verification.
//
// Email is PII: never log it in clear, never expose it in public API responses.
// The endpoint URL is treated with the SAME opacity and then some: it is a
// credential, so it lives encrypted in EndpointPrivate and only EndpointHint
// ever reaches a response.
type StatusPageSubscriber struct {
	bun.BaseModel `bun:"table:status_page_subscriber"`

	UID             string `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	StatusPageUID   string `bun:"status_page_uid,notnull"`
	// Email is nil for a webhook/slack subscriber.
	Email            *string           `bun:"email"`
	Channel          SubscriberChannel `bun:"channel,notnull"`
	ConfirmedAt      *time.Time        `bun:"confirmed_at"`
	ConfirmToken     string            `bun:"confirm_token,notnull"`
	UnsubscribeToken string            `bun:"unsubscribe_token,notnull"`
	Scope            SubscriberScope   `bun:"scope,notnull"`
	IncidentUID      *string           `bun:"incident_uid"`
	// EndpointPrivate is the AES-GCM envelope holding {url, signingSecret} for
	// an endpoint channel. NEVER serialized.
	EndpointPrivate *string `bun:"endpoint_private"`
	// EndpointHint is the masked remnant of the URL — the only part a response
	// may echo.
	EndpointHint *string `bun:"endpoint_hint"`
	// EndpointKey is the sha256 of the normalized URL, used only as the
	// dedup key in the live-uniqueness index so the URL itself never lands in
	// an index.
	EndpointKey *string `bun:"endpoint_key"`
	// FailureCount counts CONSECUTIVE delivery failures; a success resets it.
	FailureCount int `bun:"failure_count,notnull"`
	// DisabledAt is set when the circuit breaker tripped. A disabled row is
	// skipped by the fan-out but kept, so the operator can see it and re-enable
	// it after fixing the endpoint.
	DisabledAt *time.Time `bun:"disabled_at"`
	CreatedAt  time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt  *time.Time `bun:"deleted_at"`
}

// EmailAddress returns the subscriber's email, or "" for an endpoint channel.
func (s *StatusPageSubscriber) EmailAddress() string {
	if s == nil || s.Email == nil {
		return ""
	}

	return *s.Email
}

// NewStatusPageSubscriber creates an unconfirmed subscriber with a generated
// UID and CreatedAt. Tokens are set by the service.
func NewStatusPageSubscriber(orgUID, statusPageUID, email string, scope SubscriberScope) *StatusPageSubscriber {
	return &StatusPageSubscriber{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		StatusPageUID:   statusPageUID,
		Email:           &email,
		Channel:         SubscriberChannelEmail,
		Scope:           scope,
		CreatedAt:       time.Now(),
	}
}

// NewEndpointSubscriber creates an operator-registered webhook/slack
// subscription. Unlike an email subscription it is born CONFIRMED: double
// opt-in exists to stop someone signing up a stranger's mailbox, and an
// operator registering their own webhook on their own page is not that.
func NewEndpointSubscriber(
	orgUID, statusPageUID string, channel SubscriberChannel, scope SubscriberScope,
) *StatusPageSubscriber {
	now := time.Now()

	return &StatusPageSubscriber{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		StatusPageUID:   statusPageUID,
		Channel:         channel,
		Scope:           scope,
		ConfirmedAt:     &now,
		CreatedAt:       now,
	}
}
