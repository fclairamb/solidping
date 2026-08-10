// Package whatsapp is a minimal client for Meta's WhatsApp Business Cloud API
// (graph.facebook.com), plus X-Hub-Signature-256 validation for the inbound
// webhook. Like internal/integrations/twilio it depends on nothing but the
// standard library and internal/config, so both the escalation dispatcher and
// the HTTP handlers can import it without an import cycle.
//
// v1 sends **templates only**. Alerts are business-initiated messages, and
// WhatsApp does not deliver free-form text outside a 24-hour customer-service
// window, so a free-form send would simply be rejected by Meta most of the
// time. Templates must be created and approved on the WABA out of band — the
// server treats "template not found / paused / disabled" as a typed delivery
// failure, never a crash.
package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
)

const (
	// DefaultBaseURL is the real Graph API base.
	DefaultBaseURL = "https://graph.facebook.com"
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
	// signaturePrefix is the algorithm prefix Meta puts on X-Hub-Signature-256.
	signaturePrefix = "sha256="
)

// Typed delivery-failure classes. Every one of these is a *handled* outcome:
// the caller logs it, records it on the notification audit row and moves on to
// the next escalation step — none of them is ever fatal to the server.
var (
	// ErrTemplateUnavailable means the named template does not exist, is
	// paused, is disabled, or the supplied parameters do not match its
	// definition. The fix is always an operator action on the WABA.
	ErrTemplateUnavailable = errors.New("whatsapp: template unavailable (missing, paused, disabled or parameter mismatch)")
	// ErrRecipientNotOnWhatsApp means the number is not a reachable WhatsApp
	// user. The contact should not be retried on this channel.
	//
	// Kept deliberately narrow: only codes that genuinely mean "this number
	// cannot receive WhatsApp messages" belong here. Misfiling a transient
	// pacing error or a payload bug under this class tells an operator to
	// delete a perfectly good contact and hides the real cause.
	ErrRecipientNotOnWhatsApp = errors.New("whatsapp: recipient is not on WhatsApp")
	// ErrUnsupportedMessage means Meta rejected the message *we* built — an
	// unsupported type or a component/parameter shape that does not match the
	// approved template. This is our bug, not the recipient's problem, and it
	// must never be surfaced as "this number is invalid".
	ErrUnsupportedMessage = errors.New("whatsapp: message type or payload not supported by WhatsApp")
	// ErrSessionWindowClosed means the send needed an open 24-hour customer
	// service window and there wasn't one (re-engagement required). v1 sends
	// templates only, which are exempt, so this should be rare — but an
	// unmapped code degrading to a generic failure is strictly worse than
	// naming it.
	ErrSessionWindowClosed = errors.New("whatsapp: outside the 24-hour session window; a template is required")
	// ErrRateLimited means Meta throttled us — either the app request limit or
	// the WABA messaging-tier cap. Transient; the next escalation repeat may
	// succeed.
	ErrRateLimited = errors.New("whatsapp: rate limited or messaging tier cap reached")
	// ErrTokenExpired means the system-user access token is expired, revoked
	// or missing the whatsapp_business_messaging permission. Operator action.
	ErrTokenExpired = errors.New("whatsapp: access token expired or unauthorized")
	// ErrRequestFailed is the unclassified fallback for a non-2xx response.
	ErrRequestFailed = errors.New("whatsapp: request failed")
	// ErrNotConfigured is returned when the client is built from a config that
	// cannot send (kill switch off, or missing token / phone number id).
	ErrNotConfigured = errors.New("whatsapp: not configured on this instance")
	// ErrNoMessageID is returned when Meta accepts the request but returns no
	// message id — we cannot correlate a delivery status without one.
	ErrNoMessageID = errors.New("whatsapp: response carried no message id")
)

// e164Pattern validates E.164 phone numbers: a leading '+', a non-zero first
// digit, then 6-14 more digits (7-15 total). Deliberately a local copy — this
// package must not depend on internal/integrations/twilio.
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// ValidE164 reports whether s is a syntactically valid E.164 phone number.
func ValidE164(s string) bool {
	return e164Pattern.MatchString(s)
}

