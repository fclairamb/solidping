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
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
	"github.com/fclairamb/solidping/server/internal/support"
)

// maxWebhookBody caps how much we will read from an unauthenticated caller
// before the signature has been checked. Meta batches are small; 1 MiB is
// generous and bounds the memory an attacker can make us allocate.
const maxWebhookBody = 1 << 20

// Handler serves the inbound WhatsApp webhook.
type Handler struct {
	db      db.Service
	cfg     *config.Config
	log     *slog.Logger
	support *support.Service
}

// Option customizes the handler.
type Option func(*Handler)

// WithSupport wires the support inbox so inbound human messages are captured
// instead of logged and dropped. Optional: a nil support service leaves the
// webhook working exactly as before.
func WithSupport(svc *support.Service) Option {
	return func(h *Handler) { h.support = svc }
}

// NewHandler builds a WhatsApp webhook handler.
func NewHandler(dbSvc db.Service, cfg *config.Config, opts ...Option) *Handler {
	h := &Handler{db: dbSvc, cfg: cfg, log: slog.Default()}
	for _, opt := range opts {
		opt(h)
	}

	return h
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
	h.captureInbound(req, payload)

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

// captureInbound records every user reply in the support inbox.
//
// Until spec 2026-08-22-02 this function logged the message id and the type and
// threw away the body and the sender — the two things a human actually sent.
// Every inbound message is now captured: an inbound WhatsApp message also opens
// the free 24-hour customer-service window in which we may answer with ordinary
// text, and that window used to open and close without anyone knowing it existed.
//
// Capture is best-effort for the request. CaptureSafe never returns an error and
// never panics, so the webhook still answers 204 and Meta never retries — a
// non-2xx here would eventually get the subscription disabled, which costs the
// channel rather than one message.
func (h *Handler) captureInbound(req *http.Request, payload *whatsapp.WebhookPayload) {
	messages := payload.InboundMessages()
	for i := range messages {
		msg := &messages[i]

		h.log.InfoContext(req.Context(), "inbound whatsapp message received",
			"messageId", msg.ID, "type", msg.Type)

		if h.support == nil {
			continue
		}

		body, rawType := whatsAppBody(msg)

		h.support.CaptureSafe(req.Context(), &support.Inbound{
			Channel: models.SupportChannelWhatsApp,
			// Meta delivers `from` as digits with no leading '+'. Normalised to
			// E.164 here so the thread identity matches what an operator sees
			// everywhere else and what a stored contact looks like.
			Identity:   normalizeWhatsAppNumber(msg.From),
			ExternalID: msg.ID,
			Body:       body,
			RawType:    rawType,
			At:         whatsAppTimestamp(msg.Timestamp),
		})
	}
}

// whatsAppBody returns a storable body and raw type for an inbound message.
// A non-text message records a placeholder rather than an empty body: "someone
// sent a photo" is information, "" is not.
func whatsAppBody(msg *whatsapp.InboundMessage) (string, string) {
	switch msg.Type {
	case "text", "":
		if msg.Text.Body != "" {
			return msg.Text.Body, models.SupportRawTypeText
		}

		return "(empty message)", models.SupportRawTypeText
	case models.SupportRawTypeImage, models.SupportRawTypeAudio, models.SupportRawTypeVideo,
		models.SupportRawTypeDocument, models.SupportRawTypeLocation, models.SupportRawTypeSticker:
		return "(" + msg.Type + " message — open WhatsApp to view it)", msg.Type
	default:
		return "(unsupported message type: " + msg.Type + ")", models.SupportRawTypeUnsupported
	}
}

// normalizeWhatsAppNumber puts the sender back into E.164.
func normalizeWhatsAppNumber(from string) string {
	from = strings.TrimSpace(from)
	if from == "" || strings.HasPrefix(from, "+") {
		return from
	}

	return "+" + from
}

// whatsAppTimestamp parses Meta's unix-seconds-as-a-string timestamp. An
// unparseable value yields the zero time, which the support service reads as
// "now" — a slightly wrong timestamp is better than a dropped message.
func whatsAppTimestamp(raw string) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}

	return time.Unix(seconds, 0).UTC()
}

func forbidden(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusForbidden)

	return nil
}
