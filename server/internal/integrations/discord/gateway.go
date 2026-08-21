package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/config"
)

// Gateway opcodes (Discord Gateway v10).
const (
	opDispatch            = 0
	opHeartbeat           = 1
	opIdentify            = 2
	opResume              = 6
	opReconnect           = 7
	opInvalidSession      = 9
	opHello               = 10
	opHeartbeatACK        = 11
	opPresenceUpdate      = 3
	opRequestGuildMembers = 8
)

// Gateway intents.
//
// MESSAGE_CONTENT is PRIVILEGED. Without it Discord delivers message events
// with an empty `content`, so inbound comment ingestion and mention commands
// silently do nothing — the failure mode is a bot that connects, looks healthy,
// and ignores everything anyone types at it. Discord grants it freely below 100
// guilds and gates it behind review above that; see wiki/discord/README.md.
const (
	intentGuilds         = 1 << 0
	intentGuildMessages  = 1 << 9
	intentMessageContent = 1 << 15

	gatewayIntents = intentGuilds | intentGuildMessages | intentMessageContent
)

// DefaultGatewayURL is Discord's public Gateway entry point.
const DefaultGatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"

// gatewayReconnectBackoff is how long the supervisor waits before re-dialing.
//
//nolint:gochecknoglobals // overridden by tests to keep the reconnect loop fast.
var gatewayReconnectBackoff = 5 * time.Second

// Sentinel errors, kept addressable for errors.Is and status reporting.
var (
	errGatewayTokenMissing = errors.New("discord: bot token is empty")
	errGatewayNoHello      = errors.New("discord: gateway did not send Hello")
	errGatewayClosed       = errors.New("discord: gateway connection closed")
)

