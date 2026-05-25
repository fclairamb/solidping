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
		models.ConnectionTypeNtfy,
		models.ConnectionTypeOpsgenie,
		models.ConnectionTypePushover,
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
