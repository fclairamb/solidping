package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// slackRecorder is an httptest Slack API recording each call's method and body.
type slackRecorder struct {
	mu     sync.Mutex
	calls  []string
	bodies []map[string]any
}

func newSlackServer(t *testing.T, failMethod string) (*slackRecorder, *slack.Client) {
	t.Helper()
	rec := &slackRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		method := req.URL.Path[len("/chat."):]
		rec.mu.Lock()
		rec.calls = append(rec.calls, method)
		rec.bodies = append(rec.bodies, body)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == failMethod {
			_, _ = w.Write([]byte(`{"ok":false,"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"111.222","channel":"C42"}`))
	}))
	t.Cleanup(srv.Close)

	return rec, slack.NewClientWithBaseURL("xoxb-test", srv.URL)
}

func coverSlackPayload(eventType string) *Payload {
	name := "Acme API"
	resolvedAt := time.Now()

	return &Payload{
		EventType:  eventType,
		OrgSlug:    "acme",
		AppBaseURL: "https://app.acme.com",
		Incident: &models.Incident{
			UID: "inc-1", OrganizationUID: "org-1", Number: 7,
			StartedAt: resolvedAt.Add(-5 * time.Minute), ResolvedAt: &resolvedAt,
		},
		Check: &models.Check{UID: "chk-1", Name: &name},
		Integration: &models.Integration{Settings: models.JSONMap{
			"access_token": "xoxb-test", "channel_id": "C1", "team_id": "T1",
		}},
	}
}

func TestSlackSender_postNewMessage(t *testing.T) {
	t.Parallel()

	threadValue := func(ts any) *models.StateEntry {
		return &models.StateEntry{Value: &models.JSONMap{"thread_ts": ts}}
	}

	tests := []struct {
		name        string
		failMethod  string
		entry       *models.StateEntry
		setErr      error
		wantErr     bool
		wantSetKeys int
		wantThread  string
	}{
		{name: "new thread stores forward and reverse state", wantSetKeys: 2},
		{name: "existing thread replies without storing", entry: threadValue("9.9"), wantThread: "9.9"},
		{name: "empty thread ts is treated as new", entry: threadValue(""), wantSetKeys: 2},
		{name: "non-string thread ts is treated as new", entry: threadValue(12), wantSetKeys: 2},
		{name: "nil value entry is treated as new", entry: &models.StateEntry{}, wantSetKeys: 2},
		{name: "slack error is wrapped", failMethod: "postMessage", wantErr: true},
		{name: "state store error is wrapped", setErr: errDatabaseError, wantErr: true, wantSetKeys: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, client := newSlackServer(t, tc.failMethod)
			db := &mockDBService{
				setStateEntryFunc: func(context.Context, *string, string, *models.JSONMap, *time.Duration) error {
					return tc.setErr
				},
			}
			jctx := &jobdef.JobContext{DBService: db}

			sender := &SlackSender{}
			err := sender.postNewMessage(context.Background(), jctx, client,
				coverSlackPayload(eventTypeIncidentCreated), "C1", "incidents/inc-1/slack/thread", tc.entry)

			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
			r.Len(db.setStateCalls, tc.wantSetKeys)
			r.Len(rec.calls, 1)
			if tc.wantThread != "" {
				r.Equal(tc.wantThread, rec.bodies[0]["thread_ts"])
			} else {
				r.NotContains(rec.bodies[0], "thread_ts")
			}
		})
	}
}

func TestSlackSender_storeThreadInfo_NoTeamSkipsReverse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, client := newSlackServer(t, "")
	db := &mockDBService{}
	payload := coverSlackPayload(eventTypeIncidentCreated)
	payload.Integration.Settings = models.JSONMap{"access_token": "x", "channel_id": "C1"}

	err := (&SlackSender{}).postNewMessage(context.Background(), &jobdef.JobContext{DBService: db},
		client, payload, "C1", "k", nil)
	r.NoError(err)
	r.Len(db.setStateCalls, 1)
}

