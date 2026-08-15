// Package ovhsmscb handles OVHcloud SMS delivery-receipt callbacks.
//
// Unlike Twilio, OVH does NOT sign its callbacks — there is no shared-secret
// HMAC to verify, and its API carries no per-job callback field either (the
// callback URL is a service-level setting on the SMS account). The endpoint
// therefore authenticates BY CONSTRUCTION: the URL itself carries a
// high-entropy token that only OVH and this instance know, compared in
// constant time, and every other field of the payload is treated as untrusted
// attacker-influenceable input.
package ovhsmscb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/integrations/ovhsms"
)

// DLRPath is the route pattern the callback is served on.
const DLRPath = "/dlr/:token"

// maxFieldLen bounds how much of an untrusted callback field is ever stored or
// echoed. The payload is unauthenticated apart from the path token, so no
// field is allowed to become an unbounded write.
const maxFieldLen = 128

// Handler serves the OVH delivery-receipt callback.
type Handler struct {
	db    db.Service
	token string
}

// NewHandler builds the callback handler. token is the path secret; an empty
// token disables the endpoint entirely (every request 404s), which is the
// correct behaviour when no OVH provider is configured.
func NewHandler(dbSvc db.Service, token string) *Handler {
	return &Handler{db: dbSvc, token: token}
}

// ResolveToken returns the DLR path token for this deployment: the explicitly
// configured SP_SMS_OVH_DLR_TOKEN when set, otherwise one derived
// deterministically from the JWT secret.
//
// Deriving rather than requiring one keeps a self-hoster from having to invent
// a secret to receive delivery receipts, while staying rotatable — rotating
// either variable rotates the URL. Returns "" when there is nothing to derive
// from, which disables the endpoint rather than serving a guessable one.
func ResolveToken(cfg *config.Config) string {
	if explicit := strings.TrimSpace(cfg.SMS.OVH.DLRToken); explicit != "" {
		return explicit
	}

	secret := strings.TrimSpace(cfg.Auth.JWTSecret)
	if secret == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("solidping:ovhsms:dlr"))

	return hex.EncodeToString(mac.Sum(nil))
}

// CallbackURL builds the absolute URL to register as the OVH SMS account's
// service-level `callBack`. Returns "" when either half is missing.
func CallbackURL(baseURL, token string) string {
	if baseURL == "" || token == "" {
		return ""
	}

	return strings.TrimRight(baseURL, "/") + "/api/v1/integrations/ovhsms/dlr/" + token
}

// HandleDLR records one delivery receipt.
//
// Contract:
//   - unknown or expired token → 404 with no body. Never a 401 with detail:
//     the endpoint must not confirm that a token-shaped URL exists at all.
//   - strictly idempotent — OVH retries, and the underlying update is a
//     set-by-message-id, so a replay writes the same row to the same value.
//   - always 204 on an accepted callback, even when the message id matches no
//     audit row. A 4xx/5xx would make OVH retry forever over a receipt for a
//     notification we simply no longer have.
func (h *Handler) HandleDLR(writer http.ResponseWriter, req *http.Request) error {
	if !h.tokenMatches(httpx.Param(req, "token")) {
		return notFound(writer)
	}

	// Every field below is attacker-influenceable: the endpoint is unsigned,
	// so possession of the URL is the only authentication. Bound the lengths,
	// never interpolate them into anything but a bounded string, and map the
	// status through a fixed table rather than echoing it.
	query := req.URL.Query()
	messageID := clamp(query.Get("id"))

	if messageID == "" {
		// Nothing to correlate; accept and drop so OVH stops retrying.
		writer.WriteHeader(http.StatusNoContent)

		return nil
	}

	state, description := ovhsms.PttState(query.Get("ptt"))

	details := &models.DeliveryDetails{
		ResponseBody: "ovh status: " + string(state) + " (" + description + ")",
	}

	// Org-agnostic on purpose: OVH's callback carries no tenant identity, and
	// the provider message id is globally unique within the SMS account.
	_ = h.db.UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(req.Context(), messageID, details)

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// tokenMatches compares the path token in constant time. An unconfigured
// handler matches nothing.
func (h *Handler) tokenMatches(candidate string) bool {
	if h.token == "" {
		return false
	}

	return hmac.Equal([]byte(h.token), []byte(candidate))
}

func clamp(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxFieldLen {
		return s[:maxFieldLen]
	}

	return s
}

func notFound(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusNotFound)

	return nil
}