// normalizeRecipient strips the leading '+' Meta does not want on the wire
// while keeping our stored contact values in canonical E.164 form.
func normalizeRecipient(number string) string {
	return strings.TrimPrefix(strings.TrimSpace(number), "+")
}

// APIError carries Meta's structured error so the audit trail can show the
// operator exactly what Graph said. Wrapped by one of the typed sentinels
// above, so callers match with errors.Is and read details with errors.As.
type APIError struct {
	// StatusCode is the HTTP status of the Graph response.
	StatusCode int
	// Code / Subcode are Meta's numeric error codes.
	Code    int
	Subcode int
	// Message is Meta's human-readable message. Safe to store: it never
	// contains our credentials.
	Message string
	// Type is Meta's error type string (e.g. "OAuthException").
	Type string
	// class is the typed sentinel this error unwraps to.
	class error
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%v (http %d, code %d/%d): %s", e.class, e.StatusCode, e.Code, e.Subcode, e.Message)
}

// Unwrap lets errors.Is match the typed class sentinel.
func (e *APIError) Unwrap() error { return e.class }

// graphError is the wire shape of a Graph API error response.
type graphError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
		//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
		Subcode int `json:"error_subcode"`
		//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
		FBTraceID string `json:"fbtrace_id"`
	} `json:"error"`
}

// sendResponse is the wire shape of a successful /messages response.
type sendResponse struct {
	//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
	MessagingProduct string `json:"messaging_product"`
	Messages         []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

// Client talks to one WABA phone number.
type Client struct {
	httpClient    *http.Client
	baseURL       string
	apiVersion    string
	phoneNumberID string
	accessToken   string
}

// Options are the inputs to NewClient. Kept separate from config.WhatsAppConfig
// so tests can build a client without a whole server config.
type Options struct {
	BaseURL       string
	APIVersion    string
	PhoneNumberID string
	AccessToken   string
}

// NewClient builds a client from explicit options, defaulting the base URL and
// API version. Pass a BaseURL to target an httptest fake.
func NewClient(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.AccessToken) == "" || strings.TrimSpace(opts.PhoneNumberID) == "" {
		return nil, ErrNotConfigured
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	apiVersion := strings.TrimSpace(opts.APIVersion)
	if apiVersion == "" {
		apiVersion = config.DefaultWhatsAppAPIVersion
	}

	return &Client{
		httpClient:    &http.Client{Timeout: DefaultTimeout},
		baseURL:       baseURL,
		apiVersion:    apiVersion,
		phoneNumberID: strings.TrimSpace(opts.PhoneNumberID),
		accessToken:   strings.TrimSpace(opts.AccessToken),
	}, nil
}

// NewClientFromConfig builds a client from the instance WhatsApp config.
// Returns ErrNotConfigured when the instance cannot send.
func NewClientFromConfig(cfg *config.WhatsAppConfig) (*Client, error) {
	if !cfg.Active() {
		return nil, ErrNotConfigured
	}

	return NewClient(Options{
		BaseURL:       cfg.BaseURL,
		APIVersion:    cfg.ResolvedAPIVersion(),
		PhoneNumberID: cfg.PhoneNumberID,
		AccessToken:   cfg.AccessToken,
	})
}

// TemplateMessage describes one template send.
type TemplateMessage struct {
	// To is the recipient in E.164 form (with or without the leading '+').
	To string
	// Template is the approved template name on the WABA.
	Template string
	// Language is the template language code (e.g. "en").
	Language string
	// BodyParams are the ordered {{1}}…{{n}} body variables.
	BodyParams []string
	// ButtonParam, when non-empty, adds the template's dynamic URL button
	// component. It is NOT authentication-specific: the same component serves
	// both callers, because Meta gives a URL button exactly one variable.
	//
	//   - authentication templates (`solidping_verify`): the one-tap copy-code
	//     button Meta forces on the category; the value repeats the body code;
	//   - the alert template (`solidping_alert`): the "Visit website" button,
	//     whose value is the path appended to the static prefix baked into the
	//     approved template (see the docs — the base URL lives in the template,
	//     not in config).
	//
	// Empty means no button component at all, which is what an installation
	// whose approved template has no button requires.
	ButtonParam string
}

// SendTemplate posts one template message and returns the Meta message id
// (`wamid.…`), which is what inbound delivery-status webhooks key on.
func (c *Client) SendTemplate(ctx context.Context, msg *TemplateMessage) (string, error) {
	payload, err := buildTemplatePayload(msg)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal whatsapp payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/%s/messages", c.baseURL, c.apiVersion, c.phoneNumberID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating whatsapp request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending whatsapp request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= http.StatusMultipleChoices {
		return "", classifyError(resp.StatusCode, respBody)
	}

	var parsed sendResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parsing whatsapp response: %w", err)
	}

	if len(parsed.Messages) == 0 || parsed.Messages[0].ID == "" {
		return "", ErrNoMessageID
	}

	return parsed.Messages[0].ID, nil
}

