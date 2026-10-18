package incidents_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// TestAddCommentFromA2A_AttributesToTheIntegration: an AI agent's reply is
// authored by the integration, never by a user, and fans out like any
// comment, but not to the A2A channel itself (no loop back to the agent).
func TestAddCommentFromA2A_AttributesToTheIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)
	ctx := t.Context()

	a2a := attachChannel(t, s, models.ConnectionTypeA2A, "investigator", nil)

	event, err := s.svc.AddCommentFromA2A(
		ctx, s.org.UID, s.incident.UID, a2a.UID, a2a.Name, "  Root cause: expired certificate  ",
	)
	r.NoError(err)
	r.Nil(event.ActorUID)
	r.Equal(models.ActorTypeService, event.ActorType)
	r.Equal(incidents.CommentSourceA2A, event.Payload["source"])
	r.Equal(a2a.UID, event.Payload["integrationUid"])
	r.Equal("AI agent (A2A): investigator", event.Payload["authorName"])
	r.Equal("Root cause: expired certificate", event.Payload["text"])

	targets, configs := commentJobTargets(t, s.dbSvc, s.org.UID)
	r.Contains(targets, s.connUID, "positive control: the webhook channel hears it")
	r.NotContains(targets, a2a.UID, "the agent's own reply never goes back to it")

	for _, cfg := range configs {
		r.Equal("AI agent (A2A): investigator", cfg.Comment.AuthorName)
		r.Equal(incidents.CommentSourceA2A, cfg.Comment.Source)
	}
}

// TestAddCommentFromA2A_UntrustedTextIsHandledLikeAUserComment is the
// negative case: HTML and markdown links in the agent's text are neither
// rendered nor pre-escaped on the way in. The stored body and the body every
// channel receives are byte-for-byte what a dashboard user typing the same
// text produces, so the dashboard and each sender escape both identically.
func TestAddCommentFromA2A_UntrustedTextIsHandledLikeAUserComment(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	hostile := `<script>alert(1)</script> <img src=x onerror=alert(2)> [click me](https://evil.acme.com) <@channel>`

	userSetup := newResolveSetup(t)
	user := models.NewUser("ops@acme.com")
	r.NoError(userSetup.dbSvc.CreateUser(ctx, user))

	userEvent, err := userSetup.svc.AddComment(ctx, userSetup.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: userSetup.incident.UID, Text: hostile, Source: incidents.CommentSourceWeb, ActorUID: user.UID,
	})
	r.NoError(err)

	agentSetup := newResolveSetup(t)
	agentEvent, err := agentSetup.svc.AddCommentFromA2A(
		ctx, agentSetup.org.UID, agentSetup.incident.UID, "integration-uid", "investigator", hostile,
	)
	r.NoError(err)

	r.Equal(userEvent.Payload["text"], agentEvent.Payload["text"])
	r.Equal(hostile, agentEvent.Payload["text"])

	userTargets, _ := commentJobTargets(t, userSetup.dbSvc, userSetup.org.UID)
	agentTargets, _ := commentJobTargets(t, agentSetup.dbSvc, agentSetup.org.UID)
	r.Equal(hostile, userTargets[userSetup.connUID])
	r.Equal(userTargets[userSetup.connUID], agentTargets[agentSetup.connUID])
}

// TestAddCommentFromA2A_TruncatesLongReports keeps a long agent report
// instead of refusing it, cut to the comment size limit.
func TestAddCommentFromA2A_TruncatesLongReports(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newResolveSetup(t)

	event, err := s.svc.AddCommentFromA2A(
		t.Context(), s.org.UID, s.incident.UID, "integration-uid", "investigator",
		strings.Repeat("é", incidents.MaxCommentLength),
	)
	r.NoError(err)

	text, _ := event.Payload["text"].(string)
	r.LessOrEqual(len(text), incidents.MaxCommentLength)
	r.True(strings.HasSuffix(text, "…"))
}
