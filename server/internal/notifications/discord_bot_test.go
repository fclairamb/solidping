package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	discordTestGuild   = "G-ACME"
	discordTestChannel = "C-ALERTS"
	discordTestThread  = "T-THREAD"
	discordTestMessage = "M-1"
	// Obviously fake — a real bot token never appears in a fixture.
	discordTestBotToken = "test-bot-token"
)

// discordCall is one request the fake Discord REST API received.
type discordCall struct {
	Method string
	Path   string
	Body   map[string]any
}

// discordFake stands in for Discord's REST API. A live round trip would need a
// real application and guild, so the post/edit/thread/un-archive contract is
// pinned against this fake instead.
type discordFake struct {
	server *httptest.Server

	mu    sync.Mutex
	calls []discordCall

	// threadArchived models Discord's behavior: a POST into an archived thread
	// is rejected until the archived flag is cleared. This is what makes the
	// late-follow-up test a real test rather than a call-count assertion.
	threadArchived bool
	// threadCreateFails simulates a guild that denies the thread permission.
	threadCreateFails bool
}

func newDiscordFake(t *testing.T) *discordFake {
	t.Helper()

	fake := &discordFake{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}

		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}

		fake.mu.Lock()
		fake.calls = append(fake.calls, discordCall{Method: r.Method, Path: r.URL.Path, Body: body})
		fake.mu.Unlock()

		fake.route(w, r, body)
	})

	fake.server = httptest.NewServer(handler)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *discordFake) route(w http.ResponseWriter, r *http.Request, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/threads"):
		if f.threadCreateFails {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Missing Permissions"}`))

			return
		}

		f.mu.Lock()
		f.threadArchived = false
		f.mu.Unlock()

		writeFakeJSON(w, discord.ChannelInfo{
			ID: discordTestThread, Name: "incident", Type: discord.ChannelTypePublicThread,
		})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/channels/") &&
		!strings.Contains(r.URL.Path, "/messages/"):
		// Un-archive.
		if archived, ok := body["archived"].(bool); ok && !archived {
			f.mu.Lock()
			f.threadArchived = false
			f.mu.Unlock()
		}

		writeFakeJSON(w, discord.ChannelInfo{ID: discordTestThread})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
		f.mu.Lock()
		archived := f.threadArchived && strings.HasPrefix(r.URL.Path, "/channels/"+discordTestThread+"/")
		f.mu.Unlock()

		if archived {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":50083,"message":"Thread is archived"}`))

			return
		}

		writeFakeJSON(w, discord.MessageResult{ID: discordTestMessage, ChannelID: "c"})
	case r.Method == http.MethodPatch:
		// Message edit.
		writeFakeJSON(w, discord.MessageResult{ID: discordTestMessage})
	case r.Method == http.MethodGet && r.URL.Path == "/users/@me":
		writeFakeJSON(w, discord.User{ID: "U-BOT", Username: "solidping", Bot: true})
	default:
		writeFakeJSON(w, struct{}{})
	}
}

// writeFakeJSON encodes a fake response, surfacing an encoding failure as a
// 500 instead of swallowing it — a silently empty body would show up as a
// confusing decode error three frames away in the code under test.
func writeFakeJSON(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (f *discordFake) recorded() []discordCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]discordCall, len(f.calls))
	copy(out, f.calls)

	return out
}

// find returns the first recorded call matching method and a path predicate.
func (f *discordFake) find(method string, match func(string) bool) *discordCall {
	for _, call := range f.recorded() {
		if call.Method == method && match(call.Path) {
			return &call
		}
	}

	return nil
}

// sender builds a DiscordSender pointed at this fake.
func (f *discordFake) sender() *DiscordSender {
	return &DiscordSender{
		newBotClient: func(token string) *discord.BotClient {
			return discord.NewBotClient(token).WithBaseURL(f.server.URL)
		},
	}
}

// discordJCtx builds a job context with the instance bot token configured.
func discordJCtx(db *mockDBService, token string) *jobdef.JobContext {
	return &jobdef.JobContext{
		DBService: db,
		AppConfig: &config.Config{
			Discord: config.DiscordOAuthConfig{Enabled: true, BotToken: token},
		},
	}
}

// discordBotPayload builds a bot-mode payload.
func discordBotPayload(t *testing.T, eventType string) *Payload {
	t.Helper()

	settings := &models.DiscordSettings{
		GuildID:       discordTestGuild,
		GuildName:     "acme",
		ChannelID:     discordTestChannel,
		ChannelName:   "alerts",
		MentionOnCall: true,
	}

	settingsMap, err := settings.ToJSONMap()
	require.NoError(t, err)

	name := "API health"
	resolved := time.Now()

	incident := &models.Incident{
		UID:             "inc-discord-1",
		OrganizationUID: "org-1",
		Number:          42,
		StartedAt:       time.Now().Add(-90 * time.Minute),
		FailureCount:    3,
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
			Type: models.ConnectionTypeDiscord, Settings: settingsMap,
		},
	}
}