// templatePayload / component types are the wire shape of the Cloud API
// template message.
type templatePayload struct {
	//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
	MessagingProduct string `json:"messaging_product"`
	//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
	RecipientType string           `json:"recipient_type"`
	To            string           `json:"to"`
	Type          string           `json:"type"`
	Template      templateEnvelope `json:"template"`
}

type templateEnvelope struct {
	Name       string              `json:"name"`
	Language   templateLanguage    `json:"language"`
	Components []templateComponent `json:"components,omitempty"`
}

type templateLanguage struct {
	Code string `json:"code"`
}

type templateComponent struct {
	Type string `json:"type"`
	//nolint:tagliatelle // Meta's Graph API wire format uses snake_case.
	SubType    string              `json:"sub_type,omitempty"`
	Index      string              `json:"index,omitempty"`
	Parameters []templateParameter `json:"parameters,omitempty"`
}

type templateParameter struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ErrInvalidRecipient is returned when the destination is not valid E.164.
var ErrInvalidRecipient = errors.New("whatsapp: recipient is not a valid E.164 number")

// ErrMissingTemplate is returned when no template name was supplied.
var ErrMissingTemplate = errors.New("whatsapp: template name is required")

func buildTemplatePayload(msg *TemplateMessage) (*templatePayload, error) {
	recipient := strings.TrimSpace(msg.To)
	if !ValidE164(recipient) && !ValidE164("+"+normalizeRecipient(recipient)) {
		return nil, ErrInvalidRecipient
	}

	if strings.TrimSpace(msg.Template) == "" {
		return nil, ErrMissingTemplate
	}

	language := strings.TrimSpace(msg.Language)
	if language == "" {
		language = config.DefaultWhatsAppTemplateLanguage
	}

	components := make([]templateComponent, 0, 2)

	if len(msg.BodyParams) > 0 {
		params := make([]templateParameter, 0, len(msg.BodyParams))
		for _, p := range msg.BodyParams {
			params = append(params, templateParameter{Type: "text", Text: p})
		}

		components = append(components, templateComponent{Type: "body", Parameters: params})
	}

	if msg.ButtonParam != "" {
		// The dynamic URL button component. Meta allows a URL button exactly one
		// variable, appended to the static prefix fixed at template-approval
		// time — so the same shape carries the authentication copy-code and the
		// alert template's deep link to the check. See TemplateMessage.
		components = append(components, templateComponent{
			Type:       "button",
			SubType:    "url",
			Index:      "0",
			Parameters: []templateParameter{{Type: "text", Text: msg.ButtonParam}},
		})
	}

	return &templatePayload{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               normalizeRecipient(recipient),
		Type:             "template",
		Template: templateEnvelope{
			Name:       strings.TrimSpace(msg.Template),
			Language:   templateLanguage{Code: language},
			Components: components,
		},
	}, nil
}

