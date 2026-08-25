package discord

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeGatewayConn is an in-memory Gateway connection. Frames the supervisor
// writes land in `sent`; frames the test wants delivered are pushed into
// `inbound`. This drives the REAL protocol code — Hello, Identify, heartbeat,
// dispatch — without a network.
type fakeGatewayConn struct {
	inbound chan []byte
	sent    chan []byte
	closed  chan struct{}
}

func newFakeGatewayConn() *fakeGatewayConn {
	return &fakeGatewayConn{
		inbound: make(chan []byte, 16),
		sent:    make(chan []byte, 16),
		closed:  make(chan struct{}),
	}
}

func (f *fakeGatewayConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.closed:
		return nil, errGatewayClosed
	case frame := <-f.inbound:
		return frame, nil
	}
}

func (f *fakeGatewayConn) Write(_ context.Context, data []byte) error {
	select {
	case f.sent <- data:
	default:
	}

	return nil
}

func (f *fakeGatewayConn) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}

	return nil
}

// push delivers one op-0 dispatch frame to the supervisor.
func (f *fakeGatewayConn) push(t *testing.T, eventName string, seq int, data any) {
	t.Helper()

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	frame, err := json.Marshal(gatewayPayload{Op: opDispatch, T: eventName, S: &seq, D: raw})
	require.NoError(t, err)

	f.inbound <- frame
}

// nextSent waits for the next frame the supervisor wrote.
func (f *fakeGatewayConn) nextSent(t *testing.T) gatewayPayload {
	t.Helper()

	select {
	case raw := <-f.sent:
		var payload gatewayPayload
		require.NoError(t, json.Unmarshal(raw, &payload))

		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a gateway frame")

		return gatewayPayload{}
	}
}

// gatewayHarness wires a supervisor to a fake connection and an installed guild.
type gatewayHarness struct {
	sup   *GatewaySupervisor
	conn  *fakeGatewayConn
	svc   *Service
	inc   *fakeIncidents
	org   *models.Organization
	ctx   context.Context //nolint:containedctx // test harness lifetime
	stop  context.CancelFunc
	guild string
	fake  *fakeDiscord

	mu sync.Mutex
	// conns holds every connection handed to the supervisor, oldest first, and
	// dialURLs the URL it asked for each time — which is how the reconnect test
	// sees that the second dial targets the RESUME url Discord handed us.
	conns    []*fakeGatewayConn
	dialURLs []string
}

// dialed returns the nth connection handed to the supervisor, waiting for it.
func (h *gatewayHarness) dialed(t *testing.T, index int) *fakeGatewayConn {
	t.Helper()

	var conn *fakeGatewayConn

	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()

		if len(h.conns) <= index {
			return false
		}

		conn = h.conns[index]

		return true
	}, 3*time.Second, 10*time.Millisecond, "supervisor never dialed connection #%d", index)

	return conn
}

// dialedURL returns the nth URL the supervisor dialed.
func (h *gatewayHarness) dialedURL(index int) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.dialURLs) <= index {
		return ""
	}

	return h.dialURLs[index]
}