// storedThreadDB returns a mock whose forward state entry points at an
// already-posted incident message and its thread.
func storedThreadDB(threadID string) *mockDBService {
	return &mockDBService{
		getStateEntryFunc: func(_ context.Context, _ *string, key string) (*models.StateEntry, error) {
			if !strings.HasPrefix(key, "incidents/") {
				return nil, nil
			}

			return &models.StateEntry{Value: &models.JSONMap{
				discordKeyChannelID: discordTestChannel,
				discordKeyMessageID: discordTestMessage,
				discordKeyThreadID:  threadID,
			}}, nil
		},
	}
}

// --- created ---------------------------------------------------------------

func TestDiscordSender_IncidentCreatedPostsEmbedButtonsAndThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	db := &mockDBService{}

	payload := discordBotPayload(t, eventTypeIncidentCreated)
	payload.OnCallMentions = []MentionTarget{
		{UserUID: "u1", DisplayName: "alice", ExternalID: "111"},
		{UserUID: "u2", DisplayName: "bob"},
	}

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(db, discordTestBotToken), payload))

	post := fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	})
	r.NotNil(post, "the incident message must be posted into the configured channel")

	// The embed names the incident the way every other surface does.
	embeds, ok := post.Body["embeds"].([]any)
	r.True(ok)
	r.Len(embeds, 1)

	firstEmbed, ok := embeds[0].(map[string]any)
	r.True(ok)
	r.Contains(firstEmbed["title"], "#42 · New incident for API health")

	// Acknowledge button present, carrying the incident uid.
	components, ok := post.Body["components"].([]any)
	r.True(ok)
	r.Len(components, 1)

	actionRow, ok := components[0].(map[string]any)
	r.True(ok)

	buttons, ok := actionRow["components"].([]any)
	r.True(ok)
	r.Len(buttons, 3)

	ackButton, ok := buttons[0].(map[string]any)
	r.True(ok)
	r.Equal("ack:inc-discord-1", ackButton["custom_id"])

	// Mentions: a mapped user pings, an unmapped one is named in plain text,
	// and the allow-list bounds what the message may notify.
	content, _ := post.Body["content"].(string)
	r.Contains(content, "<@111>")
	r.Contains(content, "bob")

	allowed, ok := post.Body["allowed_mentions"].(map[string]any)
	r.True(ok, "an alert must never be able to @everyone")
	r.Empty(allowed["parse"])
	r.Equal([]any{"111"}, allowed["users"])

	// A thread was opened on the message.
	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return strings.HasSuffix(p, "/messages/"+discordTestMessage+"/threads")
	}))

	// Forward + reverse mappings both written.
	r.Len(db.setStateCalls, 2)

	var forward, reverse *setStateCall

	for i := range db.setStateCalls {
		switch db.setStateCalls[i].key {
		case discordThreadStateKey("inc-discord-1"):
			forward = &db.setStateCalls[i]
		case discord.ReverseThreadStateKey(discordTestGuild, discordTestThread):
			reverse = &db.setStateCalls[i]
		}
	}

	r.NotNil(forward)
	r.Equal(discordTestThread, (*forward.value)[discordKeyThreadID])
	r.NotNil(reverse, "inbound thread replies cannot be routed without the reverse mapping")
	r.Equal("inc-discord-1", (*reverse.value)[discord.ThreadIncidentUIDKey])
	r.Equal("org-1", (*reverse.value)[discord.ThreadOrgUIDKey])
}

func TestDiscordSender_MentionsOffProducesNoContent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentCreated)
	payload.OnCallMentions = []MentionTarget{{UserUID: "u1", DisplayName: "alice", ExternalID: "111"}}

	settings := &models.DiscordSettings{
		GuildID: discordTestGuild, ChannelID: discordTestChannel, MentionOnCall: false,
	}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)
	payload.Integration.Settings = settingsMap

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), payload))

	post := fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	})
	r.NotNil(post)
	r.Empty(post.Body["content"], "mentions off must leave the message byte-identical to a mention-free one")
}

// --- resolved --------------------------------------------------------------

