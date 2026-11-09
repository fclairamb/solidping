package discord

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

var errAckBoom = errors.New("boom")

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return &buf
}

//nolint:paralleltest // swaps the default logger
func TestAcknowledge_StaleIncidentLogsAtWarn(t *testing.T) {
	buf := captureDefaultLog(t)
	ctx, svc, incidents, _ := installedService(t)
	incidents.ackErr = models.ErrIncidentNotFound

	_, err := DispatchInteraction(ctx, svc, ackInteraction())
	require.NoError(t, err)
	require.Contains(t, buf.String(), "level=WARN")
	require.NotContains(t, buf.String(), "level=ERROR")
}

//nolint:paralleltest // swaps the default logger
func TestAcknowledge_OtherErrorLogsAtError(t *testing.T) {
	buf := captureDefaultLog(t)
	ctx, svc, incidents, _ := installedService(t)
	incidents.ackErr = errAckBoom

	_, err := DispatchInteraction(ctx, svc, ackInteraction())
	require.NoError(t, err)
	require.Contains(t, buf.String(), "level=ERROR msg=\"Failed to acknowledge incident from Discord\"")
}

func ackInteraction() *Interaction {
	return &Interaction{
		Type:      InteractionTypeMessageComponent,
		GuildID:   "G-ACME",
		ChannelID: "C-ALERTS",
		Member:    &InteractionMember{User: &User{ID: "U-ALICE", Username: "alice"}, Nick: "alice"},
		Message:   &InteractionMessage{ID: "M-1", ChannelID: "C-ALERTS"},
		Data:      &InteractionData{CustomID: BuildCustomID(ActionAcknowledge, "inc-1"), ComponentType: 2},
	}
}
