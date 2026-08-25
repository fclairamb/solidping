package twiliocb

import (
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/support"
)

// MessagePath is the inbound-SMS webhook path, relative to the Twilio
// integration group.
const MessagePath = "/message"

// carrierKeywords are the SMS keywords that carry LEGAL meaning and are handled
// by Twilio's platform-level Advanced Opt-Out, not by us.
//
// They are deliberately NOT captured as support messages. A person texting STOP
// is exercising an opt-out, and turning that into a support ticket would bury a
// real opt-out in an inbox nobody is obliged to read — while also implying we
// handled something we did not. Twilio still processes them exactly as before;
// we simply answer 200 and record nothing.
//
// keywordStop is the opt-out keyword, spelled once because it also appears in
// the outbound disclosure footer.
const keywordStop = "stop"

//nolint:gochecknoglobals // immutable lookup table, effectively a constant
var carrierKeywords = map[string]bool{
	keywordStop: true, "stopall": true, "unsubscribe": true, "cancel": true,
	"end": true, "quit": true,
	"start": true, "unstop": true, "yes": true,
	"help": true, "info": true,
}

// IsCarrierKeyword reports whether an inbound SMS body is a carrier opt-out /
// opt-in / help keyword. Exported so the behavior is directly testable.
func IsCarrierKeyword(body string) bool {
	normalized := strings.ToLower(strings.TrimSpace(body))
	// Carriers match the keyword alone, not a sentence containing it: "please
	// stop paging me at 3am" is a support message, not an opt-out.
	return carrierKeywords[normalized]
}

// VerifyMessageMiddleware authenticates an inbound SMS webhook.
//
// Unlike the status and voice callbacks, this URL is configured once on the
// Messaging Service and therefore carries no `cid`: there is no per-org
// connection to resolve, so the only credential that can validate it is the
// instance SMS auth token. An instance that does not send SMS through Twilio
// rejects the request outright rather than validating it against an unrelated
// account.
func (h *Handler) VerifyMessageMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		if err := req.ParseForm(); err != nil {
			return forbidden(writer)
		}

		authToken := h.smsInstanceAuthToken()
		if authToken == "" {
			return forbidden(writer)
		}

		if !h.validSignature(req, authToken) {
			return forbidden(writer)
		}

		return next(writer, req)
	}
}

// HandleMessage captures an inbound SMS reply in the support inbox.
//
// Until spec 2026-08-22-02 no inbound SMS route existed at all: the Messaging
// Service had `use_inbound_webhook_on_number: true` and SolidPing exposed no
// endpoint, so an SMS reply died at Twilio with no trace anywhere.
//
// Always answers 200 with an empty TwiML document — a non-2xx makes Twilio
// retry and, eventually, flag the endpoint. Capture is best-effort for the
// request by construction (CaptureSafe never returns an error).
func (h *Handler) HandleMessage(writer http.ResponseWriter, req *http.Request) error {
	from := strings.TrimSpace(req.PostForm.Get("From"))
	body := req.PostForm.Get("Body")
	messageSID := strings.TrimSpace(req.PostForm.Get("MessageSid"))

	switch {
	case h.support == nil || from == "":
		// Nothing to capture into, or nothing to attribute it to.
	case IsCarrierKeyword(body):
		// Carrier keyword: Twilio owns it. Recording nothing is the point.
	default:
		h.support.CaptureSafe(req.Context(), &support.Inbound{
			Channel:    models.SupportChannelSMS,
			Identity:   from,
			ExternalID: messageSID,
			Body:       body,
			RawType:    models.SupportRawTypeText,
		})
	}

	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(xmlHeader + "<Response></Response>"))

	return nil
}