func newGatewayHarness(t *testing.T, ingestion string) *gatewayHarness {
	t.Helper()

	ctx, svc, fake := setupDiscordService(t)

	incidents := &fakeIncidents{}
	svc.incidentsService = incidents

	org := models.NewOrganization("acme-gw", "acme")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	result, err := svc.HandleOAuthCallback(ctx, "fake-code", installState(ctx, t, svc, org.Slug))
	require.NoError(t, err)

	// Set the guild's comment-ingestion mode.
	conn, err := svc.db.GetChannel(ctx, result.ConnectionUID)
	require.NoError(t, err)

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	require.NoError(t, err)

	settings.ChannelID = "C-ALERTS"
	settings.CommentIngestion = ingestion

	settingsMap, err := settings.ToJSONMap()
	require.NoError(t, err)
	require.NoError(t, svc.db.UpdateChannel(ctx, result.ConnectionUID,
		&models.IntegrationUpdate{Settings: &settingsMap}))

	cfg := &config.Config{
		Discord: config.DiscordOAuthConfig{
			Enabled: true, GatewayEnabled: true, BotToken: discordTestBotTokenGW,
		},
	}

	sup := NewGatewaySupervisor(svc, cfg, nil)
	// Per-supervisor, so shortening it cannot race another parallel test.
	sup.reconnectDelay = 20 * time.Millisecond

	runCtx, stop := context.WithCancel(ctx)

	harness := &gatewayHarness{
		sup: sup, svc: svc, inc: incidents, org: org,
		ctx: runCtx, stop: stop, guild: fake.guild.ID, fake: fake,
	}

	// A FRESH connection per dial, mirroring reality: a reconnect gets a new
	// socket, which is the only way the RESUME handshake can be observed.
	sup.dial = func(_ context.Context, url string) (gatewayConn, error) {
		conn := newFakeGatewayConn()

		harness.mu.Lock()
		harness.conns = append(harness.conns, conn)
		harness.dialURLs = append(harness.dialURLs, url)
		harness.mu.Unlock()

		return conn, nil
	}

	return harness
}

// discordTestBotTokenGW is an obviously fake token for the Gateway tests.
const discordTestBotTokenGW = "test-bot-token"

// botUserIDGW is the bot's own id. It is numeric because a real Discord
// snowflake is, and the mention parser matches `<@\d+>` accordingly — a
// non-numeric fixture id would make every mention test pass for the wrong
// reason.
const botUserIDGW = "999888777000111222"

// start runs the supervisor, completes the Hello/Identify/READY handshake, and
// returns once the supervisor reports itself connected.
func (h *gatewayHarness) start(t *testing.T) {
	t.Helper()

	go func() { _ = h.sup.Run(h.ctx) }()

	t.Cleanup(func() {
		h.stop()

		h.mu.Lock()
		defer h.mu.Unlock()

		for _, conn := range h.conns {
			_ = conn.Close()
		}
	})

	h.conn = h.dialed(t, 0)
	h.sendHello(t, h.conn)

	// The supervisor must IDENTIFY, with the privileged MESSAGE_CONTENT intent.
	identify := h.conn.nextSent(t)
	require.Equal(t, opIdentify, identify.Op)

	var payload identifyPayload
	require.NoError(t, json.Unmarshal(identify.D, &payload))
	require.Equal(t, discordTestBotTokenGW, payload.Token)
	require.NotZero(t, payload.Intents&intentMessageContent,
		"without MESSAGE_CONTENT every inbound message arrives with empty content")
	require.NotZero(t, payload.Intents&intentGuildMessages)

	h.sendReady(t, h.conn, 1)

	require.Eventually(t, func() bool { return h.sup.GetStatus().Connected },
		2*time.Second, 10*time.Millisecond)
}

// sendHello delivers the mandatory op-10 frame with a heartbeat interval long
// enough that no beat interferes with the assertions.
func (h *gatewayHarness) sendHello(t *testing.T, conn *fakeGatewayConn) {
	t.Helper()

	raw, err := json.Marshal(helloData{HeartbeatInterval: 60_000})
	require.NoError(t, err)

	frame, err := json.Marshal(gatewayPayload{Op: opHello, D: raw})
	require.NoError(t, err)

	conn.inbound <- frame
}

// sendReady delivers the READY that establishes the resumable session.
func (h *gatewayHarness) sendReady(t *testing.T, conn *fakeGatewayConn, seq int) {
	t.Helper()

	conn.push(t, "READY", seq, readyData{
		SessionID:        "sess-1",
		ResumeGatewayURL: "wss://resume.example",
		User:             &User{ID: botUserIDGW, Username: "solidping", Bot: true},
		Guilds:           []Guild{{ID: h.guild, Name: "acme"}},
	})
}

