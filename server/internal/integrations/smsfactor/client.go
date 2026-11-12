// Package smsfactor is a minimal SMS Factor (smsfactor.com) REST client. It is
// hand-rolled on the standard library only, exactly like
// internal/integrations/twilio and internal/integrations/ovhsms, so it can be
// imported from both the notification senders and the HTTP handlers without an
// import cycle and without pulling an SDK into the build.
//
// There is no delivery-receipt handler for this provider: the send body used
// here carries no per-message callback URL, and SMS Factor's receipts are a
// per-account webhook setting. A send is therefore "accepted" once the API
// returns a ticket, and nothing more is learned about it afterwards.
package smsfactor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/httpclientpool"
)

const (
	// DefaultBaseURL is the real SMS Factor API base.
	DefaultBaseURL = "https://api.smsfactor.com"
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
	// MaxSenderLength is the longest alphanumeric sender SMS Factor accepts.
	MaxSenderLength = 11
	// pushTypeAlert marks the message as transactional (incident alerts),
	// as opposed to "marketing", which carries opt-out obligations and
	// time-of-day restrictions.
	pushTypeAlert = "alert"
	// maxResponseBytes bounds how much of a response body is read.
	maxResponseBytes = 64 << 10
)

// SMS Factor API status codes, carried in the response body's "status" field.
// The HTTP status can be 200 with a negative code, so the body is authoritative.
const (
	StatusOK               = 1
	StatusAuthError        = -1
	StatusXMLError         = -2
	StatusNotEnoughCredits = -3
	StatusBadDelay         = -4
	StatusNotFound         = -5
	StatusJSONError        = -6
	StatusDataError        = -7
	StatusUnderModeration  = -8
	StatusUnknown          = -99
)

var (
	// ErrRequestFailed is returned when the API could not be reached or
	// answered with a non-2xx HTTP status and no usable status body.
	ErrRequestFailed = errors.New("smsfactor request failed")
	// ErrRejected is returned when the API answered with a negative status
	// code (bad token, no credits, malformed data...). The wrapped message
	// carries the code and SMS Factor's own message.
	ErrRejected = errors.New("smsfactor rejected the request")
	// ErrNoRecipientReached is returned when SMS Factor accepted the request
	// (status 1) but sent nothing: every number was filtered as invalid,
	// blacklisted, duplicated, flooded or country-limited.
	ErrNoRecipientReached = errors.New("smsfactor: no recipient reached")
	// ErrMissingToken is returned when the API token is empty.
	ErrMissingToken = errors.New("smsfactor: api token is required")
	// ErrNoRecipients is returned when Send is called without a recipient.
	ErrNoRecipients = errors.New("smsfactor: at least one recipient is required")
	// ErrTokenRejected is returned by VerifyToken when SMS Factor refuses the
	// token.
	ErrTokenRejected = errors.New("smsfactor rejected this api token")
	// ErrTokenUnverifiable is returned by VerifyToken when SMS Factor could not
	// be reached, timed out, or answered unexpectedly: the token might be
	// fine, but it could not be proven, so the caller must refuse the write.
	ErrTokenUnverifiable = errors.New("smsfactor api token could not be verified")
)

// senderPattern is the accepted sender shape: 1-11 letters, digits or spaces.
var senderPattern = regexp.MustCompile(`^[A-Za-z0-9 ]{1,11}$`)

// ValidSender reports whether sender (already trimmed) is an acceptable
// alphanumeric sender: 1 to MaxSenderLength letters, digits or spaces. The
// sender itself must still be registered on the SMS Factor account.
func ValidSender(sender string) bool {
	return senderPattern.MatchString(sender)
}

// Client is an SMS Factor API client scoped to one API token.
type Client struct {
	// BaseURL is the API base, DefaultBaseURL unless overridden.
	BaseURL string
	// Token is the API token. SECRET.
	Token string
	// HTTPClient performs the requests.
	HTTPClient *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithBaseURL points the client at another API base (tests, egress proxy).
// An empty value keeps DefaultBaseURL.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if baseURL != "" {
			c.BaseURL = strings.TrimRight(baseURL, "/")
		}
	}
}

// WithHTTPClient overrides the HTTP client (tests).
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.HTTPClient = httpClient
		}
	}
}

