package notifications

import (
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetSender returns the sender for a connection type. Types that cannot
// receive notifications (CanNotify == false in the capability registry, e.g.
// data sources like Freebox) resolve to (nil, false) purely as a consequence
// of their declared capabilities — there is no type-specific carve-out here.
func GetSender(connType models.ConnectionType) (Sender, bool) {
	if !models.CapabilitiesFor(connType).CanNotify {
		return nil, false
	}

	// Non-notify types (e.g. freebox) are already handled by the CanNotify
	// guard above; an unmapped notify type resolves to (nil, false).
	factory, ok := senderFactories()[connType]
	if !ok {
		return nil, false
	}

	return factory(), true
}

// SenderCapabilities declares, per connection type, which classes of
// notification that channel opts into. It is the ONE place a "this channel
// does not want that kind of message" rule may live — fan-out code asks
// AcceptsEventType and never inspects the connection type itself, so adding a
// second opt-out later is a table edit rather than another scattered `if`.
type SenderCapabilities struct {
	// NotifiesComments reports whether the channel receives `incident.comment`
	// fan-out. False only for paging-cost channels: an SMS or a phone call per
	// operator note is noise the recipient pays for, in money and in attention.
	NotifiesComments bool
}

// defaultSenderCapabilities is what a connection type gets when it declares
// nothing: everything on. New integrations therefore receive comments by
// default and only an explicit table entry can take that away.
func defaultSenderCapabilities() SenderCapabilities {
	return SenderCapabilities{NotifiesComments: true}
}

// senderCapabilities maps a connection type to its opt-outs. Only types that
// differ from defaultSenderCapabilities appear here; everything absent gets
// the default. Rebuilt per call to satisfy gochecknoglobals, same as
// senderFactories.
func senderCapabilities() map[models.ConnectionType]SenderCapabilities {
	return map[models.ConnectionType]SenderCapabilities{
		// SMS and voice: every comment would be a paid text or a ringing phone.
		models.ConnectionTypeTwilio: {NotifiesComments: false},
	}
}

// CapabilitiesForSender returns the notification capabilities declared for a
// connection type.
func CapabilitiesForSender(connType models.ConnectionType) SenderCapabilities {
	if caps, ok := senderCapabilities()[connType]; ok {
		return caps
	}

	return defaultSenderCapabilities()
}

// AcceptsEventType reports whether a connection type should receive a
// notification for the given event type. Fan-out call sites use this instead
// of naming any specific integration, which is what keeps the opt-out list in
// the registry rather than in the incident service.
func AcceptsEventType(connType models.ConnectionType, eventType string) bool {
	if eventType == eventTypeIncidentComment {
		return CapabilitiesForSender(connType).NotifiesComments
	}

	return true
}

// senderFactories maps a connection type to its sender constructor. A table
// rather than a switch: the list only ever grows, and a flat lookup keeps
// GetSender's complexity constant as integrations are added. Rebuilt per call
// (negligible cost) to satisfy gochecknoglobals, same as
// severities.allowedChannels.
func senderFactories() map[models.ConnectionType]func() Sender {
	return map[models.ConnectionType]func() Sender{
		models.ConnectionTypeSlack:      func() Sender { return &SlackSender{} },
		models.ConnectionTypeDiscord:    func() Sender { return &DiscordSender{} },
		models.ConnectionTypeWebhook:    func() Sender { return &WebhookSender{} },
		models.ConnectionTypeEmail:      func() Sender { return &EmailSender{} },
		models.ConnectionTypeGoogleChat: func() Sender { return &GoogleChatSender{} },
		models.ConnectionTypeMattermost: func() Sender { return &MattermostSender{} },
		models.ConnectionTypeMSTeams:    func() Sender { return &MSTeamsSender{} },
		models.ConnectionTypeMSTeamsBot: func() Sender { return &MSTeamsBotSender{} },
		models.ConnectionTypeNtfy:       func() Sender { return &NtfySender{} },
		models.ConnectionTypeMatrix:     func() Sender { return &MatrixSender{} },
		models.ConnectionTypePagerduty:  func() Sender { return &PagerDutySender{} },
		models.ConnectionTypePushover:   func() Sender { return &PushoverSender{} },
		models.ConnectionTypeWebPush:    func() Sender { return &WebPushSender{} },
		models.ConnectionTypeTwilio:     func() Sender { return &TwilioSender{} },
	}
}
