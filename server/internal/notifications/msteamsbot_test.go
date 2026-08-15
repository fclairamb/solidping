package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/msteams"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	botTestAppID     = "bot-app-id"
	botTestAppSecret = "bot-app-secret"
	botTestConvID    = "19:channel-a"
)

// botCall is one request the fake Bot Connector received.
type botCall struct {
	Method   string
	Path     string
	Activity msteams.Activity
}

// botFake stands in for the Bot Connector REST API. A live round-trip would
// need a real Azure Bot registration, which nothing in this repo can supply,
// so the send/update/reply contract is pinned against this fake instead.
type botFake struct {
	server *httptest.Server

	mu    sync.Mutex
	calls []botCall
}

func newBotFake(t *testing.T) *botFake {
	t.Helper()

	fake := &botFake{}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	mux.HandleFunc("/v3/conversations/", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		var activity msteams.Activity
		_ = json.Unmarshal(body, &activity)

		fake.mu.Lock()
		fake.calls = append(fake.calls, botCall{Method: req.Method, Path: req.URL.Path, Activity: activity})
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "posted-activity-id"})
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *botFake) recorded() []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]botCall, len(f.calls))
	copy(out, f.calls)

	return out
}

// sender builds a MSTeamsBotSender wired to this fake. A per-fake app id keeps
// the connector client's process-wide token cache from leaking between
// parallel tests.
func (f *botFake) sender(appID string) *MSTeamsBotSender {
	return &MSTeamsBotSender{
		newClient: func(_, _, serviceURL string) *msteams.Client {
			return msteams.NewClientWithTokenURL(
				appID, botTestAppSecret, serviceURL, f.server.URL+"/oauth/token",
			)
		},
	}
}

// botJCtx builds a job context with a state-entry mock and app credentials.
func botJCtx(db *mockDBService) *jobdef.JobContext {
	return &jobdef.JobContext{
		DBService: db,
		AppConfig: &config.Config{
			MSTeams: config.MSTeamsConfig{
				Enabled: true, AppID: botTestAppID, AppSecret: botTestAppSecret,
			},
		},
	}
}

// botPayload builds a payload for a connection whose default destination is
// botTestConvID.
func botPayload(t *testing.T, eventType string, serviceURL string) *Payload {
	t.Helper()

	settings := &models.MSTeamsBotSettings{
		TenantID:    "tenant-1",
		ServiceURL:  serviceURL,
		ChannelID:   botTestConvID,
		ChannelName: "alerts",
		Destinations: []models.MSTeamsDestination{
			{ID: botTestConvID, Name: "alerts", ServiceURL: serviceURL, Type: "channel"},
			{ID: "19:channel-b", Name: "incidents", ServiceURL: serviceURL, Type: "channel"},
		},
	}

	settingsMap, err := settings.ToJSONMap()
	require.NoError(t, err)

	name := "API health"
	resolved := time.Now()

	incident := &models.Incident{
		UID:             "inc-teams-1",
		OrganizationUID: "org-1",
		StartedAt:       time.Now().Add(-5 * time.Minute),
		RelapseCount:    2,
		Details:         models.JSONMap{"failure_reason": "upstream returned 503"},
	}

	if eventType == eventTypeIncidentResolved {
		incident.ResolvedAt = &resolved
	}

	return &Payload{
		EventType:  eventType,
		Incident:   incident,
		Check:      &models.Check{UID: "chk-1", Name: &name, Type: "http"},
		OrgSlug:    "acme",
		AppBaseURL: "https://monitor.example.com",
		Integration: &models.Integration{
			UID: "conn-1", OrganizationUID: "org-1",
			Type: models.ConnectionTypeMSTeamsBot, Settings: settingsMap,
		},
	}
}

// storedCardDB returns a mockDBService whose state entry points at an
// already-posted card served by this fake.
func (f *botFake) storedCardDB() *mockDBService {
	return &mockDBService{
		getStateEntryFunc: func(_ context.Context, _ *string, _ string) (*models.StateEntry, error) {
			value := models.JSONMap{
				msTeamsBotKeyConversationID: botTestConvID,
				msTeamsBotKeyActivityID:     "existing-activity",
				msTeamsBotKeyServiceURL:     f.server.URL,
			}

			return &models.StateEntry{Value: &value}, nil
		},
	}
}

// payload builds a payload whose destination points at this fake connector.
func (f *botFake) payload(t *testing.T, eventType string) *Payload {
	t.Helper()

	return botPayload(t, eventType, f.server.URL)
}

