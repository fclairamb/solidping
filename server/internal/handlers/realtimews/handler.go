// Package realtimews serves the org-scoped live hint WebSocket
// (GET /api/v1/orgs/:org/events/ws), replacing the v1 SSE stream
// (handlers/realtimestream). The socket carries data-free hints scoped to
// whatever the client has subscribed to — a connection with zero
// subscriptions receives nothing. See internal/realtime for the hub/publisher
// fan-out and the spec at
// specs/todos/2026-07-02-02-realtime-websocket-per-entity-subscriptions.md
// for the full protocol.
package realtimews

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// Close codes. Browsers cannot see HTTP status at WebSocket-upgrade time
// (only a generic 1006 on failure), so terminal conditions are signaled by
// accepting the socket and closing with one of these application codes.
const (
	CloseAuthFailed        websocket.StatusCode = 4401 // missing/invalid/timed-out auth, or token expiry mid-connection
	CloseForbidden         websocket.StatusCode = 4403 // authenticated but not an org member
	CloseDisabled          websocket.StatusCode = 4404 // SP_REALTIME_ENABLED=false
	CloseServiceRestarting websocket.StatusCode = 1012 // server shutdown / hub closed
)

// Message-type metric labels.
const (
	msgLabelAuth        = "auth"
	msgLabelSubscribe   = "subscribe"
	msgLabelUnsubscribe = "unsubscribe"
	msgLabelUnknown     = "unknown"
)

// pingPongGrace pads the ping deadline beyond half the ping interval so a
// momentarily slow (not dead) peer isn't flagged as unresponsive.
const pingPongGrace = 5 * time.Second

// Handler upgrades and serves the realtime WebSocket for authenticated org
// members. Unlike v1 (registered behind RequireAuth/RequireOrgAccess), this
// route is registered unconditionally and performs its own auth in-handler,
// because browsers cannot present credentials at WebSocket-upgrade time.
type Handler struct {
	hub         *realtime.Hub
	authService *auth.Service
	dbService   db.Service
	cfg         *config.Config
	logger      *slog.Logger

	pingInterval time.Duration
	authGrace    time.Duration
}

// NewHandler creates the realtime WebSocket handler on the process-wide hub.
func NewHandler(
	hub *realtime.Hub, authService *auth.Service, dbService db.Service, cfg *config.Config,
) *Handler {
	pingInterval := cfg.Realtime.PingInterval
	if pingInterval <= 0 {
		pingInterval = 25 * time.Second
	}
	authGrace := cfg.Realtime.AuthGrace
	if authGrace <= 0 {
		authGrace = 5 * time.Second
	}

	return &Handler{
		hub:          hub,
		authService:  authService,
		dbService:    dbService,
		cfg:          cfg,
		logger:       slog.Default().With("component", "realtimews"),
		pingInterval: pingInterval,
		authGrace:    authGrace,
	}
}

// Serve handles GET /api/v1/orgs/:org/events/ws. It always accepts the
// upgrade — even when the feature is disabled or auth fails — because a
// rejected upgrade is invisible to browser JS; terminal conditions are
// instead signaled with a specific close code (see the Close* constants).
func (h *Handler) Serve(writer http.ResponseWriter, req bunrouter.Request) error {
	conn, err := websocket.Accept(writer, req.Request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil // the client never got a socket; nothing more to do
	}

	ctx := req.Context()

	if !h.cfg.Realtime.Enabled {
		_ = conn.Close(CloseDisabled, "realtime updates are disabled")

		return nil
	}

	orgSlug := req.Param("org")

	claims, org, closeCode, reason := h.handshake(ctx, req.Request, conn, orgSlug)
	if claims == nil {
		_ = conn.Close(closeCode, reason)

		return nil
	}

	sub, err := h.hub.Subscribe(org.UID)
	if err != nil {
		// Max-connections guard or shutdown in progress.
		_ = conn.Close(CloseServiceRestarting, "realtime hub unavailable")

		return nil
	}
	defer h.hub.Unsubscribe(sub)

	if err := writeJSON(ctx, conn, newHello()); err != nil {
		return nil // client already gone
	}

	h.serveLoop(ctx, conn, sub, org, claims)

	return nil
}

