// Package heartbeat provides HTTP handlers for heartbeat check ingestion.
package heartbeat

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
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

// maxHeartbeatDurationMs caps the accepted "durationMs" value to 7 days in
// milliseconds — a generous sanity ceiling for any real job that also keeps
// the eventual float32 conversion safely within range.
const maxHeartbeatDurationMs = 7 * 24 * 60 * 60 * 1000

// extractToken resolves the heartbeat token, preferring the
// Authorization: Bearer header (the pattern used everywhere else in the API)
// and falling back to the historical ?token= query parameter so every
// existing caller keeps working unchanged. The header wins when both are
// present. This is a bespoke per-check token, not a JWT, so it is resolved
// here rather than via the RequireAuth middleware chain.
func extractToken(req *http.Request) string {
	if auth := req.Header.Get("Authorization"); auth != "" {
		if token, ok := strings.CutPrefix(auth, "Bearer "); ok && token != "" {
			return token
		}
	}

	return req.URL.Query().Get("token")
}

// decodeHeartbeatBody reads and decodes the optional JSON heartbeat body,
// bounded to maxHeartbeatBodyBytes. It returns the "message" string and
// "durationMs" number (today's semantics, unchanged for message) and the
// remaining keys as callerData, which is nil when empty. Malformed JSON under
// the cap is tolerated — the parse error is swallowed and an empty result is
// returned, exactly like the previous fixed-struct decode — but exceeding the
// cap is a hard rejection (ErrBodyTooLarge).
func decodeHeartbeatBody(
	writer http.ResponseWriter, req *http.Request,
) (string, float32, map[string]any, error) {
	if req.Body == nil || req.Header.Get("Content-Type") != "application/json" {
		return "", 0, nil, nil
	}

	req.Body = http.MaxBytesReader(writer, req.Body, maxHeartbeatBodyBytes)

	var body map[string]any
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(decodeErr, &maxBytesErr) {
			return "", 0, nil, ErrBodyTooLarge
		}

		// Malformed JSON stays lenient: a broken body must never fail a
		// liveness ping.
		return "", 0, nil, nil
	}

	var message string
	if raw, ok := body["message"]; ok {
		if str, ok := raw.(string); ok {
			message = str
		}

		delete(body, "message")
	}

	durationMs := extractHeartbeatDuration(body)

	var callerData map[string]any
	if len(body) > 0 {
		callerData = body
	}

	return message, durationMs, callerData, nil
}

// extractHeartbeatDuration consumes the top-level "durationMs" key from body
// when it is a JSON number that is finite, >= 0, and <=
// maxHeartbeatDurationMs, deleting it from body so it is not duplicated under
// output.data. An invalid value (wrong type, negative, NaN/Inf, over the cap)
// is left untouched in body — it lands under output.data as ordinary caller
// data instead, so the caller can see exactly what they sent when debugging
// an empty response-time chart. A bad/absent durationMs never fails the ping;
// it simply yields 0.
func extractHeartbeatDuration(body map[string]any) float32 {
	raw, ok := body["durationMs"]
	if !ok {
		return 0
	}

	num, ok := raw.(float64)
	if !ok || math.IsNaN(num) || math.IsInf(num, 0) || num < 0 || num > maxHeartbeatDurationMs {
		return 0
	}

	delete(body, "durationMs")

	return float32(num)
}

// ReceiveHeartbeat handles incoming heartbeat pings.
func (h *Handler) ReceiveHeartbeat(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "identifier")
	token := extractToken(req)
	status := req.URL.Query().Get("status")

	message, durationMs, callerData, err := decodeHeartbeatBody(writer, req)
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
		req.Context(), orgSlug, identifier, token, status, message, durationMs,
		userAgent, remoteAddr, httpMethod, callerData,
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
