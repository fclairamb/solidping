package notifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestGetSender_NotifyCapableTypesResolve asserts every CanNotify connection
// type resolves to a non-nil sender.
func TestGetSender_NotifyCapableTypesResolve(t *testing.T) {
	t.Parallel()

	notifyTypes := []models.ConnectionType{
		models.ConnectionTypeSlack,
		models.ConnectionTypeDiscord,
		models.ConnectionTypeWebhook,
		models.ConnectionTypeEmail,
		models.ConnectionTypeGoogleChat,
		models.ConnectionTypeMattermost,
		models.ConnectionTypeMSTeams,
		models.ConnectionTypeMSTeamsBot,
		models.ConnectionTypeNtfy,
		models.ConnectionTypeOpsgenie,
		models.ConnectionTypePushover,
		models.ConnectionTypeWebPush,
		models.ConnectionTypeTwilio,
	}

	for _, connType := range notifyTypes {
		t.Run(string(connType), func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.True(models.CapabilitiesFor(connType).CanNotify, "precondition: %s must be CanNotify", connType)

			sender, ok := GetSender(connType)
			r.True(ok, "%s should resolve a sender", connType)
			r.NotNil(sender, "%s sender must not be nil", connType)
		})
	}
}

// TestGetSender_FreeboxIsNotASink is the data-driven regression: GetSender
// returns (nil, false) for Freebox purely because CanNotify is false — no
// type-specific branch is involved.
func TestGetSender_FreeboxIsNotASink(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(models.CapabilitiesFor(models.ConnectionTypeFreebox).CanNotify)

	sender, ok := GetSender(models.ConnectionTypeFreebox)
	r.False(ok, "freebox must not resolve a notification sender")
	r.Nil(sender, "freebox sender must be nil")
}

// TestGetSender_UnknownTypeReturnsFalse ensures an unrecognized type does not
// resolve a sender even though it defaults to CanNotify (there is no concrete
// sender implementation for it).
func TestGetSender_UnknownTypeReturnsFalse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender, ok := GetSender(models.ConnectionType("some-future-type"))
	r.False(ok)
	r.Nil(sender)
}

// TestAcceptsEventType_CommentOptOut pins the registry's only comment opt-out:
// Twilio (SMS/voice) never receives `incident.comment`, every other notify
// type does, and no event type other than comment is filtered at all.
func TestAcceptsEventType_CommentOptOut(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(AcceptsEventType(models.ConnectionTypeTwilio, "incident.comment"),
		"twilio must be opted out of comment fan-out")

	// Twilio still receives every lifecycle event — the opt-out is per event
	// class, not a mute of the whole integration.
	for _, eventType := range []string{
		"incident.created", "incident.resolved", "incident.escalated", "incident.reopened",
	} {
		r.True(AcceptsEventType(models.ConnectionTypeTwilio, eventType), eventType)
	}

	// Everything else, including email, is in the default comment set.
	for _, connType := range []models.ConnectionType{
		models.ConnectionTypeSlack,
		models.ConnectionTypeDiscord,
		models.ConnectionTypeWebhook,
		models.ConnectionTypeEmail,
		models.ConnectionTypeGoogleChat,
		models.ConnectionTypeMattermost,
		models.ConnectionTypeMSTeams,
		models.ConnectionTypeMSTeamsBot,
		models.ConnectionTypeNtfy,
		models.ConnectionTypeOpsgenie,
		models.ConnectionTypePushover,
		models.ConnectionTypeWebPush,
	} {
		r.True(AcceptsEventType(connType, "incident.comment"), string(connType))
	}
}