// TestMSTeamsBotSender_CreatedPostsCardAndStoresID covers the first half of
// the spec's "proactive send + card update" criterion.
func TestMSTeamsBotSender_CreatedPostsCardAndStoresID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)
	db := &mockDBService{}

	err := fake.sender("bot-created").Send(
		context.Background(), botJCtx(db), fake.payload(t, eventTypeIncidentCreated))
	r.NoError(err)

	calls := fake.recorded()
	r.Len(calls, 1)
	r.Equal(http.MethodPost, calls[0].Method)
	r.Equal("/v3/conversations/19:channel-a/activities", calls[0].Path)
	r.Contains(calls[0].Activity.Text, "is DOWN")
	r.NotEmpty(calls[0].Activity.Attachments)
	r.Equal(msteams.AdaptiveCardContentType, calls[0].Activity.Attachments[0].ContentType)

	// The activity id must be recorded so later events can update this card.
	r.Len(db.setStateCalls, 1)
	r.Equal("incidents/inc-teams-1/msteams/thread", db.setStateCalls[0].key)
	r.Equal("posted-activity-id", (*db.setStateCalls[0].value)[msTeamsBotKeyActivityID])
	r.Equal(botTestConvID, (*db.setStateCalls[0].value)[msTeamsBotKeyConversationID])
}

// TestMSTeamsBotSender_EscalationUpdatesInPlace is the behavior that
// distinguishes this sender from the one-way webhook one: an escalation must
// rewrite the existing card, not add a second one.
func TestMSTeamsBotSender_EscalationUpdatesInPlace(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	err := fake.sender("bot-escalated").Send(
		context.Background(), botJCtx(fake.storedCardDB()), fake.payload(t, eventTypeIncidentEscalated))
	r.NoError(err)

	calls := fake.recorded()
	r.Len(calls, 1, "escalation must update the card, not post another one")
	r.Equal(http.MethodPut, calls[0].Method)
	r.Equal("/v3/conversations/19:channel-a/activities/existing-activity", calls[0].Path)
	r.Contains(calls[0].Activity.Text, "ESCALATED")
}

// TestMSTeamsBotSender_ResolutionUpdatesAndReplies covers the thread-grouping
// half: the card flips to resolved AND a reply lands under it.
func TestMSTeamsBotSender_ResolutionUpdatesAndReplies(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	err := fake.sender("bot-resolved").Send(
		context.Background(), botJCtx(fake.storedCardDB()), fake.payload(t, eventTypeIncidentResolved))
	r.NoError(err)

	calls := fake.recorded()
	r.Len(calls, 2)

	r.Equal(http.MethodPut, calls[0].Method)
	r.Contains(calls[0].Activity.Text, "RECOVERED")

	// The reply is a POST to the original activity's id, which is what makes
	// Teams thread it under the incident card.
	r.Equal(http.MethodPost, calls[1].Method)
	r.Equal("/v3/conversations/19:channel-a/activities/existing-activity", calls[1].Path)
	r.Equal("existing-activity", calls[1].Activity.ReplyToID)
	r.Contains(calls[1].Activity.Text, "recovered")
}

// TestMSTeamsBotSender_ResolvedWithoutCardIsSkipped is the orphan guard,
// mirroring SlackSender: a resolution with nothing to update must not post a
// context-free standalone message.
func TestMSTeamsBotSender_ResolvedWithoutCardIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	err := fake.sender("bot-orphan").Send(
		context.Background(), botJCtx(&mockDBService{}), fake.payload(t, eventTypeIncidentResolved))
	r.NoError(err)
	r.Empty(fake.recorded())
}

// TestMSTeamsBotSender_ReopenedUpdatesAndReplies covers the relapse path.
func TestMSTeamsBotSender_ReopenedUpdatesAndReplies(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	err := fake.sender("bot-reopened").Send(
		context.Background(), botJCtx(fake.storedCardDB()), fake.payload(t, eventTypeIncidentReopened))
	r.NoError(err)

	calls := fake.recorded()
	r.Len(calls, 2)
	r.Contains(calls[0].Activity.Text, "REOPENED")
	r.Contains(calls[1].Activity.Text, "relapse #2")
}

// TestMSTeamsBotSender_PerCheckOverrideRedirectsConversation covers the
// check-level destination override to another CAPTURED conversation.
func TestMSTeamsBotSender_PerCheckOverrideRedirectsConversation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentCreated)
	override := models.JSONMap{msTeamsBotConversationOverrideKey: "19:channel-b"}
	payload.CheckConnectionSettings = &override

	err := fake.sender("bot-override").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.NoError(err)

	calls := fake.recorded()
	r.Len(calls, 1)
	r.Equal("/v3/conversations/19:channel-b/activities", calls[0].Path)
}

func TestMSTeamsBotSender_MissingCredentialsErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	jctx := botJCtx(&mockDBService{})
	jctx.AppConfig.MSTeams.AppSecret = ""

	err := fake.sender("bot-nocreds").Send(
		context.Background(), jctx, fake.payload(t, eventTypeIncidentCreated))
	r.ErrorIs(err, ErrMSTeamsBotNotConfigured)
}

