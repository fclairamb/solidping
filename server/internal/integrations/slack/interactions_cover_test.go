package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

var errCoverBoom = errors.New("boom")

// coverIncidentSvc is a configurable IncidentService double.
type coverIncidentSvc struct {
	incident *models.Incident
	check    *models.Check
	ackErr   error
	getErr   error
	checkErr error
}

func (f *coverIncidentSvc) AcknowledgeIncidentFromSlack(
	_ context.Context, _, _, _, _, _ string,
) (*models.Incident, error) {
	return f.incident, f.ackErr
}

func (f *coverIncidentSvc) GetIncidentByUID(_ context.Context, _, _ string) (*models.Incident, error) {
	return f.incident, f.getErr
}

func (f *coverIncidentSvc) GetCheckByUID(_ context.Context, _, _ string) (*models.Check, error) {
	return f.check, f.checkErr
}

func (f *coverIncidentSvc) AddCommentFromSlack(
	_ context.Context, _, _, _, _, _, _, _ string,
) (*models.Event, error) {
	return &models.Event{}, nil
}

func (f *coverIncidentSvc) AddCommentFromSlackCommand(
	_ context.Context, _, _, _, _, _, _ string,
) (*models.Event, error) {
	return &models.Event{}, nil
}

// coverFakeSlack answers every Slack Web API call with ok:true and a small
// channel list, and counts the calls per method.
func coverFakeSlack(t *testing.T, svc *Service) map[string]int {
	t.Helper()

	var mu sync.Mutex

	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C-NEW","name":"alerts"}],"ts":"1.2"}`))
	}))
	t.Cleanup(srv.Close)

	svc.newAPIClient = func(token string) *Client { return NewClientWithBaseURL(token, srv.URL) }

	return calls
}

func TestDispatchInteractionCover(t *testing.T) {
	t.Parallel()

	h, svc, org := solidpingSetup(t)
	coverFakeSlack(t, svc)

	chk := models.NewCheck(org.UID, "api", "http")
	name := "API"
	chk.Name = &name
	chk.Config = models.JSONMap{"url": "https://acme.com", "method": "POST"}
	require.NoError(t, svc.db.CreateCheck(t.Context(), chk))

	inc := models.NewIncident(org.UID, chk.UID, time.Now().Add(-time.Minute), "down")
	inc.Details = models.JSONMap{"failure_reason": "timeout"}
	svc.incidentsService = &coverIncidentSvc{incident: inc, check: chk}

	team := Team{ID: msgTeamID}
	user := User{ID: "U1", Username: "alice"}
	act := func(id, value string) []InteractionAction {
		return []InteractionAction{{ActionID: id, Value: value}}
	}
	form := func(url, nm string) *View {
		return &View{CallbackID: "add_check_modal", State: &ViewState{Values: map[string]map[string]InputValue{
			"url_block":  {"url_input": {Value: url}},
			"name_block": {"name_input": {Value: nm}},
		}}}
	}

	tests := []struct {
		name string
		in   *Interaction
		want string
		err  bool
	}{
		{"view closed", &Interaction{Type: "view_closed", Team: team}, "", false},
		{"unknown type", &Interaction{Type: "zzz", Team: team}, "", false},
		{"shortcut add", &Interaction{Type: "shortcut", CallbackID: ActionAddCheck, Team: team}, "", false},
		{"shortcut other", &Interaction{Type: "shortcut", CallbackID: "x", Team: team}, "", false},
		{"shortcut no team", &Interaction{Type: "shortcut", CallbackID: ActionAddCheck, Team: Team{ID: "nope"}}, "", true},
		{"block add", &Interaction{Type: "block_actions", Team: team, Actions: act(ActionAddCheck, "")}, "", false},
		{"block dashboard", &Interaction{Type: "block_actions", Team: team, Actions: act("view_dashboard", "")}, "", false},
		{"block unknown", &Interaction{Type: "block_actions", Team: team, Actions: act("zzz", "")}, "", false},
		{
			"unavailable", &Interaction{Type: "block_actions", Team: team, Actions: act("unavailable_incident", "i")},
			"unacknowledged", false,
		},
		{
			"ack empty uid",
			&Interaction{Type: "block_actions", Team: team, Actions: act("acknowledge_incident", "")},
			"Invalid incident", false,
		},
		{
			"ack no conn",
			&Interaction{Type: "block_actions", Team: Team{ID: "nope"}, Actions: act("acknowledge_incident", "i")},
			"Could not find organization", false,
		},
		{"ack ok", &Interaction{
			Type: "block_actions", Team: team, User: user, Channel: Channel{ID: msgChannel},
			Message: &InteractionMessage{Ts: "1.1"}, Actions: act("acknowledge_incident", "i"),
		}, "acknowledged", false},
		{"ack ok container", &Interaction{
			Type: "block_actions", Team: team, User: user,
			Container: InteractionContainer{MessageTs: "1.1", ChannelID: msgChannel}, Actions: act("acknowledge_incident", "i"),
		}, "acknowledged", false},
		{
			"ack ok no ts",
			&Interaction{Type: "block_actions", Team: team, User: user, Actions: act("acknowledge_incident", "i")},
			"acknowledged", false,
		},
		{"ack ok no channel", &Interaction{
			Type: "block_actions", Team: team, User: user, Message: &InteractionMessage{Ts: "1.1"},
			Actions: act("acknowledge_incident", "i"),
		}, "acknowledged", false},
		{
			"escalate empty", &Interaction{Type: "block_actions", Team: team, Actions: act("escalate_incident", "")},
			"Invalid incident", false,
		},
		{
			"escalate no conn",
			&Interaction{Type: "block_actions", Team: Team{ID: "nope"}, Actions: act("escalate_incident", "i")},
			"Could not find organization", false,
		},
		{
			"escalate ok",
			&Interaction{Type: "block_actions", Team: team, User: user, Actions: act("escalate_incident", "i")},
			"Escalation requested", false,
		},
		{"view nil", &Interaction{Type: "view_submission", Team: team}, "", false},
		{"view unknown", &Interaction{Type: "view_submission", Team: team, View: &View{CallbackID: "zzz"}}, "", false},
		{
			"view no state",
			&Interaction{Type: "view_submission", Team: team, View: &View{CallbackID: "add_check_modal"}}, "", true,
		},
		{"view empty url", &Interaction{Type: "view_submission", Team: team, View: form("", "")}, "URL is required", false},
		{
			"view ok",
			&Interaction{Type: "view_submission", Team: team, User: user, View: form("https://acme.com", "n")}, "",
			false,
		},
		{
			"view ok no team",
			&Interaction{Type: "view_submission", Team: Team{ID: "nope"}, View: form("https://acme.com", "n")}, "",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, err := h.handleInteraction(t.Context(), tt.in)
			if tt.err {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
			require.NotNil(t, resp)
			if tt.want != "" {
				require.Contains(t, resp.Text+attachmentText(resp), tt.want)
			}
		})
	}
}