func TestSlackSender_ThreadHandlers(t *testing.T) {
	t.Parallel()

	entry := func(v models.JSONMap) *models.StateEntry { return &models.StateEntry{Value: &v} }
	full := models.JSONMap{"message_id": "1.1", "channel_id": "C42", "thread_ts": "1.1"}

	tests := []struct {
		name      string
		event     string
		entry     *models.StateEntry
		fail      string
		wantCalls []string
		wantErr   string
	}{
		{"resolve updates then replies", eventTypeIncidentResolved, entry(full), "", []string{"update", "postMessage"}, ""},
		{
			"resolve without thread_ts only updates", eventTypeIncidentResolved,
			entry(models.JSONMap{"message_id": "1.1", "channel_id": "C42"}), "",
			[]string{"update"},
			"",
		},
		{
			"resolve without message id is a no-op", eventTypeIncidentResolved,
			entry(models.JSONMap{"channel_id": "C42"}), "", nil, "",
		},
		{
			"resolve without channel is a no-op", eventTypeIncidentResolved,
			entry(models.JSONMap{"message_id": "1.1"}), "", nil, "",
		},
		{
			"resolve update failure", eventTypeIncidentResolved, entry(full), "update",
			[]string{"update"},
			"updating slack message",
		},
		{
			"resolve reply failure", eventTypeIncidentResolved, entry(full), "postMessage",
			[]string{"update", "postMessage"},
			"posting thread reply",
		},
		{"reopen updates then replies", eventTypeIncidentReopened, entry(full), "", []string{"update", "postMessage"}, ""},
		{
			"reopen without thread_ts only updates", eventTypeIncidentReopened,
			entry(models.JSONMap{"message_id": "1.1", "channel_id": "C42"}), "",
			[]string{"update"},
			"",
		},
		{"reopen without ids is a no-op", eventTypeIncidentReopened, entry(models.JSONMap{}), "", nil, ""},
		{
			"reopen update failure", eventTypeIncidentReopened, entry(full), "update",
			[]string{"update"},
			"updating slack message for reopen",
		},
		{
			"reopen reply failure", eventTypeIncidentReopened, entry(full), "postMessage",
			[]string{"update", "postMessage"},
			"posting reopen thread reply",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, client := newSlackServer(t, tc.fail)
			sender := &SlackSender{}
			payload := coverSlackPayload(tc.event)

			var err error
			if tc.event == eventTypeIncidentResolved {
				err = sender.handleIncidentResolution(context.Background(), client, tc.entry, payload)
			} else {
				err = sender.handleIncidentReopen(context.Background(), client, tc.entry, payload)
			}

			if tc.wantErr != "" {
				r.ErrorContains(err, tc.wantErr)
			} else {
				r.NoError(err)
			}
			r.Equal(tc.wantCalls, rec.calls)
		})
	}
}

func TestSlackSender_Send_ResolvedAndReopenedViaThread(t *testing.T) {
	t.Parallel()

	for _, event := range []string{eventTypeIncidentResolved, eventTypeIncidentReopened} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			// Send builds its own client against slack.com, so only the
			// no-network branches are exercised here: a missing thread entry
			// must never post.
			db := &mockDBService{}
			err := (&SlackSender{}).Send(context.Background(),
				&jobdef.JobContext{DBService: db}, coverSlackPayload(event))
			r.NoError(err)
			r.Empty(db.setStateCalls)
		})
	}
}

func TestSlackHelpers_Cover(t *testing.T) {
	t.Parallel()

	name := "Acme API"
	empty := ""
	slug := "acme-api"

	t.Run("getCheckName", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			check *models.Check
			want  string
		}{
			{"name wins", &models.Check{Name: &name, Slug: &slug}, "Acme API"},
			{"slug fallback", &models.Check{Slug: &slug}, "acme-api"},
			{"empty name uses slug", &models.Check{Name: &empty, Slug: &slug}, "acme-api"},
			{"nothing", &models.Check{Name: &empty, Slug: &empty}, "Unknown check"},
			{"nil fields", &models.Check{}, "Unknown check"},
		}
		for _, tc := range tests {
			require.Equal(t, tc.want, getCheckName(tc.check), tc.name)
		}
	})

	t.Run("getCheckURL", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			check *models.Check
			want  string
		}{
			{"nil config", &models.Check{}, ""},
			{"url set", &models.Check{Config: models.JSONMap{"url": "https://acme.com"}}, "https://acme.com"},
			{"url wrong type", &models.Check{Config: models.JSONMap{"url": 5}}, ""},
			{"no url key", &models.Check{Config: models.JSONMap{}}, ""},
		}
		for _, tc := range tests {
			require.Equal(t, tc.want, getCheckURL(tc.check), tc.name)
		}
	})

	t.Run("getCheckMethod", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			check *models.Check
			want  string
		}{
			{"nil config", &models.Check{}, "GET"},
			{"post", &models.Check{Config: models.JSONMap{"method": "POST"}}, "POST"},
			{"empty method", &models.Check{Config: models.JSONMap{"method": ""}}, "GET"},
			{"wrong type", &models.Check{Config: models.JSONMap{"method": 1}}, "GET"},
			{"missing", &models.Check{Config: models.JSONMap{}}, "GET"},
		}
		for _, tc := range tests {
			require.Equal(t, tc.want, getCheckMethod(tc.check), tc.name)
		}
	})

	t.Run("buildSimpleMessage", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		msg := (&SlackSender{}).buildSimpleMessage(&Payload{Check: &models.Check{Name: &name}})
		r.Equal("Incident update for *Acme API*", msg.Text)
		r.Len(msg.Blocks, 1)
		r.Equal(msg.Text, msg.Blocks[0].Text.Text)

		// Unknown event types route here through buildMessage.
		fallback := (&SlackSender{}).buildMessage(&Payload{EventType: "incident.weird", Check: &models.Check{Slug: &slug}})
		r.Contains(fallback.Text, "acme-api")
	})

	t.Run("determineChannel override", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		settings := &models.SlackSettings{ChannelID: "C1"}
		s := &SlackSender{}
		r.Equal("C1", s.determineChannel(settings, &Payload{}))
		over := models.JSONMap{"channel_id": "C9"}
		r.Equal("C9", s.determineChannel(settings, &Payload{CheckConnectionSettings: &over}))
		blank := models.JSONMap{"channel_id": ""}
		r.Equal("C1", s.determineChannel(settings, &Payload{CheckConnectionSettings: &blank}))
	})
}
