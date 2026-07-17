package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errFakeUnused is returned by the fake incident service methods that these
// tests never exercise, so the stubs don't trip the nilnil linter.
var errFakeUnused = errors.New("fake incident service: method not used in this test")

type recordedComment struct {
	orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID, slackTs string
}

// fakeIncidentService records AddCommentFromSlack calls; the other interface
// methods are unused by the message-handler tests.
type fakeIncidentService struct {
	comments []recordedComment
	err      error
}

func (f *fakeIncidentService) AcknowledgeIncidentFromSlack(
	_ context.Context, _, _, _, _ string,
) (*models.Incident, error) {
	return nil, errFakeUnused
}

func (f *fakeIncidentService) GetIncidentByUID(_ context.Context, _, _ string) (*models.Incident, error) {
	return nil, errFakeUnused
}

func (f *fakeIncidentService) GetCheckByUID(_ context.Context, _, _ string) (*models.Check, error) {
	return nil, errFakeUnused
}

func (f *fakeIncidentService) AddCommentFromSlack(
	_ context.Context, orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID, slackTs string,
) (*models.Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.comments = append(f.comments, recordedComment{
		orgUID, incidentUID, text, slackUserID, slackUserName, slackTeamID, slackTs,
	})

	return &models.Event{UID: "evt-" + slackTs}, nil
}

const (
	msgTeamID   = "T-MSG"
	msgChannel  = "C-MSG"
	msgThreadTS = "1700000000.000001"
)

// seedIncidentThread creates an org and the reverse thread→incident mapping the
// inbound handler resolves, returning the org UID and incident UID.
func seedIncidentThread(t *testing.T, svc *Service) (orgUID, incidentUID string) {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("slack-msg-test", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	incidentUID = "incident-uid-1"
	_, err := svc.db.SetStateEntryIfNotExists(ctx, nil,
		ReverseThreadStateKey(msgTeamID, msgChannel, msgThreadTS),
		&models.JSONMap{ThreadIncidentUIDKey: incidentUID, ThreadOrgUIDKey: org.UID}, nil)
	r.NoError(err)

	return org.UID, incidentUID
}

// threadReply builds a human thread-reply message event under the seeded
// incident thread.
func threadReply(ts, user, text string) *Event {
	return &Event{
		TeamID: msgTeamID,
		Event: EventPayload{
			Type:     "message",
			Channel:  msgChannel,
			ThreadTs: msgThreadTS,
			Ts:       ts,
			User:     user,
			Text:     text,
		},
	}
}

func TestHandleMessage_ThreadReplyBecomesComment(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgUID, incidentUID := seedIncidentThread(t, svc)
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	err := DispatchEvent(ctx, svc, threadReply("1700000000.000100", "U-ALICE", "restarting the pod"))
	r.NoError(err)

	r.Len(fake.comments, 1)
	c := fake.comments[0]
	r.Equal(orgUID, c.orgUID)
	r.Equal(incidentUID, c.incidentUID)
	r.Equal("restarting the pod", c.text)
	r.Equal("U-ALICE", c.slackUserID)
	r.Equal(msgTeamID, c.slackTeamID)
	r.Equal("1700000000.000100", c.slackTs)
	// No connection is configured, so the best-effort name lookup yields "".
	r.Equal("", c.slackUserName)

	// A dedupe marker is left behind so redelivery is a no-op.
	marker, err := svc.db.GetStateEntry(ctx, &orgUID, commentDedupeStateKey(msgTeamID, msgChannel, "1700000000.000100"))
	r.NoError(err)
	r.NotNil(marker)
}

func TestHandleMessage_DedupeOnRedelivery(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	seedIncidentThread(t, svc)
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	reply := threadReply("1700000000.000200", "U-BOB", "same message twice")
	r.NoError(DispatchEvent(ctx, svc, reply))
	r.NoError(DispatchEvent(ctx, svc, reply))

	r.Len(fake.comments, 1, "redelivered message must produce exactly one comment")
}

func TestHandleMessage_IgnoresNonThreadReplies(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	seedIncidentThread(t, svc)
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	// Top-level message: no thread_ts.
	topLevel := threadReply("1700000000.000300", "U-CAR", "top level")
	topLevel.Event.ThreadTs = ""
	r.NoError(DispatchEvent(ctx, svc, topLevel))

	// Thread parent: thread_ts equals its own ts.
	parent := threadReply("1700000000.000301", "U-CAR", "parent")
	parent.Event.ThreadTs = parent.Event.Ts
	r.NoError(DispatchEvent(ctx, svc, parent))

	r.Empty(fake.comments)
}

func TestHandleMessage_IgnoresBotAndSubtypeMessages(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	seedIncidentThread(t, svc)
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	// Bot-authored reply (e.g. our own resolve/reopen thread post).
	botReply := threadReply("1700000000.000400", "U-BOT", "resolved")
	botReply.Event.BotID = "B-SELF"
	r.NoError(DispatchEvent(ctx, svc, botReply))

	// Edited / deleted / system subtypes are not new human comments.
	for _, subtype := range []string{"message_changed", "message_deleted", "bot_message", "channel_join"} {
		ev := threadReply("1700000000.000401", "U-CAR", "subtype")
		ev.Event.Subtype = subtype
		r.NoError(DispatchEvent(ctx, svc, ev))
	}

	r.Empty(fake.comments)
}

func TestHandleMessage_IgnoresUnknownThread(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	// No reverse entry seeded — a reply in an untracked thread is silently
	// ignored, not an error.
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	err := DispatchEvent(ctx, svc, threadReply("1700000000.000500", "U-DAN", "who am I replying to"))
	r.NoError(err)
	r.Empty(fake.comments)
}

func TestHandleMessage_ReleasesDedupeMarkerOnFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	orgUID, _ := seedIncidentThread(t, svc)
	fake := &fakeIncidentService{err: errors.New("transient failure")}
	svc.incidentsService = fake

	reply := threadReply("1700000000.000600", "U-EVE", "will fail once")
	err := DispatchEvent(ctx, svc, reply)
	r.Error(err, "a failed comment write must surface an error so Slack retries")

	// The dedupe marker was rolled back, so a retry is not swallowed.
	marker, err := svc.db.GetStateEntry(ctx, &orgUID, commentDedupeStateKey(msgTeamID, msgChannel, "1700000000.000600"))
	r.NoError(err)
	r.Nil(marker, "dedupe marker must be released when the comment write fails")

	// Retry now succeeds.
	fake.err = nil
	r.NoError(DispatchEvent(ctx, svc, reply))
	r.Len(fake.comments, 1)
}
