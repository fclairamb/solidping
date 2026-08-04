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
		models.ConnectionTypeOpsgenie:   func() Sender { return &OpsgenieSender{} },
		models.ConnectionTypePushover:   func() Sender { return &PushoverSender{} },
		models.ConnectionTypeWebPush:    func() Sender { return &WebPushSender{} },
		models.ConnectionTypeTwilio:     func() Sender { return &TwilioSender{} },
	}
}