// trackThread writes the reverse mapping the sender would write, and returns
// the incident it points at.
func (h *gatewayHarness) trackThread(t *testing.T, threadID string) *models.Incident {
	t.Helper()

	checkUID := seedCheck(h.ctx, t, h.svc, h.org.UID, "acme-api-"+threadID)

	incident := models.NewIncident(h.org.UID, checkUID, time.Now(), "acme api is down")
	require.NoError(t, h.svc.db.CreateIncident(h.ctx, incident))

	require.NoError(t, h.svc.db.SetStateEntry(h.ctx, nil,
		ReverseThreadStateKey(h.guild, threadID),
		&models.JSONMap{ThreadIncidentUIDKey: incident.UID, ThreadOrgUIDKey: h.org.UID}, nil))

	return incident
}

// TestGateway_HandshakeAndStatus pins the connect path: Hello → Identify with
// the privileged intent → READY → connected status.
func TestGateway_HandshakeAndStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	status := h.sup.GetStatus()
	r.True(status.Enabled)
	r.True(status.Connected)
	r.Equal(1, status.GuildCount)
	r.Equal(botUserIDGW, status.BotUserID)
	r.Empty(status.LastError)

	// The status snapshot must never leak the bot token.
	encoded, err := json.Marshal(status)
	r.NoError(err)
	r.NotContains(string(encoded), discordTestBotTokenGW)
}

// TestGateway_IngestsThreadReplyAsComment is the comment-ingestion path
// resolved question 1 put in scope: a human reply inside a tracked incident
// thread becomes an incident comment.
func TestGateway_IngestsThreadReplyAsComment(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	incident := h.trackThread(t, "T-1")

	h.conn.push(t, "MESSAGE_CREATE", 2, GatewayMessage{
		ID: "M-100", ChannelID: "T-1", GuildID: h.guild,
		Content: "restarting the pod",
		Author:  &User{ID: "U-ALICE", Username: "alice"},
		Type:    messageTypeDefault,
	})

	r.Eventually(func() bool { return h.inc.commentIncident == incident.UID },
		2*time.Second, 10*time.Millisecond)
	r.Equal("restarting the pod", h.inc.commentText)
	r.Equal(h.guild, h.inc.commentGuild)
}

// TestGateway_ExplicitModeDoesNotIngest is the fail-closed half: triage chatter
// must not become permanent, fanned-out incident-timeline content unless the
// guild opted in.
func TestGateway_ExplicitModeDoesNotIngest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionExplicit)
	h.start(t)

	h.trackThread(t, "T-2")

	h.conn.push(t, "MESSAGE_CREATE", 2, GatewayMessage{
		ID: "M-200", ChannelID: "T-2", GuildID: h.guild,
		Content: "lunch?",
		Author:  &User{ID: "U-ALICE", Username: "alice"},
		Type:    messageTypeDefault,
	})

	// Give the supervisor a chance to (not) act, then assert nothing happened.
	time.Sleep(200 * time.Millisecond)
	r.Empty(h.inc.commentIncident)
}

// TestGateway_IgnoresUntrackedAndNonHumanMessages: everything that is not a
// human reply in a tracked thread must be silently ignored.
func TestGateway_IgnoresUntrackedAndNonHumanMessages(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	h.trackThread(t, "T-3")

	cases := []GatewayMessage{
		// A reply in a thread we do not track.
		{
			ID: "M-1", ChannelID: "T-UNTRACKED", GuildID: h.guild,
			Content: "unrelated", Author: &User{ID: "U-BOB"}, Type: messageTypeDefault,
		},
		// Our own bot post in the tracked thread.
		{
			ID: "M-2", ChannelID: "T-3", GuildID: h.guild,
			Content: "incident resolved", Author: &User{ID: botUserIDGW, Bot: true}, Type: messageTypeDefault,
		},
		// A legacy-webhook alert.
		{
			ID: "M-3", ChannelID: "T-3", GuildID: h.guild,
			Content: "alert", Author: &User{ID: "U-HOOK"}, WebhookID: "W-1", Type: messageTypeDefault,
		},
		// A system notice (e.g. "started a thread").
		{
			ID: "M-4", ChannelID: "T-3", GuildID: h.guild,
			Content: "x", Author: &User{ID: "U-BOB"}, Type: 18,
		},
		// A DM (no guild).
		{
			ID: "M-5", ChannelID: "T-3", Content: "hi",
			Author: &User{ID: "U-BOB"}, Type: messageTypeDefault,
		},
	}

	for i := range cases {
		h.conn.push(t, "MESSAGE_CREATE", 10+i, cases[i])
	}

	time.Sleep(300 * time.Millisecond)
	r.Empty(h.inc.commentIncident, "only a human reply in a tracked thread may become a comment")
}

