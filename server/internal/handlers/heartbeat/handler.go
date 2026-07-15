// Package heartbeat provides HTTP handlers for heartbeat check ingestion.
package heartbeat

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for heartbeat ingestion endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new heartbeat handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// maxHeartbeatBodyBytes bounds the JSON body accepted on a heartbeat ping.
// Output is a JSONB column on the shared results table written on every ping
// of a token-checked-but-otherwise-unauthenticated endpoint; 8 KiB is
// generous for a CI payload and far below anything that hurts.
const maxHeartbeatBodyBytes = 8 * 1024

// ErrBodyTooLarge is returned when the heartbeat body exceeds
// maxHeartbeatBodyBytes. Unlike malformed JSON (which is tolerated), an
// over-cap body is a hard rejection.
var ErrBodyTooLarge = errors.New("heartbeat body too large")

// extractToken resolves the heartbeat token, preferring the
// Authorization: Bearer header (the pattern used everywhere else in the API)
// and falling back to the historical ?token= query parameter so every
// existing caller keeps working unchanged. The header wins when both are
// present. This is a bespoke per-check token, not a JWT, so it is resolved
// here rather than via the RequireAuth middleware chain.
func extractToken(req bunrouter.Request) string {
	if auth := req.Header.Get("Authorization"); auth != "" {
		if token, ok := strings.CutPrefix(auth, "Bearer "); ok && token != "" {
			return token
		}
	}

	return req.URL.Query().Get("token")
}

// decodeHeartbeatBody reads and decodes the optional JSON heartbeat body,
// bounded to maxHeartbeatBodyBytes. It returns the "message" string (today's
// semantics, unchanged) and the remaining keys as callerData, which is nil
// when empty. Malformed JSON under the cap is tolerated — the parse error is
// swallowed and an empty result is returned, exactly like the previous
// fixed-struct decode — but exceeding the cap is a hard rejection
// (ErrBodyTooLarge).
func decodeHeartbeatBody(writer http.ResponseWriter, req bunrouter.Request) (message string, callerData map[string]any, err error) {
	if req.Body == nil || req.Header.Get("Content-Type") != "application/json" {
		return "", nil, nil
	}

	req.Body = http.MaxBytesReader(writer, req.Body, maxHeartbeatBodyBytes)

	var body map[string]any
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(decodeErr, &maxBytesErr) {
			return "", nil, ErrBodyTooLarge
		}

		// Malformed JSON stays lenient: a broken body must never fail a
		// liveness ping.
		return "", nil, nil
	}

	if raw, ok := body["message"]; ok {
		if str, ok := raw.(string); ok {
			message = str
		}

		delete(body, "message")
	}

	if len(body) > 0 {
		callerData = body
	}

	return message, callerData, nil
}

// ReceiveHeartbeat handles incoming heartbeat pings.
func (h *Handler) ReceiveHeartbeat(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("identifier")
	token := extractToken(req)
	status := req.URL.Query().Get("status")

	message, callerData, err := decodeHeartbeatBody(writer, req)
	if err != nil {
		return h.handleError(writer, err)
	}

	// Caller metadata, captured for forensics/display purposes only (which
	// script/cron/proxy is actually pinging this check) — same helper and
	// semantics as auth's session history, see base.ExtractRemoteAddr.
	userAgent := req.Header.Get("User-Agent")
	remoteAddr := base.ExtractRemoteAddr(req)
	httpMethod := req.Method

	if err := h.svc.ReceiveHeartbeat(
		req.Context(), orgSlug, identifier, token, status, message, userAgent, remoteAddr, httpMethod, callerData,
	); err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrNotHeartbeatCheck):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Check is not a heartbeat type")
	case errors.Is(err, ErrMissingToken):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Missing token parameter")
	case errors.Is(err, ErrInvalidToken):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Invalid token")
	case errors.Is(err, ErrInvalidStatus):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid status, must be one of: running, up, down, error")
	case errors.Is(err, ErrBodyTooLarge):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Heartbeat body too large")
	default:
		return h.WriteInternalError(writer, err)
	}
}
