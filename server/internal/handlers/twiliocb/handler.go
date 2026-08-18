// Package twiliocb handles inbound Twilio callbacks: the voice TwiML flow with
// DTMF acknowledgement and the SMS/call delivery-status callback. Every route
// is guarded by a verify middleware, which resolves the auth token named by the
// `cid` query param — a per-org connection's decrypted token, or the instance
// credentials of the capability the callback belongs to — and validates the
// X-Twilio-Signature over the exact request URL + POST params.
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
	"github.com/fclairamb/solidping/server/internal/integrations/sms"
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

const ctxKeyCallback ctxKey = iota

// callbackTarget is what the verified callback middleware hands the handlers:
// the org the call belongs to, and the `cid` to echo on any follow-up callback
// URL. It replaces the former *models.Integration because a server-credential
// send has no per-org integration at all — only the instance configuration.
type callbackTarget struct {
	orgUID string
	// connUID is the org integration's UID for a bring-your-own call, or
	// sms.InstanceCallbackID for a server-credential one.
	connUID string
}

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

// capability says which SolidPing capability an inbound callback belongs to,
// and therefore which instance credentials may validate it. It exists because
// SMS and voice are configured INDEPENDENTLY — an instance can run OVH for SMS
// and Twilio for voice, or Twilio SMS with SP_VOICE_ENABLED=false — so "the
// instance Twilio auth token" is not one thing.
type capability int

const (
	// capVoice is the voice-only TwiML/gather flow.
	capVoice capability = iota
	// capStatus is the shared delivery-status endpoint, which serves both an
	// SMS status callback (MessageSid) and a call status callback (CallSid).
	capStatus
)

// VerifyVoiceMiddleware authenticates an inbound TwiML/gather request, which
// is voice by construction.
func (h *Handler) VerifyVoiceMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return h.verify(next, capVoice)
}

// VerifyStatusMiddleware authenticates an inbound delivery-status callback,
// which may belong to either capability — see instanceAuthToken.
func (h *Handler) VerifyStatusMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return h.verify(next, capStatus)
}

// verify authenticates the inbound request: it resolves the Twilio
// connection from `cid`, decrypts its auth token, and validates the Twilio
// signature. On any failure it returns 403 with no body detail.
//
// The form is parsed BEFORE credentials are resolved: the shared /status
// endpoint tells an SMS callback from a call callback by its payload, and
// therefore has to read it to know which auth token applies.
//
// This path is region-agnostic by construction, confirmed (not assumed) when
// the region support was added: the signature is computed over SolidPing's
// own `cfg.Server.BaseURL` + request URI + POST params, signed with the
// account's auth token — no Twilio host appears anywhere in the signed
// material or in ValidateSignature itself, so it validates identically for a
// us1, ie1, or any other regional account.
func (h *Handler) verify(next httpx.HandlerFunc, capa capability) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		if err := req.ParseForm(); err != nil {
			return forbidden(writer)
		}

		target, authToken := h.resolveCallbackCredentials(req, capa)
		if target == nil || authToken == "" {
			return forbidden(writer)
		}

		fullURL := strings.TrimRight(h.cfg.Server.BaseURL, "/") + req.URL.RequestURI()
		signature := req.Header.Get("X-Twilio-Signature")
		if signature == "" || !twilio.ValidateSignature(authToken, fullURL, req.PostForm, signature) {
			return forbidden(writer)
		}

		ctx := context.WithValue(req.Context(), ctxKeyCallback, target)

		return next(writer, req.WithContext(ctx))
	}
}

// resolveCallbackCredentials picks which Twilio auth token validates this
// request's signature, from the `cid` query parameter:
//
//   - the instance sentinel → the instance-level token of the CAPABILITY this
//     callback belongs to (see instanceAuthToken), with the org taken from
//     `oid`;
//   - anything else → the named per-org Twilio integration's decrypted token.
//
// `oid` is attacker-supplied, but it is inside the signed URL: only a party
// holding the instance auth token can produce a signature over it, so a forged
// org id cannot pass this gate.
//
// Returns (nil, "") on any failure, which the caller turns into a bare 403.
func (h *Handler) resolveCallbackCredentials(req *http.Request, capa capability) (*callbackTarget, string) {
	cid := req.URL.Query().Get("cid")
	if cid == "" {
		return nil, ""
	}

	if cid == sms.InstanceCallbackID {
		orgUID := req.URL.Query().Get("oid")
		if orgUID == "" {
			return nil, ""
		}

		authToken := h.instanceAuthToken(req, capa)
		if authToken == "" {
			return nil, ""
		}

		return &callbackTarget{orgUID: orgUID, connUID: cid}, authToken
	}

	conn, err := h.db.GetChannel(req.Context(), cid)
	if err != nil || conn == nil || conn.Type != models.ConnectionTypeTwilio {
		return nil, ""
	}

	settings, err := twilioconn.DecryptSettings(req.Context(), h.creds, conn)
	if err != nil || settings.AuthToken == "" {
		return nil, ""
	}

	return &callbackTarget{orgUID: conn.OrganizationUID, connUID: conn.UID}, settings.AuthToken
}

