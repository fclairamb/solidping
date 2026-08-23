package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Support channels. These are the inbound surfaces a human can reach us on.
// SupportChannelEmail exists in the vocabulary (and in the database CHECK
// constraint) but nothing writes it in v1: inbound email capture is deliberately
// a separate, later spec, so v1 ships the asymmetry "email support is a human
// mailbox, not a thread in the inbox". Reserving the value now means that spec
// needs no migration.
const (
	SupportChannelWhatsApp = "whatsapp"
	SupportChannelTelegram = "telegram"
	SupportChannelSMS      = "sms"
	SupportChannelSlack    = "slack"
	SupportChannelDiscord  = "discord"
	SupportChannelEmail    = "email"
)

// Thread status. Set by the OPERATOR, deliberately — this is NOT the reply
// window. See SupportThread.ReplyWindow.
const (
	SupportStatusOpen    = "open"
	SupportStatusPending = "pending"
	SupportStatusClosed  = "closed"
)

// Message direction.
const (
	SupportDirectionInbound  = "inbound"
	SupportDirectionOutbound = "outbound"
)

// Raw message types. Anything the channel sends that is not plain text is
// recorded with a placeholder body and the real kind here, so an operator can
// see that a person sent a photo rather than seeing nothing at all.
const (
	SupportRawTypeText        = "text"
	SupportRawTypeImage       = "image"
	SupportRawTypeAudio       = "audio"
	SupportRawTypeVideo       = "video"
	SupportRawTypeDocument    = "document"
	SupportRawTypeLocation    = "location"
	SupportRawTypeSticker     = "sticker"
	SupportRawTypeUnsupported = "unsupported"
)

// SupportBodyMaxLength caps a stored message body. These endpoints are fed by
// publicly reachable phone numbers, so the body is attacker-influenced: over the
// cap it is truncated and flagged rather than rejected, because a truncated
// record still beats a lost one.
const SupportBodyMaxLength = 8000

// WhatsAppReplyWindow is the free customer-service window Meta opens when a user
// messages us. Inside it we may reply with ordinary text; outside it only an
// approved template may be sent, so a free-form reply is *impossible* rather
// than merely discouraged.
const WhatsAppReplyWindow = 24 * time.Hour

// ValidSupportChannel reports whether a channel value is one we accept.
func ValidSupportChannel(channel string) bool {
	switch channel {
	case SupportChannelWhatsApp, SupportChannelTelegram, SupportChannelSMS,
		SupportChannelSlack, SupportChannelDiscord, SupportChannelEmail:
		return true
	default:
		return false
	}
}

// ValidSupportStatus reports whether a status value is one we accept.
func ValidSupportStatus(status string) bool {
	switch status {
	case SupportStatusOpen, SupportStatusPending, SupportStatusClosed:
		return true
	default:
		return false
	}
}