// TestGateway_DedupesRedeliveredMessage: a RESUME can replay events, and an
// incident timeline must not grow a duplicate comment because of it.
func TestGateway_DedupesRedeliveredMessage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	incident := h.trackThread(t, "T-4")

	msg := GatewayMessage{
		ID: "M-400", ChannelID: "T-4", GuildID: h.guild,
		Content: "first note", Author: &User{ID: "U-ALICE"}, Type: messageTypeDefault,
	}

	h.conn.push(t, "MESSAGE_CREATE", 2, msg)
	r.Eventually(func() bool { return h.inc.commentIncident == incident.UID },
		2*time.Second, 10*time.Millisecond)

	// Second delivery of the same message, with a different body so a duplicate
	// ingest would be visible.
	h.inc.commentText = ""
	msg.Content = "SHOULD NOT BE INGESTED"
	h.conn.push(t, "MESSAGE_CREATE", 3, msg)

	time.Sleep(300 * time.Millisecond)
	r.Empty(h.inc.commentText, "a redelivered message must not be ingested twice")
}

// TestGateway_MentionCommandIsAnswered proves the Gateway and the HTTP
// interactions endpoint share one command implementation.
func TestGateway_MentionCommandIsAnswered(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	h.conn.push(t, "MESSAGE_CREATE", 2, GatewayMessage{
		ID: "M-500", ChannelID: "C-ALERTS", GuildID: h.guild,
		Content: "<@" + botUserIDGW + "> help",
		Author:  &User{ID: "U-ALICE", Username: "alice"},
		Type:    messageTypeDefault,
	})

	// The reply goes out through the REST client, which the fake Discord serves.
	r.Eventually(func() bool {
		for _, msg := range h.fake.messages() {
			if msg.ChannelID == "C-ALERTS" {
				content, _ := msg.Body["content"].(string)
				if strings.Contains(content, "/solidping checks list") {
					return true
				}
			}
		}

		return false
	}, 2*time.Second, 20*time.Millisecond, "a mention command must be answered in the channel")

	// A mention is a command, never a comment.
	r.Empty(h.inc.commentIncident)
}

// TestGateway_MentionCommandInsideThreadCarriesContext proves the Gateway hands
// the thread context to DispatchCommand, so `@SolidPing comment …` typed in an
// incident thread needs no `#N`.
func TestGateway_MentionCommandInsideThreadCarriesContext(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionExplicit)
	h.start(t)

	incident := h.trackThread(t, "T-6")

	h.conn.push(t, "MESSAGE_CREATE", 2, GatewayMessage{
		ID: "M-600", ChannelID: "T-6", GuildID: h.guild,
		Content: "<@" + botUserIDGW + "> comment rolled back the deploy",
		Author:  &User{ID: "U-ALICE", Username: "alice"},
		Type:    messageTypeDefault,
	})

	r.Eventually(func() bool { return h.inc.commentIncident == incident.UID },
		2*time.Second, 20*time.Millisecond)
	r.Equal("rolled back the deploy", h.inc.commentText)
}

// TestGateway_GuildDeleteUnavailableKeepsIntegration: an outage is not a
// removal. Treating it as one would delete a customer's integration every time
// Discord has a bad afternoon.
func TestGateway_GuildDeleteUnavailableKeepsIntegration(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	h.conn.push(t, "GUILD_DELETE", 2, guildDeleteData{ID: h.guild, Unavailable: true})
	time.Sleep(200 * time.Millisecond)

	conn, err := h.svc.GetConnectionByGuildID(h.ctx, h.guild)
	r.NoError(err)
	r.NotNil(conn)

	// A real removal does clean up.
	h.conn.push(t, "GUILD_DELETE", 3, guildDeleteData{ID: h.guild, Unavailable: false})

	r.Eventually(func() bool {
		_, err := h.svc.GetConnectionByGuildID(h.ctx, h.guild)

		return err != nil
	}, 2*time.Second, 10*time.Millisecond)
}