func attachmentText(resp *MessageResponse) string {
	var out strings.Builder
	for i := range resp.Attachments {
		out.WriteString(resp.Attachments[i].Fallback)
	}

	return out.String()
}

func TestEscalateAndAckErrorsCover(t *testing.T) {
	t.Parallel()

	h, svc, org := solidpingSetup(t)
	coverFakeSlack(t, svc)

	chk := models.NewCheck(org.UID, "api", "http")
	require.NoError(t, svc.db.CreateCheck(t.Context(), chk))
	inc := models.NewIncident(org.UID, chk.UID, time.Now(), "down")
	now := time.Now()
	escalated := *inc
	escalated.EscalatedAt = &now

	in := &Interaction{Type: "block_actions", Team: Team{ID: msgTeamID}, User: User{ID: "U1", Username: "a"}}
	run := func(id string) string {
		in.Actions = []InteractionAction{{ActionID: id, Value: "i"}}
		resp, err := h.handleInteraction(t.Context(), in)
		require.NoError(t, err)

		return resp.Text
	}

	svc.incidentsService = &coverIncidentSvc{ackErr: errCoverBoom}
	require.Contains(t, run("acknowledge_incident"), "Could not acknowledge")

	svc.incidentsService = &coverIncidentSvc{incident: inc, checkErr: errCoverBoom}
	require.Contains(t, run("acknowledge_incident"), "Acknowledged by")

	svc.incidentsService = &coverIncidentSvc{getErr: errCoverBoom}
	require.Contains(t, run("escalate_incident"), "Could not find incident")

	svc.incidentsService = &coverIncidentSvc{incident: &escalated}
	require.Contains(t, run("escalate_incident"), "already been escalated")
}

func TestCheckHelpersCover(t *testing.T) {
	t.Parallel()

	empty := models.NewCheck("o", "", "http")
	slug := models.NewCheck("o", "s", "http")
	empty.Config = nil

	require.Equal(t, "Unknown check", getCheckName(empty))
	require.Equal(t, "s", getCheckName(slug))
	require.Empty(t, getCheckURL(empty))
	require.Empty(t, getCheckURL(slug))
	require.Equal(t, "GET", getCheckMethod(empty))
	require.Equal(t, "GET", getCheckMethod(slug))

	inc := models.NewIncident("o", "c", time.Now(), "t")
	require.Equal(t, "Check failed", getFailureReason(inc))
	inc.Details = models.JSONMap{"output": "oops"}
	require.Equal(t, "oops", getFailureReason(inc))
	inc.Details = nil
	require.Equal(t, "Check failed", getFailureReason(inc))
	require.Contains(t, formatTimestamp(time.Now()), "today at")

	msg := buildAcknowledgedMessage(inc, slug, "alice", time.Now())
	require.True(t, msg.ReplaceOriginal)
}