// instanceAuthToken returns the instance-level Twilio auth token that may
// validate this server-credential callback, or "" to reject it.
//
// The capability decides, NOT whichever token happens to be populated: SMS and
// voice are configured independently, and the two tokens are frequently the
// same string (one Twilio account serving both), so reading the wrong one
// works on most deployments and silently 403s on the rest.
//
//   - voice endpoints (TwiML, gather) are voice by construction;
//   - the shared /status endpoint is told apart by its payload, exactly as
//     HandleStatus already does: a MessageSid is an SMS receipt, a CallSid is a
//     call receipt. Neither (or both) is rejected rather than guessed at.
func (h *Handler) instanceAuthToken(req *http.Request, capa capability) string {
	if capa == capVoice {
		return h.voiceInstanceAuthToken()
	}

	hasMessage := req.PostForm.Get("MessageSid") != ""
	hasCall := req.PostForm.Get("CallSid") != ""

	switch {
	case hasMessage && !hasCall:
		return h.smsInstanceAuthToken()
	case hasCall && !hasMessage:
		return h.voiceInstanceAuthToken()
	default:
		return ""
	}
}

// smsInstanceAuthToken returns the instance SMS auth token, but only when the
// instance actually sends SMS through Twilio. An OVH instance never asks for a
// Twilio status callback (its receipts arrive on the service-level DLR
// endpoint), so a request claiming to be one is rejected outright rather than
// validated against an unrelated Twilio account.
func (h *Handler) smsInstanceAuthToken() string {
	if !h.cfg.SMS.Active() || h.cfg.SMS.ResolvedProvider() != config.SMSProviderTwilio {
		return ""
	}

	return h.cfg.SMS.Twilio.AuthToken
}

// voiceInstanceAuthToken returns the instance voice auth token, gated on voice
// being active on this instance.
func (h *Handler) voiceInstanceAuthToken() string {
	if !h.cfg.Voice.Active() {
		return ""
	}

	return h.cfg.Voice.Twilio.AuthToken
}

// HandleVoice answers an outbound alert call with TwiML: it reads the incident
// aloud and gathers a DTMF digit to acknowledge.
func (h *Handler) HandleVoice(writer http.ResponseWriter, req *http.Request) error {
	target := callbackTargetFromContext(req.Context())
	if target == nil {
		return forbidden(writer)
	}

	token := req.URL.Query().Get("token")
	iid := req.URL.Query().Get("iid")

	payload, err := incidentlinks.Verify([]byte(h.cfg.Auth.JWTSecret), iid, token)
	if err != nil {
		return h.writeTwiML(writer, sayHangup("This alert can no longer be acknowledged. Goodbye."))
	}

	checkName := h.checkNameFor(req.Context(), target.orgUID, payload.IncidentUID)
	say := fmt.Sprintf("SolidPing alert. %s is down. Again: %s is down.", checkName, checkName)

	return h.writeTwiML(writer, voiceGatherTwiML(say, h.gatherActionURL(target, iid, token)))
}

// HandleGather processes the DTMF digit: 4 acknowledges the incident, anything
// else re-prompts.
func (h *Handler) HandleGather(writer http.ResponseWriter, req *http.Request) error {
	target := callbackTargetFromContext(req.Context())
	if target == nil {
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
			req.Context(), target.orgUID, payload.IncidentUID, from,
		); ackErr != nil {
			return h.writeTwiML(writer, sayHangup("Sorry, we could not acknowledge the incident. Goodbye."))
		}

		return h.writeTwiML(writer, sayHangup("Acknowledged. Goodbye."))
	}

	return h.writeTwiML(writer, voiceGatherTwiML(
		"Sorry, I did not get that. Press 4 to acknowledge.",
		h.gatherActionURL(target, iid, token),
	))
}

// HandleStatus records an SMS/call delivery-status update on the matching
// notification audit row.
func (h *Handler) HandleStatus(writer http.ResponseWriter, req *http.Request) error {
	target := callbackTargetFromContext(req.Context())
	if target == nil {
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
		_ = h.db.UpdateIncidentNotificationDeliveryByMessageID(req.Context(), target.orgUID, sid, details)
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

// gatherActionURL builds the signed callback URL the <Gather> posts the DTMF
// to. It echoes back the same cid/oid pair the inbound request carried, so the
// second hop re-authenticates against the same credentials as the first.
func (h *Handler) gatherActionURL(target *callbackTarget, iid, token string) string {
	return fmt.Sprintf("%s/api/v1/integrations/twilio/voice/gather?cid=%s&oid=%s&iid=%s&token=%s",
		strings.TrimRight(h.cfg.Server.BaseURL, "/"),
		url.QueryEscape(target.connUID), url.QueryEscape(target.orgUID),
		url.QueryEscape(iid), url.QueryEscape(token))
}

func (h *Handler) writeTwiML(writer http.ResponseWriter, body string) error {
	writer.Header().Set("Content-Type", "text/xml; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(body))

	return nil
}

func callbackTargetFromContext(ctx context.Context) *callbackTarget {
	target, _ := ctx.Value(ctxKeyCallback).(*callbackTarget)

	return target
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
