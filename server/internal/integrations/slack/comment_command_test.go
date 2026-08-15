package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHandleMessage_ExplicitModeDoesNotIngestThreadReplies proves the negative
// the spec exists for: with the default (explicit) ingestion mode, a plain
// human thread reply in a tracked incident thread is NOT turned into an
// incident comment.
func TestHandleMessage_ExplicitModeDoesNotIngestThreadReplies(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-explicit", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	_, err := svc.db.SetStateEntryIfNotExists(ctx, nil,
		ReverseThreadStateKey(msgTeamID, msgChannel, msgThreadTS),
		&models.JSONMap{ThreadIncidentUIDKey: "incident-uid-1", ThreadOrgUIDKey: org.UID}, nil)
	r.NoError(err)

	seedSlackConnection(t, svc, org.UID, models.SlackCommentIngestionExplicit)

	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	r.NoError(DispatchEvent(ctx, svc, threadReply("1700000000.000700", "U-ALICE", "lunch?")))

	r.Empty(fake.comments, "a plain thread reply must not be ingested in explicit mode")
}

// TestHandleMessage_UnsetModeDefaultsToExplicit covers the migration case: an
// integration stored before comment_ingestion existed carries no key at all,
// and must stop over-capturing without any backfill.
func TestHandleMessage_UnsetModeDefaultsToExplicit(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-legacy", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	_, err := svc.db.SetStateEntryIfNotExists(ctx, nil,
		ReverseThreadStateKey(msgTeamID, msgChannel, msgThreadTS),
		&models.JSONMap{ThreadIncidentUIDKey: "incident-uid-1", ThreadOrgUIDKey: org.UID}, nil)
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "legacy")
	conn.Enabled = true
	conn.Settings = models.JSONMap{
		settingsKeyTeamID: msgTeamID, "access_token": "xoxb", "channel_id": msgChannel,
	}
	r.NoError(svc.db.CreateChannel(ctx, conn))

	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	r.NoError(DispatchEvent(ctx, svc, threadReply("1700000000.000800", "U-BOB", "who's on call?")))

	r.Empty(fake.comments)
}

// TestParseCommentArgs pins the `#N` split, including the case that must NOT
// be read as a reference.
func TestParseCommentArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, raw, wantRef, wantText string
	}{
		{"plain text", "the central DNS is down", "", "the central DNS is down"},
		{"explicit ref", "#42 restarting the pod", "42", "restarting the pod"},
		{"ref with extra spaces", "  #7   look here  ", "7", "look here"},
		{"hash but not a number", "#ops please look", "", "#ops please look"},
		{"ref with no text", "#42", "42", ""},
		{"empty", "   ", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ref, text := parseCommentArgs(tt.raw)
			r.Equal(tt.wantRef, ref)
			r.Equal(tt.wantText, text)
		})
	}
}

// commentSetup builds an org + a connected workspace and returns the handler
// under test.
func commentSetup(t *testing.T) (*Handler, *Service, *models.Organization, *fakeIncidentService) {
	t.Helper()
	r := require.New(t)
	ctx, svc := setupSlackService(t)

	org := models.NewOrganization("slack-comment-cmd", "")
	r.NoError(svc.db.CreateOrganization(ctx, org))

	seedSlackConnection(t, svc, org.UID, models.SlackCommentIngestionExplicit)

	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	return &Handler{svc: svc}, svc, org, fake
}

// openIncident creates an active incident with a Slack thread mapped into the
// test channel.
func openIncident(t *testing.T, svc *Service, orgUID, title, threadTS string) *models.Incident {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(orgUID, "api-"+threadTS, "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	incident := models.NewIncident(orgUID, check.UID, time.Now().Add(-time.Minute), title)
	r.NoError(svc.db.CreateIncident(ctx, incident))

	_, err := svc.db.SetStateEntryIfNotExists(ctx, nil,
		ReverseThreadStateKey(msgTeamID, msgChannel, threadTS),
		&models.JSONMap{ThreadIncidentUIDKey: incident.UID, ThreadOrgUIDKey: orgUID}, nil)
	r.NoError(err)

	return incident
}

func commentCmd(text string) *Command {
	return &Command{
		Command:   "/comment",
		Text:      text,
		TeamID:    msgTeamID,
		ChannelID: msgChannel,
		UserID:    "U-ALICE",
		UserName:  "alice",
	}
}

// TestCommentCommand_SingleTrackedIncident is the happy path: one active
// incident in the channel, no reference needed.
func TestCommentCommand_SingleTrackedIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org, fake := commentSetup(t)
	incident := openIncident(t, svc, org.UID, "api is down", "1700000001.000001")

	resp, err := h.handleCommentCommand(t.Context(), commentCmd("I think the central DNS is down"))
	r.NoError(err)
	r.NotNil(resp)
	r.Equal(ResponseTypeEphemeral, resp.ResponseType)
	r.Contains(resp.Text, "Comment added")

	r.Len(fake.commandComments, 1)
	r.Equal(incident.UID, fake.commandComments[0].incidentUID)
	r.Equal("I think the central DNS is down", fake.commandComments[0].text)
	r.Equal(org.UID, fake.commandComments[0].orgUID)
}