// NewClient builds a client targeting the real API unless an option says
// otherwise.
func NewClient(token string, opts ...Option) *Client {
	client := &Client{
		BaseURL:    DefaultBaseURL,
		Token:      token,
		HTTPClient: httpclientpool.NewClient(DefaultTimeout),
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// SendParams are the inputs to Send.
type SendParams struct {
	// Recipients are E.164 numbers; the leading '+' is stripped on the wire.
	Recipients []string
	// Text is the SMS body.
	Text string
	// Sender is the registered alphanumeric sender. Empty lets SMS Factor
	// pick its default.
	Sender string
}

// SendResult is the useful part of SMS Factor's send response.
type SendResult struct {
	// Ticket is the campaign id, the provider message id.
	Ticket string
	// Status is the API status code (StatusOK or StatusUnderModeration).
	Status int
	// Sent is how many recipients the message was sent to.
	Sent int
	// Credits is the account balance after the send.
	Credits float64
	// Cost is the credits this send consumed.
	Cost float64
}

type message struct {
	Text     string `json:"text"`
	PushType string `json:"pushtype"`
	Sender   string `json:"sender,omitempty"`
}

type gsm struct {
	GSMSMSID string `json:"gsmsmsid"`
	Value    string `json:"value"`
}

type recipients struct {
	GSM []gsm `json:"gsm"`
}

type sendBody struct {
	SMS struct {
		Message    message    `json:"message"`
		Recipients recipients `json:"recipients"`
	} `json:"sms"`
}

// apiResponse is the shared envelope of every SMS Factor JSON answer. The
// numeric fields use flexNumber because the API is not consistent about
// quoting them.
//
//nolint:tagliatelle // field names match the SMS Factor wire format.
type apiResponse struct {
	Status       flexNumber `json:"status"`
	Message      string     `json:"message"`
	Details      string     `json:"details"`
	Ticket       flexString `json:"ticket"`
	Cost         flexNumber `json:"cost"`
	Credits      flexNumber `json:"credits"`
	Total        flexNumber `json:"total"`
	Sent         flexNumber `json:"sent"`
	Blacklisted  flexNumber `json:"blacklisted"`
	Duplicated   flexNumber `json:"duplicated"`
	NPAI         flexNumber `json:"npai"`
	Invalid      flexNumber `json:"invalid"`
	NotAllowed   flexNumber `json:"not_allowed"`
	Flood        flexNumber `json:"flood"`
	CountryLimit flexNumber `json:"country_limit"`
}

// Send posts one SMS to every recipient in a single campaign. Status -8
// (campaign under moderation) is a success: SMS Factor will still send the
// message, and retrying would send it twice.
func (c *Client) Send(ctx context.Context, params *SendParams) (*SendResult, error) {
	if strings.TrimSpace(c.Token) == "" {
		return nil, ErrMissingToken
	}

	if len(params.Recipients) == 0 {
		return nil, ErrNoRecipients
	}

	var body sendBody

	body.SMS.Message = message{
		Text:     params.Text,
		PushType: pushTypeAlert,
		Sender:   strings.TrimSpace(params.Sender),
	}

	body.SMS.Recipients.GSM = make([]gsm, 0, len(params.Recipients))
	for _, number := range params.Recipients {
		body.SMS.Recipients.GSM = append(body.SMS.Recipients.GSM, gsm{
			GSMSMSID: newGSMSMSID(),
			Value:    strings.TrimPrefix(strings.TrimSpace(number), "+"),
		})
	}

	resp, err := c.do(ctx, http.MethodPost, "/", body)
	if err != nil {
		return nil, err
	}

	status := resp.Status.Int()

	result := &SendResult{
		Ticket:  string(resp.Ticket),
		Status:  status,
		Sent:    resp.Sent.Int(),
		Credits: float64(resp.Credits),
		Cost:    float64(resp.Cost),
	}

	if status == StatusUnderModeration {
		return result, nil
	}

	if result.Sent == 0 {
		return nil, fmt.Errorf(
			"%w: total=%d invalid=%d blacklisted=%d duplicated=%d npai=%d not_allowed=%d flood=%d country_limit=%d",
			ErrNoRecipientReached, resp.Total.Int(), resp.Invalid.Int(), resp.Blacklisted.Int(),
			resp.Duplicated.Int(), resp.NPAI.Int(), resp.NotAllowed.Int(), resp.Flood.Int(),
			resp.CountryLimit.Int())
	}

	return result, nil
}

// VerifyToken makes one side-effect-free authenticated call, GET /credits, to
// confirm the token works. Mirrors twilio.VerifyCredentials, including the
// split between "this token is wrong" (ErrTokenRejected) and "we could not
// check right now" (ErrTokenUnverifiable).
func (c *Client) VerifyToken(ctx context.Context) error {
	if strings.TrimSpace(c.Token) == "" {
		return ErrTokenRejected
	}

	_, err := c.do(ctx, http.MethodGet, "/credits", nil)
	if err == nil {
		return nil
	}

	var rejected *rejectedError
	if errors.As(err, &rejected) && rejected.code == StatusAuthError {
		return fmt.Errorf("%w: %w", ErrTokenRejected, err)
	}

	var httpErr *httpStatusError
	if errors.As(err, &httpErr) &&
		(httpErr.code == http.StatusUnauthorized || httpErr.code == http.StatusForbidden) {
		return fmt.Errorf("%w: %w", ErrTokenRejected, err)
	}

	return fmt.Errorf("%w: %w", ErrTokenUnverifiable, err)
}

// rejectedError carries a negative SMS Factor status code.
type rejectedError struct {
	code    int
	message string
}

func (e *rejectedError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", ErrRejected.Error(), e.code, e.message)
}

func (e *rejectedError) Unwrap() error { return ErrRejected }

// httpStatusError carries a non-2xx HTTP status with no usable status body.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: http status %d", ErrRequestFailed.Error(), e.code)
}

func (e *httpStatusError) Unwrap() error { return ErrRequestFailed }

// do sends one request and returns the decoded envelope when its status is
// StatusOK or StatusUnderModeration. Any other status is a *rejectedError.
func (c *Client) do(ctx context.Context, method, path string, payload any) (*apiResponse, error) {
	var reader io.Reader = http.NoBody

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding smsfactor request: %w", err)
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("creating smsfactor request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)
	// Without it the API answers in XML.
	req.Header.Set("Accept", "application/json")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = httpclientpool.NewClient(DefaultTimeout)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %w", ErrRequestFailed, err)
	}

	var decoded apiResponse
	if jsonErr := json.Unmarshal(raw, &decoded); jsonErr != nil || decoded.Status.Int() == 0 {
		if resp.StatusCode >= http.StatusMultipleChoices {
			return nil, &httpStatusError{code: resp.StatusCode}
		}

		return nil, fmt.Errorf("%w: unparseable response (http %d)", ErrRequestFailed, resp.StatusCode)
	}

	switch status := decoded.Status.Int(); status {
	case StatusOK, StatusUnderModeration:
		return &decoded, nil
	default:
		msg := decoded.Message
		if decoded.Details != "" {
			msg += " (" + decoded.Details + ")"
		}

		return nil, &rejectedError{code: status, message: msg}
	}
}

// newGSMSMSID returns a short random id SMS Factor echoes back on delivery
// receipts. Unused for now (no receipt handler), but required by the API.
func newGSMSMSID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)

	return hex.EncodeToString(buf)
}

// flexNumber decodes a JSON number or a quoted number.
type flexNumber float64

// Int returns the value truncated to an int.
func (n flexNumber) Int() int { return int(n) }

// UnmarshalJSON accepts 12, 12.5, "12" and null.
func (n *flexNumber) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		*n = 0

		return nil
	}

	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("smsfactor: invalid number %q: %w", text, err)
	}

	*n = flexNumber(value)

	return nil
}

// flexString decodes a JSON string or a bare number into a string.
type flexString string

// UnmarshalJSON accepts "14672468", 14672468 and null.
func (s *flexString) UnmarshalJSON(data []byte) error {
	text := string(data)
	if text == "null" {
		*s = ""

		return nil
	}

	if strings.HasPrefix(text, `"`) {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return fmt.Errorf("smsfactor: invalid string: %w", err)
		}

		*s = flexString(decoded)

		return nil
	}

	*s = flexString(text)

	return nil
}