// SupportThread is one conversation with one person on one channel.
//
// Threads belong to the INSTANCE, not to an organization. The sender of an
// inbound WhatsApp message is a phone number; frequently there is no org to
// attribute it to at all, and a message from a stranger must not be dropped for
// lack of one. OrganizationUID/UserUID are therefore nullable ATTRIBUTION — a
// hint for the operator, never an access-control boundary.
type SupportThread struct {
	bun.BaseModel `bun:"table:support_threads,alias:support_threads"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	Channel         string     `bun:"channel,notnull"`
	ChannelIdentity string     `bun:"channel_identity,notnull"`
	ChannelContext  JSONMap    `bun:"channel_context,type:jsonb,nullzero"`
	Subject         string     `bun:"subject,notnull"`
	Status          string     `bun:"status,notnull"`
	OrganizationUID *string    `bun:"organization_uid"`
	UserUID         *string    `bun:"user_uid"`
	LastMessageAt   time.Time  `bun:"last_message_at,notnull"`
	LastInboundAt   *time.Time `bun:"last_inbound_at"`
	UnreadCount     int        `bun:"unread_count,notnull"`
	LastMirrorAt    *time.Time `bun:"last_mirror_at"`
	PendingMirrors  int        `bun:"pending_mirrors,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewSupportThread builds a live thread for a channel identity.
func NewSupportThread(channel, identity string, createdAt time.Time) *SupportThread {
	return &SupportThread{
		UID:             uuid.New().String(),
		Channel:         channel,
		ChannelIdentity: identity,
		Status:          SupportStatusOpen,
		LastMessageAt:   createdAt,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
}

// SupportReplyWindow is the DERIVED answer to "can we still send a free-form
// reply right now?". It is computed from the last inbound message's timestamp
// and the channel's rule at read time and never stored, so it cannot go stale.
//
// It is a different axis from Status. A thread can be open (the customer's
// question is unanswered) and yet expired (WhatsApp will no longer accept a
// free-form reply) — which is precisely the state an operator most needs to see,
// because it is the one where the product cannot do what the UI otherwise
// implies.
type SupportReplyWindow struct {
	// Expires reports whether this channel has a window at all. Only WhatsApp
	// does.
	Expires bool `json:"expires"`
	// Open reports whether a free-form reply can be sent right now.
	Open bool `json:"open"`
	// ExpiresAt is when the window closes. Nil for non-expiring channels.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Reason explains a closed window in operator-facing terms. Empty when open.
	Reason string `json:"reason,omitempty"`
	// CostsMoney flags channels where every reply is billed per segment.
	CostsMoney bool `json:"costsMoney"`
}

// ReplyWindow derives the current reply window for the thread.
func (t *SupportThread) ReplyWindow(now time.Time) SupportReplyWindow {
	window := SupportReplyWindow{
		Expires:    false,
		Open:       true,
		CostsMoney: t.Channel == SupportChannelSMS,
	}

	// Email has no outbound adapter in v1 — replies go through the human
	// mailbox, deliberately. Say so rather than offering a box that fails.
	if t.Channel == SupportChannelEmail {
		window.Open = false
		window.Reason = "email replies are sent from the support mailbox, not from here"

		return window
	}

	if t.Channel != SupportChannelWhatsApp {
		return window
	}

	window.Expires = true

	if t.LastInboundAt == nil {
		window.Open = false
		window.Reason = "no inbound message yet — WhatsApp only allows a free-form reply " +
			"inside the 24-hour window a user's message opens"

		return window
	}

	expiresAt := t.LastInboundAt.Add(WhatsAppReplyWindow)
	window.ExpiresAt = &expiresAt

	if !now.Before(expiresAt) {
		window.Open = false
		window.Reason = "the 24-hour WhatsApp customer-service window has lapsed — " +
			"only an approved template may be sent now"
	}

	return window
}

// SupportMessage is one message inside a thread.
type SupportMessage struct {
	bun.BaseModel `bun:"table:support_messages,alias:support_messages"`

	UID        string    `bun:"uid,pk,type:varchar(36)"`
	ThreadUID  string    `bun:"thread_uid,notnull,type:varchar(36)"`
	Channel    string    `bun:"channel,notnull"`
	Direction  string    `bun:"direction,notnull"`
	Body       string    `bun:"body,notnull"`
	Truncated  bool      `bun:"truncated,notnull"`
	RawType    string    `bun:"raw_type,notnull"`
	ExternalID *string   `bun:"external_id"`
	AuthorUID  *string   `bun:"author_uid"`
	Delivery   JSONMap   `bun:"delivery,type:jsonb,nullzero"`
	CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt  time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewSupportMessage builds a message row, applying the body cap.
func NewSupportMessage(threadUID, channel, direction, body string, createdAt time.Time) *SupportMessage {
	body, truncated := TruncateSupportBody(body)

	return &SupportMessage{
		UID:       uuid.New().String(),
		ThreadUID: threadUID,
		Channel:   channel,
		Direction: direction,
		Body:      body,
		Truncated: truncated,
		RawType:   SupportRawTypeText,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

// TruncateSupportBody caps a body at SupportBodyMaxLength, reporting whether it
// had to cut. Rune-aware, so a cap never splits a multi-byte character.
func TruncateSupportBody(body string) (string, bool) {
	runes := []rune(body)
	if len(runes) <= SupportBodyMaxLength {
		return body, false
	}

	return string(runes[:SupportBodyMaxLength]), true
}

// ListSupportThreadsFilter configures the thread listing.
type ListSupportThreadsFilter struct {
	Status  string
	Channel string
	Query   string
	Limit   int
}