// GatewayStatus is the snapshot returned by the operator status endpoint.
// It never contains the bot token.
type GatewayStatus struct {
	Enabled         bool       `json:"enabled"`
	Connected       bool       `json:"connected"`
	LastConnectedAt *time.Time `json:"lastConnectedAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	GuildCount      int        `json:"guildCount"`
	// BotUserID is the bot's own user id as reported by the last READY.
	BotUserID string `json:"botUserId,omitempty"`
}

// gatewayConn is the subset of a WebSocket connection the supervisor uses.
// An interface so tests can drive the protocol without a network.
type gatewayConn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
}

// wsConn adapts a *websocket.Conn to gatewayConn.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsConn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := w.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading gateway frame: %w", err)
	}

	return data, nil
}

// Write serializes writes: the heartbeat goroutine and the dispatch loop can
// both send, and a WebSocket connection allows only one writer at a time.
func (w *wsConn) Write(ctx context.Context, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("writing gateway frame: %w", err)
	}

	return nil
}

func (w *wsConn) Close() error {
	return w.conn.Close(websocket.StatusNormalClosure, "")
}

// GatewaySupervisor holds a long-lived Discord Gateway (WebSocket) connection
// and routes inbound guild messages into the transport-agnostic command and
// comment paths.
//
// It is the Discord counterpart of slack.SlackSocketSupervisor, and exists for
// the same reason: Discord has no HTTP event subscription for ordinary
// messages. Buttons and slash commands arrive over the HTTP interactions
// endpoint; everything a human TYPES — a thread reply that should become an
// incident comment, an `@SolidPing checks list` — only ever arrives here.
type GatewaySupervisor struct {
	svc *Service
	cfg *config.Config
	log *slog.Logger

	// dial builds a connection. Tests override it to point at an httptest
	// WebSocket server.
	dial func(ctx context.Context, url string) (gatewayConn, error)

	mu         sync.RWMutex
	connected  bool
	lastError  string
	lastConn   *time.Time
	guildCount int
	botUserID  string

	// session state for RESUME.
	sessionID        string
	resumeGatewayURL string
	lastSequence     *int
}

// NewGatewaySupervisor constructs a supervisor wired to svc and cfg. It does
// not auto-start — call Run from a goroutine tracked by the caller.
func NewGatewaySupervisor(svc *Service, cfg *config.Config, log *slog.Logger) *GatewaySupervisor {
	if log == nil {
		log = slog.Default()
	}

	sup := &GatewaySupervisor{
		svc: svc,
		cfg: cfg,
		log: log.With("component", "discord.GatewaySupervisor"),
	}
	sup.dial = defaultGatewayDial

	return sup
}

// defaultGatewayDial opens the real Discord Gateway connection.
func defaultGatewayDial(ctx context.Context, url string) (gatewayConn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dialing discord gateway: %w", err)
	}

	// Discord frames are small, but a READY for a bot in many guilds is not.
	conn.SetReadLimit(1 << 22)

	return &wsConn{conn: conn}, nil
}

// GetStatus returns a snapshot. Safe to call concurrently with Run.
func (g *GatewaySupervisor) GetStatus() GatewayStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()

	status := GatewayStatus{
		Enabled:    g.cfg != nil && g.cfg.Discord.Enabled && g.cfg.Discord.GatewayEnabled,
		Connected:  g.connected,
		LastError:  g.lastError,
		GuildCount: g.guildCount,
		BotUserID:  g.botUserID,
	}

	if g.lastConn != nil {
		conn := *g.lastConn
		status.LastConnectedAt = &conn
	}

	return status
}

// Run is the supervisor goroutine. Blocks until ctx is canceled.
func (g *GatewaySupervisor) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		token := ""
		if g.cfg != nil {
			token = g.cfg.Discord.BotToken
		}

		if token == "" {
			g.recordError(errGatewayTokenMissing)
			g.log.WarnContext(ctx, "Discord Gateway disabled: bot token is empty")

			if !sleepOrDone(ctx, gatewayReconnectBackoff) {
				return ctx.Err()
			}

			continue
		}

		if err := g.runOnce(ctx, token); err != nil && !errors.Is(err, context.Canceled) {
			g.recordError(err)
			g.log.WarnContext(ctx, "Discord Gateway cycle ended", "error", err)
		}

		g.setConnected(false)

		if !sleepOrDone(ctx, gatewayReconnectBackoff) {
			return ctx.Err()
		}
	}
}

// runOnce performs one full connect → identify/resume → dispatch cycle.
func (g *GatewaySupervisor) runOnce(ctx context.Context, token string) error {
	cycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := g.dial(cycleCtx, g.dialURL())
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	interval, err := g.readHello(cycleCtx, conn)
	if err != nil {
		return err
	}

	if err := g.handshake(cycleCtx, conn, token); err != nil {
		return err
	}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()
		g.heartbeatLoop(cycleCtx, conn, interval)
	}()

	err = g.readLoop(cycleCtx, conn, token)

	cancel()
	wg.Wait()

	return err
}

// dialURL prefers the resume URL Discord handed us on the last READY.
func (g *GatewaySupervisor) dialURL() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.sessionID != "" && g.resumeGatewayURL != "" {
		return g.resumeGatewayURL + "/?v=10&encoding=json"
	}

	if g.cfg != nil && g.cfg.Discord.RedirectURL != "" {
		// RedirectURL is unrelated to the gateway; never used here. Kept out of
		// the way deliberately so a misconfiguration cannot redirect the socket.
		_ = g.cfg.Discord.RedirectURL
	}

	return DefaultGatewayURL
}

// helloData is the payload of op 10.
//
//nolint:tagliatelle // Discord API uses snake_case
type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// readHello reads the mandatory Hello frame and returns the heartbeat interval.
func (g *GatewaySupervisor) readHello(ctx context.Context, conn gatewayConn) (time.Duration, error) {
	raw, err := conn.Read(ctx)
	if err != nil {
		return 0, err
	}

	var payload gatewayPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, fmt.Errorf("decoding gateway hello: %w", err)
	}

	if payload.Op != opHello {
		return 0, errGatewayNoHello
	}

	var hello helloData
	if err := json.Unmarshal(payload.D, &hello); err != nil || hello.HeartbeatInterval <= 0 {
		return 0, errGatewayNoHello
	}

	return time.Duration(hello.HeartbeatInterval) * time.Millisecond, nil
}

// handshake sends RESUME when a session is in hand, IDENTIFY otherwise.
func (g *GatewaySupervisor) handshake(ctx context.Context, conn gatewayConn, token string) error {
	g.mu.RLock()
	sessionID, sequence := g.sessionID, g.lastSequence
	g.mu.RUnlock()

	if sessionID != "" {
		seq := 0
		if sequence != nil {
			seq = *sequence
		}

		return g.send(ctx, conn, opResume, resumePayload{
			Token: token, SessionID: sessionID, Seq: seq,
		})
	}

	return g.send(ctx, conn, opIdentify, identifyPayload{
		Token:   token,
		Intents: gatewayIntents,
		Properties: identifyProperties{
			OS: "linux", Browser: "solidping", Device: "solidping",
		},
	})
}

// gatewayPayload is the Gateway frame envelope.
type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int            `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type identifyPayload struct {
	Token      string             `json:"token"`
	Intents    int                `json:"intents"`
	Properties identifyProperties `json:"properties"`
}

type identifyProperties struct {
	OS      string `json:"os"`
	Browser string `json:"browser"`
	Device  string `json:"device"`
}

//nolint:tagliatelle // Discord API uses snake_case
type resumePayload struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
}

// send marshals and writes one frame.
func (g *GatewaySupervisor) send(ctx context.Context, conn gatewayConn, op int, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling gateway payload: %w", err)
	}

	frame, err := json.Marshal(gatewayPayload{Op: op, D: body})
	if err != nil {
		return fmt.Errorf("marshalling gateway frame: %w", err)
	}

	return conn.Write(ctx, frame)
}

