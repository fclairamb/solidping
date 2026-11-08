package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestBlockActionReplyGoesToResponseURL pins that a button's reply reaches
// Slack through the interaction's response_url: Slack ignores the HTTP body of
// a block_actions request, so returning the updated message there alone left
// the Acknowledge button looking dead.
func TestBlockActionReplyGoesToResponseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	h, svc, org := solidpingSetup(t)
	coverFakeSlack(t, svc)

	transport := &recordingTransport{}
	h.responseURLClient = newResponseURLClient(transport)

	chk := models.NewCheck(org.UID, "api", "http")
	r.NoError(svc.db.CreateCheck(t.Context(), chk))
	svc.incidentsService = &coverIncidentSvc{
		incident: models.NewIncident(org.UID, chk.UID, time.Now(), "down"), checkErr: errCoverBoom,
	}

	in := &Interaction{
		Type:        "block_actions",
		Team:        Team{ID: msgTeamID},
		User:        User{ID: "U1", Username: "alice"},
		ResponseURL: "https://hooks.slack.com/actions/T1/1/abc",
		Actions:     []InteractionAction{{ActionID: "acknowledge_incident", Value: "i"}},
	}

	_, err := h.handleInteraction(t.Context(), in)
	r.NoError(err)

	seen := transport.seen()
	r.Len(seen, 1)
	r.Equal("https://hooks.slack.com/actions/T1/1/abc", seen[0].url)
	r.Contains(seen[0].body, `"replace_original":true`)
	r.Contains(seen[0].body, "Acknowledged by")

	// The ephemeral error of a failed acknowledgment goes the same way.
	svc.incidentsService = &coverIncidentSvc{ackErr: errCoverBoom}
	_, err = h.handleInteraction(t.Context(), in)
	r.NoError(err)
	r.Len(transport.seen(), 2)
	r.Contains(transport.seen()[1].body, "Could not acknowledge")

	// A URL button sends nothing, and a non-Slack response_url is never called.
	in.Actions = []InteractionAction{{ActionID: "view_dashboard"}}
	_, err = h.handleInteraction(t.Context(), in)
	r.NoError(err)

	in.Actions = []InteractionAction{{ActionID: "acknowledge_incident", Value: "i"}}
	in.ResponseURL = "https://attacker.example/x"
	_, err = h.handleInteraction(t.Context(), in)
	r.NoError(err)
	r.Len(transport.seen(), 2)
}
