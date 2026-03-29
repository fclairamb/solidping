package notifications

import (
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetSender returns the sender for a connection type.
func GetSender(connType models.ConnectionType) (Sender, bool) {
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