// serveLoop runs the write-loop in the background and blocks in the
// read-loop until the connection ends, then tears both down.
func (h *Handler) serveLoop(
	ctx context.Context, conn *websocket.Conn, sub *realtime.Subscriber, org *models.Organization, claims *auth.Claims,
) {
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		h.writeLoop(loopCtx, conn, sub)
	}()

	h.readLoop(loopCtx, conn, sub, org, claims)
	cancel()
	<-writeDone
}

// writeLoop drains the subscriber's dirty state and the ping ticker until
// the loop context is canceled (peer gone, auth expired, hub closed).
func (h *Handler) writeLoop(ctx context.Context, conn *websocket.Conn, sub *realtime.Subscriber) {
	ping := time.NewTicker(h.pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			// A dead peer fails to pong within the deadline: coder/websocket
			// closes the connection itself on any Read/Write/Ping error
			// (including context expiry), so there is no live socket left to
			// carry an explicit close code by the time Ping returns — the
			// explicit Close here is a best-effort formality for the rare
			// case the connection is still writable.
			pingCtx, cancel := context.WithTimeout(ctx, h.pingInterval/2+pingPongGrace)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				_ = conn.Close(CloseServiceRestarting, "ping timeout")

				return
			}
		case <-sub.Signal():
			updates, resync, closed := sub.Take()
			if closed {
				_ = conn.Close(CloseServiceRestarting, "realtime hub shutting down")

				return
			}
			if resync {
				if err := writeJSON(ctx, conn, newResync()); err != nil {
					return
				}
			}
			for i := range updates {
				if err := writeJSON(ctx, conn, newUpdate(updates[i])); err != nil {
					return
				}
			}
		}
	}
}

// readLoop authenticates (if not already) and then processes
// subscribe/unsubscribe/auth messages until the connection ends. Runs on the
// caller's goroutine; returns when the connection closes or the token
// expires.
func (h *Handler) readLoop(
	ctx context.Context, conn *websocket.Conn, sub *realtime.Subscriber, org *models.Organization, claims *auth.Claims,
) {
	expiry := tokenExpiryTimer(claims)
	defer expiry.Stop()

	msgCh := make(chan clientMessage)
	errCh := make(chan error, 1)
	go pumpMessages(ctx, conn, msgCh, errCh)

	checkedUIDs := make(map[string]bool) // per-connection existence-check cache

	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			_ = conn.Close(CloseAuthFailed, "access token expired")

			return
		case err := <-errCh:
			if err != nil {
				h.logger.DebugContext(ctx, "realtime read loop ended", "error", err)
			}

			return
		case msg := <-msgCh:
			h.handleMessage(ctx, conn, sub, org, msg, checkedUIDs)
		}
	}
}

// handleMessage dispatches one decoded client message. Per-message failures
// reply with an `error` frame and keep the socket open (never close it).
func (h *Handler) handleMessage(
	ctx context.Context, conn *websocket.Conn, sub *realtime.Subscriber,
	org *models.Organization, msg clientMessage, checkedUIDs map[string]bool,
) {
	switch msg.Type {
	case msgTypeAuth:
		prommetrics.RealtimeMessagesReceived.WithLabelValues(msgLabelAuth).Inc()
		// Already authenticated (this handler only reaches handleMessage
		// post-handshake) — a stray auth message is harmless, ignore it.
	case msgTypeSubscribe:
		prommetrics.RealtimeMessagesReceived.WithLabelValues(msgLabelSubscribe).Inc()
		h.handleSubscribe(ctx, conn, sub, org, msg, checkedUIDs)
	case msgTypeUnsubscribe:
		prommetrics.RealtimeMessagesReceived.WithLabelValues(msgLabelUnsubscribe).Inc()
		h.handleUnsubscribe(sub, msg)
	default:
		prommetrics.RealtimeMessagesReceived.WithLabelValues(msgLabelUnknown).Inc()
		_ = writeJSON(ctx, conn, newError(
			string(base.ErrorCodeValidationError), "Unknown message type", msg.Entity, msg.UID))
	}
}