func TestMSTeamsBotSender_UninstalledConnectionErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentCreated)
	payload.Integration.Settings["uninstalled_at"] = time.Now().Format(time.RFC3339)

	err := fake.sender("bot-uninstalled").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.ErrorIs(err, ErrMSTeamsBotUninstalled)
}

func TestMSTeamsBotSender_NoDestinationErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentCreated)
	payload.Integration.Settings["channel_id"] = ""

	err := fake.sender("bot-nodest").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.ErrorIs(err, ErrMSTeamsBotNoDestination)
}

// TestMSTeamsBotSender_MissingServiceURLErrors covers a connection that never
// captured a connector base URL — there is nowhere to send.
func TestMSTeamsBotSender_MissingServiceURLErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := botPayload(t, eventTypeIncidentCreated, "")

	err := fake.sender("bot-nosvc").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.ErrorIs(err, msteams.ErrMissingServiceURL)
}

// TestMSTeamsBotSender_DisabledInstanceSendsNothing pins that turning the
// feature off stops OUTBOUND traffic too, not just the inbound endpoint.
// Without this gate a "disabled" instance would keep posting incident cards
// into customers' Teams channels.
func TestMSTeamsBotSender_DisabledInstanceSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	jctx := botJCtx(&mockDBService{})
	jctx.AppConfig.MSTeams.Enabled = false

	err := fake.sender("bot-disabled").Send(
		context.Background(), jctx, fake.payload(t, eventTypeIncidentCreated))
	r.ErrorIs(err, ErrMSTeamsBotDisabled)
	r.Empty(fake.recorded(), "a disabled instance must not reach the Bot Connector")
}

// TestMSTeamsBotSender_RejectsUncapturedOverride is the send-time authorization
// control for destination selection.
//
// A Teams conversation id is discoverable ("Get link to channel" URLs contain
// it), so a stored selection naming a conversation this bot was never added to
// must not be honored — otherwise the shared SaaS bot credential would post one
// organization's incident cards into another tenant's channel. The write paths
// reject this too; this guard covers rows that predate the validation or were
// written straight to the database.
func TestMSTeamsBotSender_RejectsUncapturedOverride(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentCreated)
	override := models.JSONMap{msTeamsBotConversationOverrideKey: "19:someone-elses-channel"}
	payload.CheckConnectionSettings = &override

	err := fake.sender("bot-badoverride").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.ErrorIs(err, ErrMSTeamsBotUnknownDestination)
	r.Empty(fake.recorded(), "nothing may be sent to an uncaptured conversation")
}

// TestMSTeamsBotSender_RejectsUncapturedDefaultChannel covers the same rule
// for the connection-level default rather than the per-check override.
func TestMSTeamsBotSender_RejectsUncapturedDefaultChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentCreated)
	payload.Integration.Settings["channel_id"] = "19:never-joined"

	err := fake.sender("bot-baddefault").Send(context.Background(), botJCtx(&mockDBService{}), payload)
	r.ErrorIs(err, ErrMSTeamsBotUnknownDestination)
	r.Empty(fake.recorded())
}

// TestMSTeamsBotSender_CommentRepliesWithoutTouchingCard is the correctness
// point for comments on a stateful sender: the incident card shows LIVE
// incident state, so a comment must be a reply under it and must never rewrite
// it (which would erase "is DOWN" from the channel) or claim its state entry.
func TestMSTeamsBotSender_CommentRepliesWithoutTouchingCard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)
	db := fake.storedCardDB()

	payload := fake.payload(t, eventTypeIncidentComment)
	payload.Comment = &CommentInfo{Text: "central DNS looks down", AuthorName: "Ada", Source: "web"}

	r.NoError(fake.sender("bot-comment").Send(context.Background(), botJCtx(db), payload))

	calls := fake.recorded()
	r.Len(calls, 1, "a comment must produce exactly one reply, and no card update")
	r.Equal(http.MethodPost, calls[0].Method)
	r.Equal("existing-activity", calls[0].Activity.ReplyToID)
	r.Contains(calls[0].Activity.Text, "central DNS looks down")
	r.Contains(calls[0].Activity.Text, "Ada")

	r.Empty(db.setStateCalls, "a comment must not claim the incident's card reference")
}

// TestMSTeamsBotSender_CommentWithoutCardIsSkipped is the orphan guard: a
// channel that never heard the incident open gets no context-free comment.
func TestMSTeamsBotSender_CommentWithoutCardIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newBotFake(t)

	payload := fake.payload(t, eventTypeIncidentComment)
	payload.Comment = &CommentInfo{Text: "hello?", AuthorName: "Ada"}

	r.NoError(fake.sender("bot-comment-orphan").Send(
		context.Background(), botJCtx(&mockDBService{}), payload))
	r.Empty(fake.recorded())
}