// Meta error codes, grouped by the failure class we must handle. Sourced from
// the Cloud API error reference; unknown codes fall back to the HTTP status and
// then to ErrRequestFailed, so an unlisted code degrades to a generic handled
// failure rather than an unhandled one.
//
//nolint:gochecknoglobals // immutable lookup table, effectively a constant.
var errorClassByCode = map[int]error{
	// Template problems — missing, paused, disabled, or parameter mismatch.
	132000: ErrTemplateUnavailable,
	132001: ErrTemplateUnavailable,
	132005: ErrTemplateUnavailable,
	132007: ErrTemplateUnavailable,
	132012: ErrTemplateUnavailable,
	132015: ErrTemplateUnavailable,
	132016: ErrTemplateUnavailable,
	132068: ErrTemplateUnavailable,
	132069: ErrTemplateUnavailable,
	// Recipient is not a reachable WhatsApp user. 131026 ("Message
	// undeliverable") is the only code that actually means this.
	131026: ErrRecipientNotOnWhatsApp,
	// Throttling, messaging-tier caps and per-user pacing. 131049 is Meta's
	// "not delivered to maintain healthy ecosystem engagement" — a transient
	// per-user pacing drop, NOT a bad number, so it belongs here rather than
	// with the recipient errors.
	4:      ErrRateLimited,
	80007:  ErrRateLimited,
	130429: ErrRateLimited,
	131048: ErrRateLimited,
	131049: ErrRateLimited,
	131056: ErrRateLimited,
	// Our own payload is wrong: 131051 is "Unsupported message type".
	131051: ErrUnsupportedMessage,
	// 132000 is a template component/parameter count mismatch. It is listed
	// with the template errors above because the operator fix is the same
	// (align the template with what we send).
	//
	// Session window: 131047 is "Re-engagement message" — the 24-hour window
	// has closed and a template is required.
	131047: ErrSessionWindowClosed,
	// Auth / permission.
	190: ErrTokenExpired,
	102: ErrTokenExpired,
	10:  ErrTokenExpired,
	200: ErrTokenExpired,
}

// classifyError turns a non-2xx Graph response into a typed *APIError.
func classifyError(status int, body []byte) error {
	var wire graphError
	_ = json.Unmarshal(body, &wire)

	apiErr := &APIError{
		StatusCode: status,
		Code:       wire.Error.Code,
		Subcode:    wire.Error.Subcode,
		Message:    wire.Error.Message,
		Type:       wire.Error.Type,
	}

	if class, ok := errorClassByCode[wire.Error.Code]; ok {
		apiErr.class = class

		return apiErr
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		apiErr.class = ErrTokenExpired
	case http.StatusTooManyRequests:
		apiErr.class = ErrRateLimited
	default:
		apiErr.class = ErrRequestFailed
	}

	return apiErr
}

// FailureReason maps a send error to a short, stable, operator-facing string
// for the notification audit row and the dashboard delivery history. Never
// includes credentials.
func FailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTemplateUnavailable):
		return "whatsapp_template_unavailable"
	case errors.Is(err, ErrRecipientNotOnWhatsApp):
		return "whatsapp_recipient_not_on_whatsapp"
	case errors.Is(err, ErrUnsupportedMessage):
		return "whatsapp_unsupported_message"
	case errors.Is(err, ErrSessionWindowClosed):
		return "whatsapp_session_window_closed"
	case errors.Is(err, ErrRateLimited):
		return "whatsapp_rate_limited"
	case errors.Is(err, ErrTokenExpired):
		return "whatsapp_token_expired"
	case errors.Is(err, ErrNotConfigured):
		return "whatsapp_not_configured"
	case errors.Is(err, ErrInvalidRecipient):
		return "whatsapp_invalid_recipient"
	default:
		return "whatsapp_send_failed"
	}
}

// ValidateSignature reports whether the X-Hub-Signature-256 header matches an
// HMAC-SHA256 of the exact raw request body keyed by the Meta app secret. The
// caller MUST pass the untouched bytes it will later parse — validating a
// re-serialized body is not validating anything.
//
// A missing or malformed header is a failure, never a pass: this is the only
// thing standing between the public webhook route and an unauthenticated
// caller writing to our delivery records.
func ValidateSignature(appSecret string, body []byte, header string) bool {
	if appSecret == "" || header == "" {
		return false
	}

	if !strings.HasPrefix(header, signaturePrefix) {
		return false
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(header, signaturePrefix))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)

	return hmac.Equal(mac.Sum(nil), provided)
}

// Sign returns the X-Hub-Signature-256 header value for a body. Exported for
// tests and for documenting the scheme in one place.
func Sign(appSecret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)

	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
