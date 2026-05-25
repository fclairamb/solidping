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

	switch connType {
	case models.ConnectionTypeSlack:
		return &SlackSender{}, true
	case models.ConnectionTypeDiscord:
		return &DiscordSender{}, true
	case models.ConnectionTypeWebhook:
		return &WebhookSender{}, true
	case models.ConnectionTypeEmail:
		return &EmailSender{}, true
	case models.ConnectionTypeGoogleChat:
		return &GoogleChatSender{}, true
	case models.ConnectionTypeMattermost:
		return &MattermostSender{}, true
	case models.ConnectionTypeNtfy:
		return &NtfySender{}, true
	case models.ConnectionTypeOpsgenie:
		return &OpsgenieSender{}, true
	case models.ConnectionTypePushover:
		return &PushoverSender{}, true
	default:
		return nil, false
	}
}
