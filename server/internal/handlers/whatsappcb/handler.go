// Package whatsappcb handles Meta's inbound WhatsApp Cloud API webhook: the
// GET subscription handshake and the POST event stream carrying delivery
// statuses and inbound user replies.
//
// It is the WhatsApp counterpart to internal/handlers/twiliocb, with one
// structural difference: Twilio callbacks are org-scoped (they carry a `cid`
// naming the org's connection), while WhatsApp is an *instance*-level
// integration — one WABA for the whole deployment. Authenticity therefore comes
// from the app secret rather than a per-org credential, and delivery statuses
// are matched on the globally-unique `wamid.…` message id alone.
//
// SECURITY: the POST route is public. Its only gate is the
// X-Hub-Signature-256 HMAC over the *raw* request body, which is validated
// BEFORE the body is parsed. A missing, malformed or mismatched signature is a
// 403 with no body detail — never a 400 that tells a prober how far it got.
package whatsappcb

import (
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
)

// maxWebhookBody caps how much we will read from an unauthenticated caller
// before the signature has been checked. Meta batches are small; 1 MiB is
// generous and bounds the memory an attacker can make us allocate.
const maxWebhookBody = 1 << 20

// Handler serves the inbound WhatsApp webhook.
type Handler struct {
	db  db.Service
	cfg *config.Config
	log *slog.Logger
}

// NewHandler builds a WhatsApp webhook handler.
func NewHandler(dbSvc db.Service, cfg *config.Config) *Handler {
	return &Handler{db: dbSvc, cfg: cfg, log: slog.Default()}
}

// whatsappConfig returns the instance WhatsApp config, or the zero value.
func (h *Handler) whatsappConfig() config.WhatsAppConfig {
	if h.cfg == nil {
		return config.WhatsAppConfig{}
	}

	return h.cfg.WhatsApp
}

// HandleVerify answers Meta's GET subscription handshake: when hub.mode is
// "subscribe" and hub.verify_token matches the configured token, echo
// hub.challenge verbatim as text/plain. Anything else is a 403.
//
// The token compare is constant-time — the handshake is repeatable at will by
// an anonymous caller, so a byte-by-byte compare would be an oracle.
func (h *Handler) HandleVerify(writer http.ResponseWriter, req *http.Request) error {
	cfg := h.whatsappConfig()

	query := req.URL.Query()
	mode := query.Get("hub.mode")
	token := query.Get("hub.verify_token")
	challenge := query.Get("hub.challenge")

	if cfg.WebhookVerifyToken == "" || mode != "subscribe" || challenge == "" {
		return forbidden(writer)
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.WebhookVerifyToken)) != 1 {
		return forbidden(writer)
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(challenge))

	return nil
}

// HandleEvent processes a POST event batch: delivery statuses update the
// matching notification audit row, inbound replies are logged only.
//
// Meta retries on any non-2xx, so once the request is authenticated we answer
// 200 even if an individual record could not be applied — a stuck retry loop
// is worse than a missing delivery detail.
func (h *Handler) HandleEvent(writer http.ResponseWriter, req *http.Request) error {
	cfg := h.whatsappConfig()

	// Read the raw body first: the signature covers these exact bytes, and
	// validating anything re-serialized would validate nothing.
	body, err := io.ReadAll(io.LimitReader(req.Body, maxWebhookBody))
	if err != nil {
		return forbidden(writer)
	}

	if !whatsapp.ValidateSignature(cfg.AppSecret, body, req.Header.Get("X-Hub-Signature-256")) {
		return forbidden(writer)
	}

	payload, err := whatsapp.ParseWebhook(body)
	if err != nil {
		// Authenticated but unparseable: acknowledge so Meta stops retrying a
		// batch we will never understand, and leave a trace for the operator.
		h.log.WarnContext(req.Context(), "unparseable whatsapp webhook payload", "error", err)
		writer.WriteHeader(http.StatusNoContent)

		return nil
	}

	h.applyStatuses(req, payload)
	h.logInbound(req, payload)

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// applyStatuses records each delivery-status transition on the notification
// audit row that carries the matching provider message id.
func (h *Handler) applyStatuses(req *http.Request, payload *whatsapp.WebhookPayload) {
	statuses := payload.Statuses()
	for i := range statuses {
		status := &statuses[i]
		if status.ID == "" || status.Status == "" {
			continue
		}

		details := &models.DeliveryDetails{ResponseBody: status.Describe()}

		if err := h.db.UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(
			req.Context(), status.ID, details,
		); err != nil {
			h.log.WarnContext(req.Context(), "failed to record whatsapp delivery status",
				"messageId", status.ID, "status", status.Status, "error", err)
		}

		if status.Status == whatsapp.StatusFailed {
			h.log.InfoContext(req.Context(), "whatsapp delivery failed",
				"messageId", status.ID, "detail", status.Describe())
		}
	}
}

// logInbound records user replies. v1 accepts them (they open the free 24-hour
// session window) but implements no command handling.
func (h *Handler) logInbound(req *http.Request, payload *whatsapp.WebhookPayload) {
	messages := payload.InboundMessages()
	for i := range messages {
		h.log.InfoContext(req.Context(), "inbound whatsapp message received (no command handling in v1)",
			"messageId", messages[i].ID, "type", messages[i].Type)
	}
}

func forbidden(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusForbidden)

	return nil
}
