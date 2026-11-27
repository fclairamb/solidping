package incidents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestAckFromMattermostAttributesActorAndSkipsOrigin pins the Mattermost
// variant of the chat ack path: the payload names the Mattermost user, and the
// connection whose post was clicked is not sent a second "acknowledged" post.
func TestAckFromMattermostAttributesActorAndSkipsOrigin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	mm := attachChannel(t, s, models.ConnectionTypeMattermost, "mm", models.JSONMap{
		"webhook_url": "https://mattermost.example.com/hooks/abc",
	})

	_, err := s.svc.AcknowledgeIncidentFromMattermost(
		ctx, s.org.UID, s.incident.UID, "mm-user-1", "alice", mm.UID,
	)
	r.NoError(err)

	var ackEvent *models.Event

	for _, event := range listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID) {
		if event.EventType == models.EventTypeIncidentAcknowledged {
			ackEvent = event

			break
		}
	}

	r.NotNil(ackEvent)
	r.Equal("mattermost", ackEvent.Payload["via"])
	r.Equal("mm-user-1", ackEvent.Payload["mattermost_user_id"])
	r.Equal("alice", ackEvent.Payload["mattermost_username"])

	notices := pendingAckNoticeJobs(t, s, s.org.UID)
	r.NotEmpty(notices)

	for _, cfg := range notices {
		r.NotEqual(mm.UID, cfg.ConnectionUID,
			"the connection whose post was updated must not get a duplicate ack notice")
	}
}
