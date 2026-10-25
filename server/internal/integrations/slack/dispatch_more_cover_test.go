package slack

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestLinkSharedUnfurlsOwnLinks pins that link_shared reaches the base URL
// through DispatchEvent (it used to dereference a nil config and panic) and
// only unfurls links to this instance.
func TestLinkSharedUnfurlsOwnLinks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, _ := solidpingSetup(t)
	calls := coverFakeSlack(t, svc)

	event := &Event{TeamID: msgTeamID, Event: EventPayload{
		Type: "link_shared", Channel: msgChannel, MessageTs: "1.1",
		Links: []Link{
			{Domain: "localhost", URL: "http://localhost:4000/d/orgs/acme/checks/api"},
			{Domain: "acme.com", URL: "https://acme.com/x"},
		},
	}}
	r.NoError(DispatchEvent(t.Context(), svc, event))
	r.Equal(1, calls["/chat.unfurl"])

	// Only foreign links: nothing to unfurl.
	event.Event.Links = event.Event.Links[1:]
	r.NoError(DispatchEvent(t.Context(), svc, event))
	r.Equal(1, calls["/chat.unfurl"])

	// No base URL configured: nothing to unfurl.
	svc.cfg.Server.BaseURL = ""
	r.NoError(DispatchEvent(t.Context(), svc, event))

	// Unknown workspace.
	event.TeamID = "T-UNKNOWN"
	r.Error(DispatchEvent(t.Context(), svc, event))
}

func TestMemberJoinedChannelAutoConfiguresDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, org := solidpingSetup(t)
	calls := coverFakeSlack(t, svc)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeSlack, "second")
	conn.Enabled = true
	conn.Settings = models.JSONMap{settingsKeyTeamID: "T-NOCHAN", "access_token": "xoxb-2", "bot_user_id": "UBOT"}
	r.NoError(svc.db.CreateChannel(t.Context(), conn))

	joined := func(team, user string) *Event {
		return &Event{TeamID: team, Event: EventPayload{Type: "member_joined_channel", Channel: "C-JOIN", User: user}}
	}

	// Someone else joined: ignored.
	r.NoError(DispatchEvent(t.Context(), svc, joined("T-NOCHAN", "U-OTHER")))
	// The seeded workspace already has a default channel.
	r.NoError(DispatchEvent(t.Context(), svc, joined(msgTeamID, "")))
	// Our bot joined a workspace without a default channel: configured.
	r.NoError(DispatchEvent(t.Context(), svc, joined("T-NOCHAN", "UBOT")))

	got, err := svc.GetConnectionByTeamID(t.Context(), "T-NOCHAN")
	r.NoError(err)
	settings, err := models.SlackSettingsFromJSONMap(got.Settings)
	r.NoError(err)
	r.Equal("C-JOIN", settings.ChannelID)
	r.NotZero(calls["/chat.postMessage"])
}

func TestSocketSupervisorHandleEventKinds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)
	ctx := t.Context()

	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeConnecting})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeConnected})
	r.True(sup.GetStatus().Connected)
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeConnectionError, Data: errors.New("dial")})
	r.False(sup.GetStatus().Connected)
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeInvalidAuth})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeDisconnect})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeHello})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeErrorBadMessage})
	r.NotEmpty(sup.GetStatus().LastError)

	// Events API: no request, empty payload, bad payload.
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeEventsAPI})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "e0"}})
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "e1", Payload: []byte("{")}})

	// Slash command with the wrong payload type.
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeSlashCommand, Data: "nope"})

	// Interactive: wrong payload type, then a view_closed callback that is acked.
	before := fake.ackCount()
	sup.handleEvent(ctx, fake, socketmode.Event{Type: socketmode.EventTypeInteractive, Data: 42})
	sup.handleEvent(ctx, fake, socketmode.Event{
		Type:    socketmode.EventTypeInteractive,
		Request: &socketmode.Request{EnvelopeID: "i1"},
		Data:    slackgo.InteractionCallback{Type: slackgo.InteractionTypeViewClosed, Team: slackgo.Team{ID: "T1"}},
	})
	sup.handleEvent(ctx, fake, socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slackgo.InteractionCallback{Type: "something_else"},
	})
	r.Equal(before+1, fake.ackCount())
}

func TestLookupUserByEmail(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	responses := map[string]string{
		"alice@acme.com":  `{"ok":true,"user":{"id":"U1","name":"alice"}}`,
		"bot@acme.com":    `{"ok":true,"user":{"id":"U2","name":"bot","is_bot":true}}`,
		"gone@acme.com":   `{"ok":true,"user":{"id":"U3","name":"gone","deleted":true}}`,
		"nobody@acme.com": `{"ok":false,"error":"users_not_found"}`,
		"broken@acme.com": `{"ok":false,"error":"invalid_auth"}`,
		"noid@acme.com":   `{"ok":true,"user":{}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.NoError(req.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[req.Form.Get("email")]))
	}))
	t.Cleanup(srv.Close)

	client := NewClientWithBaseURL("xoxb-t", srv.URL)
	user, found, err := client.LookupUserByEmail(t.Context(), "alice@acme.com")
	r.NoError(err)
	r.True(found)
	r.Equal("U1", user.ID)

	for _, email := range []string{"bot@acme.com", "gone@acme.com", "nobody@acme.com", "noid@acme.com"} {
		_, found, err = client.LookupUserByEmail(t.Context(), email)
		r.NoError(err, email)
		r.False(found, email)
	}

	_, _, err = client.LookupUserByEmail(t.Context(), "broken@acme.com")
	r.Error(err)
}