// heartbeatLoop sends op 1 on Discord's cadence. The first beat is jittered, as
// the protocol requires, so a fleet of pods reconnecting together does not
// heartbeat in lockstep.
func (g *GatewaySupervisor) heartbeatLoop(ctx context.Context, conn gatewayConn, interval time.Duration) {
	jitter := time.Duration(rand.Int63n(int64(interval))) //nolint:gosec // jitter, not a secret

	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	for {
		if err := g.sendHeartbeat(ctx, conn); err != nil {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (g *GatewaySupervisor) sendHeartbeat(ctx context.Context, conn gatewayConn) error {
	g.mu.RLock()
	sequence := g.lastSequence
	g.mu.RUnlock()

	frame, err := json.Marshal(map[string]any{"op": opHeartbeat, "d": sequence})
	if err != nil {
		return fmt.Errorf("marshalling heartbeat: %w", err)
	}

	return conn.Write(ctx, frame)
}

// readLoop pumps frames until the connection or context ends.
func (g *GatewaySupervisor) readLoop(ctx context.Context, conn gatewayConn, token string) error {
	for {
		raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		var payload gatewayPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			g.log.WarnContext(ctx, "Discord Gateway: undecodable frame", "error", err)

			continue
		}

		if payload.S != nil {
			g.mu.Lock()
			seq := *payload.S
			g.lastSequence = &seq
			g.mu.Unlock()
		}

		switch payload.Op {
		case opDispatch:
			g.handleDispatch(ctx, &payload)
		case opHeartbeat:
			// Discord asking for an immediate beat.
			_ = g.sendHeartbeat(ctx, conn)
		case opHeartbeatACK:
			// Nothing to do — a missing ACK surfaces as a read error anyway.
		case opReconnect:
			return errGatewayClosed
		case opInvalidSession:
			// The session cannot be resumed: drop it so the next cycle sends a
			// fresh IDENTIFY instead of looping on a rejected RESUME.
			g.clearSession()

			return errGatewayClosed
		case opIdentify, opResume, opPresenceUpdate, opRequestGuildMembers:
			// Client→server opcodes; never received.
		default:
			g.log.DebugContext(ctx, "Discord Gateway: unhandled opcode", "op", payload.Op)
		}

		_ = token
	}
}

// readyData is the subset of READY we keep.
//
//nolint:tagliatelle // Discord API uses snake_case
type readyData struct {
	SessionID        string  `json:"session_id"`
	ResumeGatewayURL string  `json:"resume_gateway_url"`
	User             *User   `json:"user"`
	Guilds           []Guild `json:"guilds"`
}

// handleDispatch routes one op-0 event.
func (g *GatewaySupervisor) handleDispatch(ctx context.Context, payload *gatewayPayload) {
	switch payload.T {
	case "READY":
		g.handleReady(ctx, payload.D)
	case "RESUMED":
		g.setConnected(true)
		g.log.InfoContext(ctx, "Discord Gateway session resumed")
	case "MESSAGE_CREATE":
		g.handleMessageCreate(ctx, payload.D)
	case "GUILD_DELETE":
		g.handleGuildDelete(ctx, payload.D)
	default:
		g.log.DebugContext(ctx, "Discord Gateway: unhandled event", "event", payload.T)
	}
}

func (g *GatewaySupervisor) handleReady(ctx context.Context, data json.RawMessage) {
	var ready readyData
	if err := json.Unmarshal(data, &ready); err != nil {
		g.log.WarnContext(ctx, "Discord Gateway: undecodable READY", "error", err)

		return
	}

	g.mu.Lock()
	g.sessionID = ready.SessionID
	g.resumeGatewayURL = ready.ResumeGatewayURL
	g.guildCount = len(ready.Guilds)

	if ready.User != nil {
		g.botUserID = ready.User.ID
	}

	g.mu.Unlock()

	g.setConnected(true)

	g.log.InfoContext(ctx, "Discord Gateway connected",
		"guild_count", len(ready.Guilds), "bot_user_id", g.BotUserID())
}

// BotUserID returns the bot's own user id from the last READY.
func (g *GatewaySupervisor) BotUserID() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.botUserID
}

// guildDeleteData is the payload of GUILD_DELETE.
type guildDeleteData struct {
	ID          string `json:"id"`
	Unavailable bool   `json:"unavailable"`
}

// handleGuildDelete cleans up when the bot is removed from a guild.
//
// `unavailable: true` is an OUTAGE, not a removal — treating it as one would
// delete a customer's integration every time Discord has a bad afternoon.
func (g *GatewaySupervisor) handleGuildDelete(ctx context.Context, data json.RawMessage) {
	var event guildDeleteData
	if err := json.Unmarshal(data, &event); err != nil || event.ID == "" {
		return
	}

	if event.Unavailable {
		g.log.InfoContext(ctx, "Discord guild temporarily unavailable — keeping the integration",
			"guild_id", event.ID)

		return
	}

	if err := g.svc.HandleGuildRemoved(ctx, event.ID); err != nil {
		g.log.WarnContext(ctx, "Failed to clean up after Discord guild removal",
			"guild_id", event.ID, "error", err)
	}
}

func (g *GatewaySupervisor) setConnected(connected bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.connected = connected

	if connected {
		now := time.Now()
		g.lastConn = &now
		g.lastError = ""
	}
}

func (g *GatewaySupervisor) recordError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.lastError = err.Error()
}

// clearSession drops the resume state so the next cycle re-identifies.
func (g *GatewaySupervisor) clearSession() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.sessionID = ""
	g.resumeGatewayURL = ""
	g.lastSequence = nil
}

// sleepOrDone returns false if ctx was canceled before d elapsed.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
