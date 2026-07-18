package incidents_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TestAddComment_WebSource verifies a dashboard comment is stored as an
// append-only incident.comment event attributed to the authoring user.
func TestAddComment_WebSource(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	// actor_uid FKs users(uid), so the author must be a real user.
	user := models.NewUser("responder@example.com")
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	event, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "  restarting the pod  ",
		Source:      incidents.CommentSourceWeb,
		ActorUID:    user.UID,
	})
	r.NoError(err)
	r.NotNil(event)
	r.Equal(models.EventTypeIncidentComment, event.EventType)
	r.Equal(models.ActorTypeUser, event.ActorType)
	r.NotNil(event.ActorUID)
	r.Equal(user.UID, *event.ActorUID)
	r.NotNil(event.IncidentUID)
	r.Equal(s.incident.UID, *event.IncidentUID)
	// Text is trimmed and the source recorded on the payload.
	r.Equal("restarting the pod", event.Payload["text"])
	r.Equal(incidents.CommentSourceWeb, event.Payload["source"])
	// Web comments carry no Slack attribution.
	_, hasSlack := event.Payload["slackUserId"]
	r.False(hasSlack)

	// The event is persisted and readable through the events list.
	events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
	var comments int
	for _, e := range events {
		if e.EventType == models.EventTypeIncidentComment {
			comments++
		}
	}
	r.Equal(1, comments)
}

// TestAddCommentFromSlack_Attribution verifies a Slack-authored comment records
// its Slack attribution in the payload and leaves ActorUID empty (the author
// usually has no SolidPing user).
func TestAddCommentFromSlack_Attribution(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	event, err := s.svc.AddCommentFromSlack(
		ctx, s.org.UID, s.incident.UID, "escalating to the DBA", "U123", "alice", "T999", "1700000000.000100",
	)
	r.NoError(err)
	r.NotNil(event)
	r.Equal(models.EventTypeIncidentComment, event.EventType)
	r.Equal(models.ActorTypeUser, event.ActorType)
	r.Nil(event.ActorUID)
	r.Equal("escalating to the DBA", event.Payload["text"])
	r.Equal(incidents.CommentSourceSlack, event.Payload["source"])
	r.Equal("U123", event.Payload["slackUserId"])
	r.Equal("alice", event.Payload["slackUserName"])
	r.Equal("T999", event.Payload["slackTeamId"])
	r.Equal("1700000000.000100", event.Payload["slackTs"])
}

// TestAddComment_Validation covers empty and over-length bodies.
func TestAddComment_Validation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		text    string
		wantErr error
	}{
		{name: "empty", text: "", wantErr: incidents.ErrCommentEmpty},
		{name: "whitespace only", text: "   \n\t ", wantErr: incidents.ErrCommentEmpty},
		{
			name:    "over max length",
			text:    strings.Repeat("a", incidents.MaxCommentLength+1),
			wantErr: incidents.ErrCommentTooLong,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			s := newResolveSetup(t)

			_, err := s.svc.AddComment(t.Context(), s.org.Slug, &incidents.AddCommentRequest{
				IncidentUID: s.incident.UID,
				Text:        tc.text,
				Source:      incidents.CommentSourceWeb,
				ActorUID:    "user-x",
			})
			r.ErrorIs(err, tc.wantErr)

			// Nothing is persisted on a rejected comment.
			events := listIncidentEvents(t, s.dbSvc, s.org.UID, s.incident.UID)
			for _, e := range events {
				r.NotEqual(models.EventTypeIncidentComment, e.EventType)
			}
		})
	}
}

// TestAddComment_MaxLengthBoundary confirms a comment exactly at the cap is
// accepted (the limit is inclusive).
func TestAddComment_MaxLengthBoundary(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	user := models.NewUser("boundary@example.com")
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        strings.Repeat("a", incidents.MaxCommentLength),
		Source:      incidents.CommentSourceWeb,
		ActorUID:    user.UID,
	})
	r.NoError(err)
}

// TestAddComment_UnknownOrg rejects a comment for a non-existent org.
func TestAddComment_UnknownOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)

	_, err := s.svc.AddComment(t.Context(), "no-such-org", &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "hello",
		Source:      incidents.CommentSourceWeb,
		ActorUID:    "user-x",
	})
	r.ErrorIs(err, incidents.ErrOrganizationNotFound)
}

// TestAddComment_UnknownIncident rejects a comment for a non-existent incident
// (also covers the cross-org case: an incident UID not in this org resolves to
// not-found).
func TestAddComment_UnknownIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)

	_, err := s.svc.AddComment(t.Context(), s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: "00000000-0000-0000-0000-000000000000",
		Text:        "hello",
		Source:      incidents.CommentSourceWeb,
		ActorUID:    "user-x",
	})
	r.ErrorIs(err, incidents.ErrIncidentNotFound)
}
