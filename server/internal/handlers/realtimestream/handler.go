// Package realtimestream serves the org-scoped live hint stream
// (GET /api/v1/orgs/:org/events/stream) over Server-Sent Events. The stream
// carries data-free hints ({kinds}) plus resync markers; the dashboard
// invalidates the matching query caches and refetches over the normal REST
// API. See internal/realtime for the hub/publisher fan-out.
package realtimestream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// ProtocolVersion is announced in the `hello` event so clients can detect
// incompatible server upgrades and force a full reconnect.
const ProtocolVersion = 1

// Handler streams realtime hints to authenticated org members.
type Handler struct {
	base.HandlerBase
	hub          *realtime.Hub
	pingInterval time.Duration
}

// NewHandler creates the stream handler on the process-wide hub.
func NewHandler(hub *realtime.Hub, pingInterval time.Duration, cfg *config.Config) *Handler {
	if pingInterval <= 0 {
		pingInterval = 25 * time.Second
	}

	return &Handler{
		HandlerBase:  base.NewHandlerBase(cfg),
		hub:          hub,
		pingInterval: pingInterval,
	}
}

// Stream handles GET /api/v1/orgs/:org/events/stream. It holds the response
// open, emitting `hello` once, then `hint` / `resync` events and comment
// pings until the client leaves, the access token expires, the hub shuts
// down, or the subscription is dropped.
//
// Registered behind RequireAuth + RequireOrgAccess: the org is taken from the
// request context (membership already verified — non-members got a 403), and
// the token expiry from the validated claims.
func (h *Handler) Stream(writer http.ResponseWriter, req bunrouter.Request) error {
	ctx := req.Context()

	org, ok := ctx.Value(base.ContextKeyOrganization).(*models.Organization)
	if !ok || org == nil {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"Organization context is required")
	}

	sub, err := h.hub.Subscribe(org.UID)
	if err != nil {
		// Max-connections guard or shutdown in progress: the dashboard falls
		// back to plain polling and retries with backoff.
		return h.WriteError(writer, http.StatusServiceUnavailable, base.ErrorCodeConcurrencyLimited,
			"Realtime stream unavailable: "+err.Error())
	}
	defer h.hub.Unsubscribe(sub)

	ctrl := http.NewResponseController(writer)

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	writer.WriteHeader(http.StatusOK)

	if err := writeEvent(writer, "hello", fmt.Sprintf(`{"protocol":%d}`, ProtocolVersion)); err != nil {
		return nil // client already gone; nothing to report
	}
	if err := ctrl.Flush(); err != nil {
		// No flush support (misconfigured middleware wrapper): a stream that
		// only arrives on close is worse than polling, so bail out early.
		return nil // stream aborted, response already committed
	}

	return h.streamLoop(req, writer, ctrl, sub)
}

// streamLoop pumps subscriber wake-ups, pings and expiry until the stream ends.
func (h *Handler) streamLoop(
	req bunrouter.Request, writer http.ResponseWriter, ctrl *http.ResponseController, sub *realtime.Subscriber,
) error {
	ctx := req.Context()

	ping := time.NewTicker(h.pingInterval)
	defer ping.Stop()

	// Close the stream when the access token expires; the client reconnects
	// with a fresh token. Hints carry no history, so a clean reconnect is
	// trivially correct. Tokens without an expiry (PATs) never fire.
	expiry := tokenExpiryTimer(req)
	defer expiry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-expiry.C:
			return nil
		case <-ping.C:
			if err := writeComment(writer); err != nil {
				return nil // client gone
			}
			if err := ctrl.Flush(); err != nil {
				return nil // client gone
			}
		case <-sub.Signal():
			kinds, resync, closed := sub.Take()
			if closed {
				return nil
			}
			if err := writePending(writer, kinds, resync); err != nil {
				return nil // client gone
			}
			if err := ctrl.Flush(); err != nil {
				return nil // client gone
			}
		}
	}
}

// tokenExpiryTimer returns a timer firing when the request's access token
// expires, or one that never fires for tokens without an expiry claim.
func tokenExpiryTimer(req bunrouter.Request) *time.Timer {
	if claims, ok := middleware.GetClaimsFromContext(req.Context()); ok &&
		claims != nil && claims.ExpiresAt != nil {
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

// writePending emits the drained subscriber state: a resync marker first (the
// client performs one full invalidation), then the dirty kinds if any.
func writePending(writer http.ResponseWriter, kinds []realtime.Kind, resync bool) error {
	if resync {
		if err := writeEvent(writer, "resync", "{}"); err != nil {
			return err
		}
	}
	if len(kinds) == 0 {
		return nil
	}

	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, string(k))
	}
	data, err := json.Marshal(struct {
		Kinds []string `json:"kinds"`
	}{Kinds: names})
	if err != nil {
		return fmt.Errorf("encoding hint event: %w", err)
	}

	return writeEvent(writer, "hint", string(data))
}

// writeEvent writes one SSE event frame.
func writeEvent(writer http.ResponseWriter, event, data string) error {
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("writing SSE event: %w", err)
	}

	return nil
}

// writeComment writes an SSE comment line used as a keep-alive ping.
func writeComment(writer http.ResponseWriter) error {
	if _, err := fmt.Fprint(writer, ": ping\n\n"); err != nil {
		return fmt.Errorf("writing SSE ping: %w", err)
	}

	return nil
}
