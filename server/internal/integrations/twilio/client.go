// Package twilio is a minimal Twilio REST client for sending SMS and placing
// voice calls, plus request-signature validation for inbound callbacks. It has
// no dependencies beyond the standard library so it can be imported from both
// the notification senders and the HTTP handlers without an import cycle,
// mirroring the internal/integrations/slack client seam.
package twilio

import (
	"context"
	"crypto/hmac"
	//nolint:gosec // Twilio mandates HMAC-SHA1 for X-Twilio-Signature validation.
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the real Twilio REST API base.
	DefaultBaseURL = "https://api.twilio.com"
	// apiVersion is the Twilio REST API version segment.
	apiVersion = "2010-04-01"
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
)

var (
	// ErrRequestFailed is returned when a Twilio API request returns a non-2xx
	// status.
	ErrRequestFailed = errors.New("twilio request failed")
	// ErrMissingSender is returned when neither a from-number nor a messaging
	// service SID is provided for an SMS.
	ErrMissingSender = errors.New("twilio: from_number or messaging_service_sid required")
)

// e164Pattern validates E.164 phone numbers: a leading '+', a non-zero first
// digit, then 6-14 more digits (7-15 total).
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// ValidE164 reports whether s is a syntactically valid E.164 phone number.
func ValidE164(s string) bool {
	return e164Pattern.MatchString(s)
}

// ValidAccountSID reports whether s looks like a Twilio Account SID (AC + 32
// hex chars). We only check the AC prefix and length loosely — Twilio is the
// authority, this just catches obvious paste errors.
func ValidAccountSID(s string) bool {
	return strings.HasPrefix(s, "AC") && len(s) == 34
}

// Client is a Twilio REST API client scoped to one account.
type Client struct {
	httpClient *http.Client
	accountSID string
	authToken  string
	baseURL    string
}

// NewClient creates a Twilio client targeting the real Twilio API.
func NewClient(accountSID, authToken string) *Client {
	return NewClientWithBaseURL(accountSID, authToken, DefaultBaseURL)
}

// NewClientWithBaseURL creates a Twilio client targeting a custom API base URL.
// Intended for tests that point the client at an httptest fake server.
func NewClientWithBaseURL(accountSID, authToken, baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: DefaultTimeout},
		accountSID: accountSID,
		authToken:  authToken,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Result carries the identifying fields of a Twilio Message or Call resource.
type Result struct {
	SID    string `json:"sid"`
	Status string `json:"status"`
}

type apiError struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	MoreInfo string `json:"more_info"`
}

// SendSMSParams are the inputs to SendSMS. Exactly one of From /
// MessagingServiceSID must be set.
type SendSMSParams struct {
	To                  string
	From                string
	MessagingServiceSID string
	Body                string
	StatusCallback      string
}

// SendSMS sends one SMS message via Twilio's Messages resource.
func (c *Client) SendSMS(ctx context.Context, params SendSMSParams) (*Result, error) {
	data := url.Values{}
	data.Set("To", params.To)
	switch {
	case params.MessagingServiceSID != "":
		data.Set("MessagingServiceSid", params.MessagingServiceSID)
	case params.From != "":
		data.Set("From", params.From)
	default:
		return nil, ErrMissingSender
	}

	data.Set("Body", params.Body)
	if params.StatusCallback != "" {
		data.Set("StatusCallback", params.StatusCallback)
	}

	var res Result
	if err := c.post(ctx, "Messages.json", data, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// CreateCallParams are the inputs to CreateCall.
type CreateCallParams struct {
	To             string
	From           string
	TwiMLURL       string
	StatusCallback string
}

// CreateCall places an outbound voice call that fetches TwiML from TwiMLURL.
func (c *Client) CreateCall(ctx context.Context, params CreateCallParams) (*Result, error) {
	data := url.Values{}
	data.Set("To", params.To)
	data.Set("From", params.From)
	data.Set("Url", params.TwiMLURL)
	if params.StatusCallback != "" {
		data.Set("StatusCallback", params.StatusCallback)
	}

	var res Result
	if err := c.post(ctx, "Calls.json", data, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

func (c *Client) post(ctx context.Context, resource string, data url.Values, out any) error {
	endpoint := fmt.Sprintf("%s/%s/Accounts/%s/%s", c.baseURL, apiVersion, c.accountSID, resource)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("creating twilio request: %w", err)
	}

	req.SetBasicAuth(c.accountSID, c.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending twilio request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)

		return fmt.Errorf("%w: status %d: %s", ErrRequestFailed, resp.StatusCode, apiErr.Message)
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("parsing twilio response: %w", err)
		}
	}

	return nil
}

// ValidateSignature reports whether the X-Twilio-Signature header value matches
// the expected HMAC-SHA1 over the full request URL plus the POST parameters
// (sorted by name, each key immediately followed by its value), signed with the
// account auth token. This is Twilio's documented webhook-signature scheme.
func ValidateSignature(authToken, fullURL string, params url.Values, signature string) bool {
	var b strings.Builder
	b.WriteString(fullURL)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range params[k] {
			b.WriteString(k)
			b.WriteString(v)
		}
	}

	//nolint:gosec // Twilio mandates HMAC-SHA1 for signature validation.
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}
