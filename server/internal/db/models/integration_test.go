package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilitiesFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		connType  ConnectionType
		canNotify bool
		canSource bool
	}{
		{name: "slack", connType: ConnectionTypeSlack, canNotify: true, canSource: false},
		{name: "discord", connType: ConnectionTypeDiscord, canNotify: true, canSource: false},
		{name: "webhook", connType: ConnectionTypeWebhook, canNotify: true, canSource: false},
		{name: "email", connType: ConnectionTypeEmail, canNotify: true, canSource: false},
		{name: "googlechat", connType: ConnectionTypeGoogleChat, canNotify: true, canSource: false},
		{name: "mattermost", connType: ConnectionTypeMattermost, canNotify: true, canSource: false},
		{name: "msteams", connType: ConnectionTypeMSTeams, canNotify: true, canSource: false},
		{name: "msteams-bot", connType: ConnectionTypeMSTeamsBot, canNotify: true, canSource: false},
		{name: "ntfy", connType: ConnectionTypeNtfy, canNotify: true, canSource: false},
		{name: "pagerduty", connType: ConnectionTypePagerduty, canNotify: true, canSource: false},
		{name: "pushover", connType: ConnectionTypePushover, canNotify: true, canSource: false},
		{name: "matrix", connType: ConnectionTypeMatrix, canNotify: true, canSource: false},
		{name: "freebox", connType: ConnectionTypeFreebox, canNotify: false, canSource: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			caps := CapabilitiesFor(tt.connType)
			r.Equal(tt.canNotify, caps.CanNotify, "CanNotify mismatch for %s", tt.connType)
			r.Equal(tt.canSource, caps.CanSource, "CanSource mismatch for %s", tt.connType)
		})
	}
}

// TestCapabilitiesFor_FreeboxIsNotNotify guards the user-facing bug fix: a
// freebox integration must never report CanNotify, otherwise it could be bound
// to a check as a notification target and silently no-op at send time.
func TestCapabilitiesFor_FreeboxIsNotNotify(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(CapabilitiesFor(ConnectionTypeFreebox).CanNotify)
	r.True(CapabilitiesFor(ConnectionTypeFreebox).CanSource)
}

// TestCapabilitiesFor_UnknownTypeDefaultsToNotify documents that an
// unrecognized type falls through to the notify-sink default. This keeps the
// existing behavior where new notification types are CanNotify without an
// explicit registry entry.
func TestCapabilitiesFor_UnknownTypeDefaultsToNotify(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	caps := CapabilitiesFor(ConnectionType("some-future-type"))
	r.True(caps.CanNotify)
	r.False(caps.CanSource)
}
