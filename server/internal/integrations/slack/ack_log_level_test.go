package slack

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

//nolint:paralleltest // swaps the default logger
func TestAcknowledgeLogLevel(t *testing.T) {
	h, svc, _ := solidpingSetup(t)
	coverFakeSlack(t, svc)

	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	in := &Interaction{
		Type: "block_actions", Team: Team{ID: msgTeamID}, User: User{ID: "U1", Username: "a"},
		Actions: []InteractionAction{{ActionID: "acknowledge_incident", Value: "i"}},
	}

	svc.incidentsService = &coverIncidentSvc{ackErr: fmt.Errorf("wrapped: %w", models.ErrIncidentNotFound)}
	resp, err := h.handleInteraction(t.Context(), in)
	require.NoError(t, err)
	require.Contains(t, resp.Text, "Could not acknowledge")
	require.Contains(t, buf.String(), "level=WARN")
	require.NotContains(t, buf.String(), "level=ERROR")

	buf.Reset()

	svc.incidentsService = &coverIncidentSvc{ackErr: errCoverBoom}
	_, err = h.handleInteraction(t.Context(), in)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "level=ERROR msg=\"Failed to acknowledge incident\"")
}
