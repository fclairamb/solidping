package telegramcb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// TestComment_SingleOpenIncidentNeedsNoRef is the everyday case: one thing is
// broken, so a bare /comment lands on it.
func TestComment_SingleOpenIncidentNeedsNoRef(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCommandEnv(t, true)
	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.command(t, "/comment the central DNS is down")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Comment added")

	events := env.incidentComments(t, incident.UID)
	r.Len(events, 1)
	r.Equal("the central DNS is down", events[0].Payload["text"])
	// Attributed to the linked contact's SolidPing user, not to nobody.
	r.NotNil(events[0].ActorUID)
	r.Equal(env.user.UID, *events[0].ActorUID)
	r.Equal("telegram", events[0].Payload["source"])
}

// TestComment_ExplicitRefWins pins that a named incident beats the
// single-open-incident heuristic.
func TestComment_ExplicitRefWins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCommandEnv(t, true)
	first := env.seedIncident(t, env.seedCheck(t, "api"), time.Minute)
	second := env.seedIncident(t, env.seedCheck(t, "database"), time.Minute)

	env.command(t, "/comment #"+itoa(second.Number)+" restarting the primary")

	r.Empty(env.incidentComments(t, first.UID))

	events := env.incidentComments(t, second.UID)
	r.Len(events, 1)
	r.Equal("restarting the primary", events[0].Payload["text"])
}

// TestComment_AmbiguousListsCandidates proves the negative: with several
// incidents open and no reference, nothing is written and the bot asks.
func TestComment_AmbiguousListsCandidates(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCommandEnv(t, true)
	first := env.seedIncident(t, env.seedCheck(t, "api"), time.Minute)
	second := env.seedIncident(t, env.seedCheck(t, "database"), time.Minute)

	env.command(t, "/comment everything is on fire")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Which one?")

	r.Empty(env.incidentComments(t, first.UID))
	r.Empty(env.incidentComments(t, second.UID))
}

// TestComment_NoOpenIncidents answers rather than silently dropping the note.
func TestComment_NoOpenIncidents(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCommandEnv(t, true)

	env.command(t, "/comment anyone home?")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Equal(telegram.BuildNothingToCommentHTML(), msgs[0])
}

// TestComment_EmptyTextShowsUsage covers a bare /comment.
func TestComment_EmptyTextShowsUsage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := setupCommandEnv(t, true)
	env.seedIncident(t, env.seedCheck(t, "api"), time.Minute)

	env.command(t, "/comment")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Equal(telegram.BuildCommentUsageHTML(), msgs[0])
}

// TestSplitCommentArg pins the `#ref` split, including the case where the
// comment text itself must not lose its first word.
func TestSplitCommentArg(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, arg, wantRef, wantText string }{
		{"plain text", "the DNS is down", "", "the DNS is down"},
		{"explicit ref", "#42 restarting", "#42", "restarting"},
		{"qualified ref", "#acme:42 restarting", "#acme:42", "restarting"},
		{"ref only", "#42", "#42", ""},
		{"empty", "  ", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ref, text := splitCommentArg(tt.arg)
			r.Equal(tt.wantRef, ref)
			r.Equal(tt.wantText, text)
		})
	}
}

// incidentComments reads the incident.comment events recorded for an incident.
func (e *cmdEnv) incidentComments(t *testing.T, incidentUID string) []*models.Event {
	t.Helper()

	events, err := e.db.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: e.org.UID,
		IncidentUID:     &incidentUID,
		Limit:           50,
	})
	require.NoError(t, err)

	out := make([]*models.Event, 0, len(events))

	for _, event := range events {
		if event.EventType == models.EventTypeIncidentComment {
			out = append(out, event)
		}
	}

	return out
}

// itoa renders a positive incident number without importing strconv.
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
