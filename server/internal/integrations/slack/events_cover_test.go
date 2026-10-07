package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Subtests are order-dependent (app_uninstalled removes the workspace connection), so they run sequentially.
//
//nolint:paralleltest,tparallel // order-dependent subtests sharing one service
func TestDispatchEventCover(t *testing.T) {
	t.Parallel()

	h, svc, _ := solidpingSetup(t)
	calls := coverFakeSlack(t, svc)
	_ = h

	mk := func(typ string, mut func(*Event)) *Event {
		ev := &Event{TeamID: msgTeamID, Event: EventPayload{Type: typ, Channel: msgChannel, Ts: "1.1", User: "U1"}}
		if mut != nil {
			mut(ev)
		}

		return ev
	}

	tests := []struct {
		name    string
		ev      *Event
		wantErr bool
	}{
		{"unknown", mk("zzz", nil), false},
		{"home wrong tab", mk("app_home_opened", nil), false},
		{"home ok", mk("app_home_opened", func(e *Event) { e.Event.Tab = "home" }), false},
		{"home no team", mk("app_home_opened", func(e *Event) { e.Event.Tab = "home"; e.TeamID = "nope" }), true},
		{"mention help", mk("app_mention", func(e *Event) { e.Event.Text = "<@UBOT> help" }), false},
		{"uninstalled", mk("app_uninstalled", nil), false},
		{"joined other user", mk("member_joined_channel", nil), false},
		{"joined no team", mk("member_joined_channel", func(e *Event) { e.TeamID = "nope" }), false},
		{"joined bot", mk("member_joined_channel", func(e *Event) { e.Event.User = "" }), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DispatchEvent(t.Context(), svc, tt.ev)
			if tt.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
		})
	}

	require.NotZero(t, calls["/views.publish"])
}

func TestDispatchCommandCover(t *testing.T) {
	t.Parallel()

	_, svc, _ := solidpingSetup(t)

	for cmd, want := range map[string]string{
		"/check": "/solidping", "/comment": "/solidping", "/nope": "Unknown command", "/solidping": "Help",
	} {
		resp, err := DispatchCommand(t.Context(), svc, &Command{Command: cmd, TeamID: msgTeamID, ChannelID: msgChannel})
		require.NoError(t, err, cmd)
		require.NotNil(t, resp, cmd)
		require.Contains(t, resp.Text+firstBlockText(resp), want, cmd)
	}
}

func firstBlockText(resp *MessageResponse) string {
	if len(resp.Blocks) > 0 && resp.Blocks[0].Text != nil {
		return resp.Blocks[0].Text.Text
	}

	return ""
}

func TestClientMethodsCover(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/fail.method" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"nope"}`))

			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"U1","name":"alice","profile":{"email":"a@acme.com"}}}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithBaseURL("xoxb-t", srv.URL)
	ctx := t.Context()
	msg := &MessageResponse{
		Text:        "hi",
		Blocks:      []Block{{Type: BlockTypeSection}},
		Attachments: []Attachment{{Color: "#fff"}},
	}

	require.NoError(t, c.UpdateMessage(ctx, UpdateMessageOptions{Channel: "C", TS: "1", Message: msg}))
	require.NoError(t, c.UpdateMessage(ctx, UpdateMessageOptions{
		Channel: "C", TS: "1", Message: &MessageResponse{Text: "x"},
	}))
	require.NoError(t, c.PostEphemeral(ctx, "C", "U", msg))
	require.NoError(t, c.PostEphemeral(ctx, "C", "U", &MessageResponse{Text: "x"}))
	require.NoError(t, c.UnfurlLinks(ctx, "C", "1", map[string]Unfurl{"u": {}}))
	require.NoError(t, c.PublishView(ctx, "U", &AppHomeView{Type: "home"}))
	require.NoError(t, c.OpenModal(ctx, "trig", &ModalView{Type: "modal"}))
	require.NoError(t, c.UpdateModal(ctx, "v", "", &ModalView{Type: "modal"}))
	require.NoError(t, c.UpdateModal(ctx, "v", "h", &ModalView{Type: "modal"}))
	require.NoError(t, c.AddReaction(ctx, "C", "1", "eyes"))

	user, err := c.GetUserDetails(ctx, "U1")
	require.NoError(t, err)
	require.NotNil(t, user)

	require.Equal(t, int32(10)+1, hits.Load())

	bad := NewClientWithBaseURL("xoxb-t", "http://127.0.0.1:1")
	require.Error(t, bad.AddReaction(ctx, "C", "1", "eyes"))
	require.Error(t, bad.PostEphemeral(ctx, "C", "U", msg))
	_, err = bad.GetUserDetails(ctx, "U1")
	require.Error(t, err)
}

func TestServiceMiscCover(t *testing.T) {
	t.Parallel()

	_, svc, _ := solidpingSetup(t)

	svc.SetSupport(nil)
	svc.ReportDMCapability(t.Context())

	// No sync wired, or missing arguments: nothing runs.
	svc.runIdentitySync(t.Context(), "acme", "uid")
	svc.SetIdentitySync(func(context.Context, string, string) error { return errCoverBoom })
	svc.runIdentitySync(t.Context(), "", "uid")
	svc.runIdentitySync(t.Context(), "acme", "")

	done := make(chan struct{}, 1)
	svc.SetIdentitySync(func(context.Context, string, string) error {
		done <- struct{}{}

		return errCoverBoom
	})
	svc.runIdentitySync(t.Context(), "acme", "uid")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("identity sync never ran")
	}
}