func TestDiscordSender_ResolvedEditsOriginalAndRepliesInThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentResolved)

	r.NoError(fake.sender().Send(
		t.Context(), discordJCtx(storedThreadDB(discordTestThread), discordTestBotToken), payload))

	edit := fake.find(http.MethodPatch, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages/"+discordTestMessage
	})
	r.NotNil(edit, "the original alert must be rewritten, not left red with a live Acknowledge button")

	embeds, ok := edit.Body["embeds"].([]any)
	r.True(ok)

	resolvedEmbed, ok := embeds[0].(map[string]any)
	r.True(ok)
	r.Contains(resolvedEmbed["title"], "Automatically resolved")

	components, ok := edit.Body["components"].([]any)
	r.True(ok)
	r.Empty(components, "a resolved card must carry no buttons")

	reply := fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestThread+"/messages"
	})
	r.NotNil(reply, "the resolve notice belongs in the incident's thread")
}

// TestDiscordSender_LateResolveReachesArchivedThread is resolved question 3's
// acceptance test. Discord auto-archives an idle thread and rejects posts into
// it; the incident whose thread has gone quiet for a week is exactly the one
// whose resolve message matters most. Without the un-archive this test fails
// with "Thread is archived".
func TestDiscordSender_LateResolveReachesArchivedThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	fake.threadArchived = true

	payload := discordBotPayload(t, eventTypeIncidentResolved)

	r.NoError(fake.sender().Send(
		t.Context(), discordJCtx(storedThreadDB(discordTestThread), discordTestBotToken), payload))

	unarchive := fake.find(http.MethodPatch, func(p string) bool {
		return p == "/channels/"+discordTestThread
	})
	r.NotNil(unarchive, "a late follow-up must un-archive the thread first")
	r.Equal(false, unarchive.Body["archived"])

	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestThread+"/messages"
	}), "the resolve message must still land in the archived thread")
}

// TestDiscordSender_FollowUpFallsBackToChannelWithoutThread covers the guild
// that denied the thread permission: noisier, never silent.
func TestDiscordSender_FollowUpFallsBackToChannelWithoutThread(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentResolved)

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(storedThreadDB(""), discordTestBotToken), payload))

	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	}))
}

func TestDiscordSender_ThreadCreationFailureStillDelivers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	fake.threadCreateFails = true

	db := &mockDBService{}
	payload := discordBotPayload(t, eventTypeIncidentCreated)

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(db, discordTestBotToken), payload))

	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	}))

	// One state write only: with no thread there is no reverse mapping to make.
	r.Len(db.setStateCalls, 1)
	r.Empty((*db.setStateCalls[0].value)[discordKeyThreadID])
}

// --- orphan guard ----------------------------------------------------------

func TestDiscordSender_FollowUpWithoutThreadIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, eventType := range []string{
		eventTypeIncidentResolved, eventTypeIncidentReopened, eventTypeIncidentComment,
	} {
		fake := newDiscordFake(t)
		payload := discordBotPayload(t, eventType)

		r.NoError(fake.sender().Send(
			t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), payload))

		r.Empty(fake.recorded(),
			"a %s with no recorded incident message must post nothing at all", eventType)
	}
}

// --- reopened --------------------------------------------------------------

func TestDiscordSender_ReopenedRestoresButtons(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentReopened)
	payload.Incident.RelapseCount = 2

	r.NoError(fake.sender().Send(
		t.Context(), discordJCtx(storedThreadDB(discordTestThread), discordTestBotToken), payload))

	edit := fake.find(http.MethodPatch, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages/"+discordTestMessage
	})
	r.NotNil(edit)

	components, ok := edit.Body["components"].([]any)
	r.True(ok)
	r.Len(components, 1, "a reopened incident is actionable again")
}

// --- configuration failures ------------------------------------------------

func TestDiscordSender_BotModeWithoutTokenFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	err := fake.sender().Send(
		t.Context(), discordJCtx(&mockDBService{}, ""), discordBotPayload(t, eventTypeIncidentCreated))
	r.ErrorIs(err, ErrDiscordBotNotConfigured)
	r.Empty(fake.recorded())
}

func TestDiscordSender_GuildWithoutChannelReportsNoDestination(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentCreated)

	settings := &models.DiscordSettings{GuildID: discordTestGuild}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)
	payload.Integration.Settings = settingsMap

	r.ErrorIs(
		fake.sender().Send(t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), payload),
		ErrDiscordNoDestination,
	)
}

// TestDiscordSender_PerCheckChannelOverride pins the override path.
func TestDiscordSender_PerCheckChannelOverride(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)

	payload := discordBotPayload(t, eventTypeIncidentCreated)
	override := models.JSONMap{discordKeyChannelID: "C-OTHER"}
	payload.CheckConnectionSettings = &override

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), payload))

	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/C-OTHER/messages"
	}))
}