// TestCommentCommand_AmbiguousChannelListsCandidates proves the command never
// guesses: two active incidents in the channel produce an error listing both
// and write nothing.
func TestCommentCommand_AmbiguousChannelListsCandidates(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org, fake := commentSetup(t)
	first := openIncident(t, svc, org.UID, "api is down", "1700000002.000001")
	second := openIncident(t, svc, org.UID, "db is down", "1700000002.000002")

	resp, err := h.handleCommentCommand(t.Context(), commentCmd("both of these look bad"))
	r.NoError(err)
	r.NotNil(resp)
	r.Contains(resp.Text, "Several active incidents")
	r.Contains(resp.Text, incidentLabel(first))
	r.Contains(resp.Text, incidentLabel(second))
	r.Empty(fake.commandComments, "an ambiguous reference must never write a comment")
}

// TestCommentCommand_ExplicitRefWins asserts a named `#N` beats the channel
// heuristic, even when the channel is ambiguous.
func TestCommentCommand_ExplicitRefWins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org, fake := commentSetup(t)
	_ = openIncident(t, svc, org.UID, "api is down", "1700000003.000001")
	target := openIncident(t, svc, org.UID, "db is down", "1700000003.000002")

	cmd := commentCmd("#" + itoa(target.Number) + " restarting the primary")

	resp, err := h.handleCommentCommand(t.Context(), cmd)
	r.NoError(err)
	r.Contains(resp.Text, "Comment added")

	r.Len(fake.commandComments, 1)
	r.Equal(target.UID, fake.commandComments[0].incidentUID)
	r.Equal("restarting the primary", fake.commandComments[0].text)
}

// TestCommentCommand_NoTrackedIncident answers with the usage hint rather than
// silently dropping the note.
func TestCommentCommand_NoTrackedIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _, fake := commentSetup(t)

	resp, err := h.handleCommentCommand(t.Context(), commentCmd("anyone home?"))
	r.NoError(err)
	r.Contains(resp.Text, "No active SolidPing incident")
	r.Empty(fake.commandComments)
}

// TestCommentCommand_ResolvedIncidentIsNotACandidate keeps history out of the
// "exactly one active incident" rule.
func TestCommentCommand_ResolvedIncidentIsNotACandidate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, svc, org, fake := commentSetup(t)
	closed := openIncident(t, svc, org.UID, "old outage", "1700000004.000001")
	live := openIncident(t, svc, org.UID, "current outage", "1700000004.000002")

	resolved := models.IncidentStateResolved
	r.NoError(svc.db.UpdateIncident(t.Context(), closed.UID, &models.IncidentUpdate{State: &resolved}))

	resp, err := h.handleCommentCommand(t.Context(), commentCmd("on it"))
	r.NoError(err)
	r.Contains(resp.Text, "Comment added")
	r.Len(fake.commandComments, 1)
	r.Equal(live.UID, fake.commandComments[0].incidentUID)
}

// TestCommentCommand_EmptyTextShowsUsage covers the bare `/comment`.
func TestCommentCommand_EmptyTextShowsUsage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	h, _, _, fake := commentSetup(t)

	resp, err := h.handleCommentCommand(t.Context(), commentCmd("   "))
	r.NoError(err)
	r.Contains(resp.Text, "Usage:")
	r.Empty(fake.commandComments)
}

// TestDispatchCommand_RoutesComment asserts the slash command is wired into
// the transport-agnostic dispatcher, so it works over HTTP and Socket Mode
// alike.
func TestDispatchCommand_RoutesComment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, org, fake := commentSetup(t)
	incident := openIncident(t, svc, org.UID, "api is down", "1700000005.000001")

	resp, err := DispatchCommand(t.Context(), svc, commentCmd("dispatched"))
	r.NoError(err)
	r.NotNil(resp)
	r.NotContains(resp.Text, "Unknown command")
	r.Len(fake.commandComments, 1)
	r.Equal(incident.UID, fake.commandComments[0].incidentUID)
}

// itoa avoids importing strconv in the test for a single call.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}

	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}

	return digits
}