// handleSubscribe validates and registers one scope subscription.
func (h *Handler) handleSubscribe(
	ctx context.Context, conn *websocket.Conn, sub *realtime.Subscriber,
	org *models.Organization, msg clientMessage, checkedUIDs map[string]bool,
) {
	scope, ok := parseScope(msg)
	if !ok {
		_ = writeJSON(ctx, conn, newError(
			string(base.ErrorCodeValidationError), "Invalid subscription scope", msg.Entity, msg.UID))

		return
	}

	if scope.Entity == realtime.EntityCheck && !checkedUIDs[scope.UID] {
		if _, err := h.dbService.GetCheck(ctx, org.UID, scope.UID); err != nil {
			_ = writeJSON(ctx, conn, newError(
				string(base.ErrorCodeNotFound), "Check not found", msg.Entity, msg.UID))

			return
		}
		checkedUIDs[scope.UID] = true
	}

	if err := sub.AddScope(scope); err != nil {
		if errors.Is(err, realtime.ErrTooManySubscriptions) {
			_ = writeJSON(ctx, conn, newError(
				string(base.ErrorCodeConcurrencyLimited), "Subscription limit reached", msg.Entity, msg.UID))

			return
		}
		_ = writeJSON(ctx, conn, newError(
			string(base.ErrorCodeInternalError), "Failed to subscribe", msg.Entity, msg.UID))

		return
	}

	prommetrics.RealtimeSubscriptions.Set(float64(sub.ScopeCount()))
	// Duplicate subscribe is idempotent, not an error: always ack normally.
	_ = writeJSON(ctx, conn, newSubscribed(scope))
}

// handleUnsubscribe drops a scope subscription. Removing a scope that was
// never subscribed is a silent no-op — no ack is defined for unsubscribe.
func (h *Handler) handleUnsubscribe(sub *realtime.Subscriber, msg clientMessage) {
	scope, ok := parseScope(msg)
	if !ok {
		return
	}
	sub.RemoveScope(scope)
	prommetrics.RealtimeSubscriptions.Set(float64(sub.ScopeCount()))
}

// parseScope validates and converts a client message into a realtime.Scope.
// `check` requires a non-empty uid; collection entities require an empty uid.
func parseScope(msg clientMessage) (realtime.Scope, bool) {
	switch realtime.Entity(msg.Entity) {
	case realtime.EntityCheck:
		if msg.UID == "" {
			return realtime.Scope{}, false
		}

		return realtime.Scope{Entity: realtime.EntityCheck, UID: msg.UID}, true
	case realtime.EntityChecks, realtime.EntityIncidents, realtime.EntityEvents, realtime.EntityJobs:
		return realtime.Scope{Entity: realtime.Entity(msg.Entity)}, true
	default:
		return realtime.Scope{}, false
	}
}

// pumpMessages reads and decodes frames from conn until it errors or ctx is
// canceled, sending each decoded message on msgCh. A JSON decode failure is
// reported once via errCh is NOT how we handle it — malformed messages are
// non-fatal (see readLoop/handleMessage), so decode errors are logged and
// skipped, only transport-level Read errors terminate the pump.
func pumpMessages(ctx context.Context, conn *websocket.Conn, msgCh chan<- clientMessage, errCh chan<- error) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			errCh <- err

			return
		}

		var msg clientMessage
		if jsonErr := json.Unmarshal(data, &msg); jsonErr != nil {
			prommetrics.RealtimeMessagesReceived.WithLabelValues(msgLabelUnknown).Inc()
			_ = writeJSON(ctx, conn, newError(string(base.ErrorCodeValidationError), "Malformed message", "", ""))

			continue
		}

		select {
		case msgCh <- msg:
		case <-ctx.Done():
			return
		}
	}
}

// writeJSON marshals and writes one JSON text frame.
func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	return conn.Write(ctx, websocket.MessageText, data)
}

// tokenExpiryTimer returns a timer firing when the token expires, or one
// that never fires for tokens without an expiry claim (PATs).
func tokenExpiryTimer(claims *auth.Claims) *time.Timer {
	if claims != nil && claims.ExpiresAt != nil {
		until := time.Until(claims.ExpiresAt.Time)
		if until < 0 {
			until = 0
		}

		return time.NewTimer(until)
	}

	// Effectively never: stopped-by-defer timer with a very long duration.
	const never = 365 * 24 * time.Hour

	return time.NewTimer(never)
}