// TestGateway_InvalidSessionClearsResumeState: a session Discord refuses to
// resume must be dropped, or the supervisor loops forever on a rejected RESUME.
func TestGateway_InvalidSessionClearsResumeState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	r.NotEmpty(h.sup.sessionID)

	frame, err := json.Marshal(gatewayPayload{Op: opInvalidSession})
	r.NoError(err)
	h.conn.inbound <- frame

	r.Eventually(func() bool {
		h.sup.mu.RLock()
		defer h.sup.mu.RUnlock()

		return h.sup.sessionID == ""
	}, 2*time.Second, 10*time.Millisecond)
}

// TestGateway_ReconnectResumesTheSession is what the supervisor exists for:
// when the socket drops, the next cycle must RESUME the existing session
// against the resume URL Discord handed us — not IDENTIFY afresh.
//
// The difference is not cosmetic. A RESUME replays the events missed while the
// socket was down, so a thread reply typed during a blip still becomes an
// incident comment; a fresh IDENTIFY silently drops them and starts a new
// session. An agent that "reconnects" but never resumes loses exactly the
// continuity this component is for, and does it invisibly.
func TestGateway_ReconnectResumesTheSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	// Advance the sequence so the RESUME has something non-zero to replay from.
	h.conn.push(t, "MESSAGE_CREATE", 7, GatewayMessage{
		ID: "M-SEQ", ChannelID: "C-UNTRACKED", GuildID: h.guild,
		Content: "chatter", Author: &User{ID: "U-ALICE"}, Type: messageTypeDefault,
	})

	r.Eventually(func() bool {
		h.sup.mu.RLock()
		defer h.sup.mu.RUnlock()

		return h.sup.lastSequence != nil && *h.sup.lastSequence == 7
	}, 2*time.Second, 10*time.Millisecond, "the supervisor must track the dispatch sequence")

	// Drop the socket the way a network blip does.
	r.NoError(h.conn.Close())

	// The next cycle dials the RESUME url Discord gave us in READY, not the
	// default gateway entry point.
	second := h.dialed(t, 1)
	r.Contains(h.dialedURL(1), "wss://resume.example",
		"a resumable session must re-dial the resume_gateway_url from READY")

	h.sendHello(t, second)

	frame := second.nextSent(t)
	r.Equal(opResume, frame.Op, "a live session must RESUME, never re-IDENTIFY")

	var payload resumePayload
	r.NoError(json.Unmarshal(frame.D, &payload))
	r.Equal(discordTestBotTokenGW, payload.Token)
	r.Equal("sess-1", payload.SessionID)
	r.Equal(7, payload.Seq, "resuming from the wrong sequence silently loses events")

	// And the session comes back up on RESUMED, without a second READY.
	resumedFrame, err := json.Marshal(gatewayPayload{Op: opDispatch, T: "RESUMED"})
	r.NoError(err)
	second.inbound <- resumedFrame

	r.Eventually(func() bool { return h.sup.GetStatus().Connected },
		2*time.Second, 10*time.Millisecond)
}

// TestGateway_InvalidSessionReIdentifies is the other half of the reconnect
// contract: a session Discord refuses to resume must be dropped, and the NEXT
// cycle must send a fresh IDENTIFY. Without the drop the supervisor would loop
// forever re-sending a RESUME that is rejected every time.
func TestGateway_InvalidSessionReIdentifies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newGatewayHarness(t, models.DiscordCommentIngestionAll)
	h.start(t)

	frame, err := json.Marshal(gatewayPayload{Op: opInvalidSession})
	r.NoError(err)
	h.conn.inbound <- frame

	second := h.dialed(t, 1)
	r.Equal(DefaultGatewayURL, h.dialedURL(1),
		"a dropped session must fall back to the default gateway entry point")

	h.sendHello(t, second)

	next := second.nextSent(t)
	r.Equal(opIdentify, next.Op, "an invalidated session must re-IDENTIFY, not RESUME again")
}
