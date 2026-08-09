// Package twiliocb handles inbound Twilio callbacks: the voice TwiML flow with
// DTMF acknowledgement and the SMS/call delivery-status callback. Every route
// is guarded by VerifyMiddleware, which loads the connection named by the `cid`
// query param, decrypts its auth token, and validates the X-Twilio-Signature
// over the exact request URL + POST params.
package twiliocb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/integrations/twilioconn"
)

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`

// twimlEscaper escapes the five XML metacharacters for use in TwiML bodies.
//
//nolint:gochecknoglobals // immutable replacer, effectively a constant.
var twimlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// IncidentAcker is the minimal incidents-service surface the DTMF flow needs.
type IncidentAcker interface {
	AcknowledgeIncidentFromPhone(
		ctx context.Context, orgUID, incidentUID, phone string,
	) (*models.Incident, error)
}

type ctxKey int

const ctxKeyIntegration ctxKey = iota

// Handler serves the inbound Twilio callbacks.
type Handler struct {
	db    db.Service
	creds credentials.Service
	cfg   *config.Config
	acker IncidentAcker
}

// NewHandler builds a Twilio callback handler.
func NewHandler(dbSvc db.Service, creds credentials.Service, cfg *config.Config, acker IncidentAcker) *Handler {
	return &Handler{db: dbSvc, creds: creds, cfg: cfg, acker: acker}
}

// VerifyMiddleware authenticates the inbound request: it resolves the Twilio
// connection from `cid`, decrypts its auth token, and validates the Twilio
// signature. On any failure it returns 403 with no body detail.
//
// This path is region-agnostic by construction, confirmed (not assumed) when
// the region support was added: the signature is computed over SolidPing's
// own `cfg.Server.BaseURL` + request URI + POST params, signed with the
// account's auth token — no Twilio host appears anywhere in the signed
// material or in ValidateSignature itself, so it validates identically for a
// us1, ie1, or any other regional account.
func (h *Handler) VerifyMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		cid := req.URL.Query().Get("cid")
		if cid == "" {
			return forbidden(writer)
		}

		conn, err := h.db.GetChannel(req.Context(), cid)
		if err != nil || conn == nil || conn.Type != models.ConnectionTypeTwilio {
			return forbidden(writer)
		}

		settings, err := twilioconn.DecryptSettings(req.Context(), h.creds, conn)
		if err != nil || settings.AuthToken == "" {
			return forbidden(writer)
		}

		if err := req.ParseForm(); err != nil {
			return forbidden(writer)
		}

		fullURL := strings.TrimRight(h.cfg.Server.BaseURL, "/") + req.URL.RequestURI()
		signature := req.Header.Get("X-Twilio-Signature")
		if signature == "" || !twilio.ValidateSignature(settings.AuthToken, fullURL, req.PostForm, signature) {
			return forbidden(writer)
		}

		ctx := context.WithValue(req.Context(), ctxKeyIntegration, conn)

		return next(writer, req.WithContext(ctx))
	}
}

// HandleVoice answers an outbound alert call with TwiML: it reads the incident
// aloud and gathers a DTMF digit to acknowledge.
func (h *Handler) HandleVoice(writer http.ResponseWriter, req *http.Request) error {
	conn := integrationFromContext(req.Context())
	if conn == nil {
		return forbidden(writer)
	}

	token := req.URL.Query().Get("token")
	iid := req.URL.Query().Get("iid")

	payload, err := incidentlinks.Verify([]byte(h.cfg.Auth.JWTSecret), iid, token)
	if err != nil {
		return h.writeTwiML(writer, sayHangup("This alert can no longer be acknowledged. Goodbye."))
	}

	checkName := h.checkNameFor(req.Context(), conn.OrganizationUID, payload.IncidentUID)
	say := fmt.Sprintf("SolidPing alert. %s is down. Again: %s is down.", checkName, checkName)

	return h.writeTwiML(writer, voiceGatherTwiML(say, h.gatherActionURL(conn.UID, iid, token)))
}

// HandleGather processes the DTMF digit: 4 acknowledges the incident, anything
// else re-prompts.
func (h *Handler) HandleGather(writer http.ResponseWriter, req *http.Request) error {
	conn := integrationFromContext(req.Context())
	if conn == nil {
		return forbidden(writer)
	}

	token := req.URL.Query().Get("token")
	iid := req.URL.Query().Get("iid")

	payload, err := incidentlinks.Verify([]byte(h.cfg.Auth.JWTSecret), iid, token)
	if err != nil {
		return h.writeTwiML(writer, sayHangup("This alert can no longer be acknowledged. Goodbye."))
	}

	if req.PostForm.Get("Digits") == "4" {
		from := req.PostForm.Get("From")
		if _, ackErr := h.acker.AcknowledgeIncidentFromPhone(
			req.Context(), conn.OrganizationUID, payload.IncidentUID, from,
		); ackErr != nil {
			return h.writeTwiML(writer, sayHangup("Sorry, we could not acknowledge the incident. Goodbye."))
		}

		return h.writeTwiML(writer, sayHangup("Acknowledged. Goodbye."))
	}

	return h.writeTwiML(writer, voiceGatherTwiML(
		"Sorry, I did not get that. Press 4 to acknowledge.",
		h.gatherActionURL(conn.UID, iid, token),
	))
}

// HandleStatus records an SMS/call delivery-status update on the matching
// notification audit row.
func (h *Handler) HandleStatus(writer http.ResponseWriter, req *http.Request) error {
	conn := integrationFromContext(req.Context())
	if conn == nil {
		return forbidden(writer)
	}

	sid := req.PostForm.Get("MessageSid")
	if sid == "" {
		sid = req.PostForm.Get("CallSid")
	}

	status := req.PostForm.Get("MessageStatus")
	if status == "" {
		status = req.PostForm.Get("CallStatus")
	}

	if sid != "" && status != "" {
		details := &models.DeliveryDetails{ResponseBody: "twilio status: " + status}
		_ = h.db.UpdateIncidentNotificationDeliveryByMessageID(req.Context(), conn.OrganizationUID, sid, details)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// checkNameFor returns the incident's check name for the voice script, with a
// safe generic fallback.
func (h *Handler) checkNameFor(ctx context.Context, orgUID, incidentUID string) string {
	incident, err := h.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil || incident == nil {
		return "a monitored service"
	}

	check, err := h.db.GetCheck(ctx, orgUID, incident.CheckUID)
	if err == nil && check != nil && check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	return "a monitored service"
}

// gatherActionURL builds the signed callback URL the <Gather> posts the DTMF to.
func (h *Handler) gatherActionURL(connUID, iid, token string) string {
	return fmt.Sprintf("%s/api/v1/integrations/twilio/voice/gather?cid=%s&iid=%s&token=%s",
		strings.TrimRight(h.cfg.Server.BaseURL, "/"),
		url.QueryEscape(connUID), url.QueryEscape(iid), url.QueryEscape(token))
}

func (h *Handler) writeTwiML(writer http.ResponseWriter, body string) error {
	writer.Header().Set("Content-Type", "text/xml; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(body))

	return nil
}

func integrationFromContext(ctx context.Context) *models.Integration {
	conn, _ := ctx.Value(ctxKeyIntegration).(*models.Integration)

	return conn
}

func forbidden(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusForbidden)

	return nil
}

func sayHangup(text string) string {
	return xmlHeader + "<Response><Say>" + twimlEscaper.Replace(text) + "</Say><Hangup/></Response>"
}

func voiceGatherTwiML(say, action string) string {
	return xmlHeader + "<Response>" +
		"<Say>" + twimlEscaper.Replace(say) + "</Say>" +
		`<Gather numDigits="1" method="POST" action="` + twimlEscaper.Replace(action) + `">` +
		"<Say>Press 4 to acknowledge.</Say>" +
		"</Gather>" +
		"<Say>No input received. Goodbye.</Say><Hangup/>" +
		"</Response>"
}
