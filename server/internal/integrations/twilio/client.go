// Package twilio is a minimal Twilio REST client for sending SMS and placing
// voice calls, plus request-signature validation for inbound callbacks. It has
// no dependencies beyond the standard library so it can be imported from both
// the notification senders and the HTTP handlers without an import cycle,
// mirroring the internal/integrations/slack client seam.
package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // Twilio mandates HMAC-SHA1 for X-Twilio-Signature validation.
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
	// ErrCredentialsRejected is returned by VerifyCredentials when Twilio
	// itself refuses the account SID / auth token pair (401/403) against the
	// resolved regional base — the credentials are simply wrong for that
	// region.
	ErrCredentialsRejected = errors.New("twilio rejected these credentials")
	// ErrCredentialsUnverifiable is returned by VerifyCredentials when Twilio
	// could not be reached, timed out, or returned an unexpected status — the
	// credentials might be fine, but we could not prove it, so the caller
	// must refuse the write rather than persist an unverified connection.
	ErrCredentialsUnverifiable = errors.New("twilio credentials could not be verified")
)

// e164Pattern validates E.164 phone numbers: a leading '+', a non-zero first
// digit, then 6-14 more digits (7-15 total).
var e164Pattern = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// regionPattern validates a Twilio region token: two lowercase letters
// followed by one or more digits (us1, ie1, au1, ...). This is deliberately
// not an enumerated allowlist — Twilio can add regions without a SolidPing
// code change — but the shape is load-bearing: BaseURLForRegion splices the
// region straight into a URL host, so a region that isn't provably just
// "letters+digits" (no '.', '/', ':') could otherwise redirect an outbound
// request to an arbitrary host (SSRF). Keep this the only gate — every
// region that reaches BaseURLForRegion must have passed through here first.
var regionPattern = regexp.MustCompile(`^[a-z]{2}[0-9]+$`)

// ValidE164 reports whether s is a syntactically valid E.164 phone number.
func ValidE164(s string) bool {
	return e164Pattern.MatchString(s)
}

// ValidRegion reports whether region is acceptable to store: empty (meaning
// the default US1 region) or a well-formed region token. See regionPattern
// for why the format check matters beyond cosmetics.
func ValidRegion(region string) bool {
	return region == "" || regionPattern.MatchString(region)
}

// BaseURLForRegion resolves a Twilio region token to its API base URL. ""
// and "us1" both map to DefaultBaseURL (the global/US1 edge, which has no
// "us1." host prefix); every other region maps to
// https://api.<region>.twilio.com. Callers must only ever pass a region that
// has already passed ValidRegion — this function does not itself validate,
// so an unvalidated caller reintroduces the SSRF risk regionPattern exists to
// close.
func BaseURLForRegion(region string) string {
	if region == "" || region == "us1" {
		return DefaultBaseURL
	}

	return "https://api." + region + ".twilio.com"
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
	Code    int    `json:"code"`
	Message string `json:"message"`
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
func (c *Client) SendSMS(ctx context.Context, params *SendSMSParams) (*Result, error) {
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

// VerifyCredentials makes one side-effect-free authenticated call —
// GET /2010-04-01/Accounts/{accountSID}.json — against baseURL to confirm the
// account SID / auth token pair is valid there. Twilio credentials are
// region-scoped, so this must be called against the same regional base the
// account was provisioned in; verifying against the wrong region's host will
// spuriously reject valid credentials.
//
// Returns nil when Twilio accepts the credentials, ErrCredentialsRejected
// when Twilio explicitly refuses them (401/403), and
// ErrCredentialsUnverifiable for anything else (network error, timeout,
// unexpected status) — the two are kept distinct so a caller can tell "these
// credentials are wrong" from "we couldn't check right now".
func VerifyCredentials(ctx context.Context, accountSID, authToken, baseURL string) error {
	endpoint := fmt.Sprintf("%s/%s/Accounts/%s.json", strings.TrimRight(baseURL, "/"), apiVersion, accountSID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: creating verification request: %w", ErrCredentialsUnverifiable, err)
	}

	req.SetBasicAuth(accountSID, authToken)

	client := &http.Client{Timeout: DefaultTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCredentialsUnverifiable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: status %d", ErrCredentialsRejected, resp.StatusCode)
	default:
		return fmt.Errorf("%w: unexpected status %d", ErrCredentialsUnverifiable, resp.StatusCode)
	}
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
	var builder strings.Builder
	builder.WriteString(fullURL)

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range params[k] {
			builder.WriteString(k)
			builder.WriteString(v)
		}
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(builder.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}
