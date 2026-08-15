// Package ovhsms is a minimal OVHcloud SMS API client. It is hand-rolled on
// the standard library only, exactly like internal/integrations/twilio, so it
// can be imported from both the notification senders and the HTTP handlers
// without an import cycle and without pulling an SDK into the build.
package ovhsms

import (
	"bytes"
	"context"
	"crypto/sha1" // OVH mandates SHA1 for its X-Ovh-Signature scheme.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OVH API endpoint identifiers accepted by SP_SMS_OVH_ENDPOINT.
const (
	EndpointEU = "ovh-eu"
	EndpointCA = "ovh-ca"
	EndpointUS = "ovh-us"
)

// DefaultTimeout is the default HTTP client timeout.
const DefaultTimeout = 30 * time.Second

// DefaultEndpoint is the endpoint assumed when none is configured. OVH's SMS
// product is European-first and the EU endpoint is the one an operator gets by
// default when they create an application on ovh.com.
const DefaultEndpoint = EndpointEU

var (
	// ErrRequestFailed is returned when an OVH API request returns a non-2xx
	// status.
	ErrRequestFailed = errors.New("ovh sms request failed")
	// ErrUnknownEndpoint is returned for an endpoint token that is not one of
	// ovh-eu / ovh-ca / ovh-us. Returned at construction so an operator gets a
	// clear startup error rather than a runtime failure on the first incident.
	ErrUnknownEndpoint = errors.New("ovh: unknown api endpoint")
	// ErrMissingCredentials is returned when the application key, application
	// secret, consumer key or service name is empty.
	ErrMissingCredentials = errors.New("ovh: application key, secret, consumer key and service name are required")
	// ErrNoReceiverAccepted is returned when OVH accepted the request but
	// reported every receiver as invalid — an HTTP 200 that sent nothing.
	ErrNoReceiverAccepted = errors.New("ovh: no valid receiver in sending report")
	// ErrCredentialsRejected is returned by VerifyCredentials when OVH itself
	// refuses the credential triplet (401/403).
	ErrCredentialsRejected = errors.New("ovh rejected these credentials")
	// ErrCredentialsUnverifiable is returned by VerifyCredentials when OVH
	// could not be reached, timed out, or returned an unexpected status — the
	// credentials might be fine, but we could not prove it, so the caller must
	// refuse the write rather than persist an unverified connection.
	ErrCredentialsUnverifiable = errors.New("ovh credentials could not be verified")
)

// endpointBaseURLs maps an endpoint token to its API v1 base URL. Rebuilt per
// call rather than kept in a package var to satisfy gochecknoglobals.
func endpointBaseURLs() map[string]string {
	return map[string]string{
		EndpointEU: "https://eu.api.ovh.com/1.0",
		EndpointCA: "https://ca.api.ovh.com/1.0",
		EndpointUS: "https://api.us.ovhcloud.com/1.0",
	}
}

// ValidEndpoint reports whether endpoint is acceptable to store: empty
// (meaning DefaultEndpoint) or one of the three known OVH API regions. Mirrors
// twilio.ValidRegion so the operator gets a startup error rather than a
// runtime failure on the first incident.
func ValidEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}

	_, ok := endpointBaseURLs()[endpoint]

	return ok
}

// BaseURLForEndpoint resolves an endpoint token to its API base URL. Callers
// must only ever pass a token that has already passed ValidEndpoint; an
// unknown token yields the EU base rather than splicing attacker-controlled
// text into a URL host.
func BaseURLForEndpoint(endpoint string) string {
	if base, ok := endpointBaseURLs()[endpoint]; ok {
		return base
	}

	return endpointBaseURLs()[DefaultEndpoint]
}

// Config carries everything a Client needs.
type Config struct {
	// Endpoint is ovh-eu / ovh-ca / ovh-us. Ignored when BaseURL is set.
	Endpoint string
	// BaseURL overrides the endpoint-derived API base (tests, egress proxy).
	BaseURL string
	// ApplicationKey / ApplicationSecret / ConsumerKey are the OVH credential
	// triplet. The last two are secrets.
	ApplicationKey    string
	ApplicationSecret string
	ConsumerKey       string
	// ServiceName is the sms-xxxxx-1 account jobs are posted to.
	ServiceName string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
	// Now overrides the local clock (tests).
	Now func() time.Time
}

// Client is an OVHcloud SMS API client scoped to one SMS account.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	appKey      string
	appSecret   string
	consumerKey string
	serviceName string
	now         func() time.Time

	// skewMu guards the cached OVH-server-time delta. OVH computes the
	// signature against ITS OWN clock, so a pod or laptop a few seconds off
	// gets a hard auth failure that reads exactly like a bad credential. The
	// official SDKs fetch /auth/time once and keep the difference; so do we.
	skewMu      sync.Mutex
	skewFetched bool
	skew        time.Duration
}

// NewClient builds a client and immediately synchronizes the OVH server-time
// delta. A failure to reach /auth/time is NOT fatal — the delta simply stays
// zero and is retried on the next send — but an unknown endpoint or a missing
// credential is, because those can never start working on their own.
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	if !ValidEndpoint(cfg.Endpoint) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEndpoint, cfg.Endpoint)
	}

	if strings.TrimSpace(cfg.ApplicationKey) == "" ||
		strings.TrimSpace(cfg.ApplicationSecret) == "" ||
		strings.TrimSpace(cfg.ConsumerKey) == "" ||
		strings.TrimSpace(cfg.ServiceName) == "" {
		return nil, ErrMissingCredentials
	}

	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = BaseURLForEndpoint(cfg.Endpoint)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	client := &Client{
		httpClient:  httpClient,
		baseURL:     base,
		appKey:      cfg.ApplicationKey,
		appSecret:   cfg.ApplicationSecret,
		consumerKey: cfg.ConsumerKey,
		serviceName: cfg.ServiceName,
		now:         nowFn,
	}

	// Best-effort: prime the delta at construction so the first real send is
	// never the one that discovers the clock is off.
	_ = client.syncTime(ctx)

	return client, nil
}

// SendParams are the inputs to Send.
type SendParams struct {
	// Receivers are E.164 destination numbers.
	Receivers []string
	// Message is the SMS body.
	Message string
	// Sender is the pre-registered alphanumeric sender or short number.
	Sender string
}

// SendingReport is the sms.SmsSendingReportUser payload OVH returns.
type SendingReport struct {
	CreditsLeft         float64  `json:"creditsLeft"`
	IDs                 []int64  `json:"ids"`
	InvalidReceivers    []string `json:"invalidReceivers"`
	TotalCreditsRemoved float64  `json:"totalCreditsRemoved"`
	ValidReceivers      []string `json:"validReceivers"`
}

// jobRequest is the POST /sms/{serviceName}/jobs body.
//
// The field set is the one the live OVH schema declares. Note what is NOT
// here: there is no per-job delivery-receipt callback field — the callback URL
// is a service-level setting (`callBack` on the SMS account), which is why the
// DLR endpoint authenticates with an instance token rather than a per-message
// one.
type jobRequest struct {
	Charset string `json:"charset"`
	Message string `json:"message"`
	// NoStopClause MUST always be true for our traffic. Without it OVH appends
	// a "STOP" clause that consumes characters — possibly pushing the message
	// into a second billed segment — and is legally reserved for marketing,
	// which transactional alerting is not. It is a plain bool rather than a
	// parameter precisely so no caller can regress it.
	NoStopClause bool     `json:"noStopClause"`
	Priority     string   `json:"priority"`
	Receivers    []string `json:"receivers"`
	Sender       string   `json:"sender"`
}

// Send posts one SMS job. It always sets noStopClause=true; see jobRequest.
func (c *Client) Send(ctx context.Context, params *SendParams) (*SendingReport, error) {
	body := jobRequest{
		Charset:      "UTF-8",
		Message:      params.Message,
		NoStopClause: true,
		Priority:     "high",
		Receivers:    params.Receivers,
		Sender:       params.Sender,
	}

	var report SendingReport
	if err := c.do(ctx, http.MethodPost, "/sms/"+c.serviceName+"/jobs", body, &report); err != nil {
		return nil, err
	}

	if len(report.ValidReceivers) == 0 && len(report.IDs) == 0 {
		return nil, fmt.Errorf("%w: invalid receivers %v", ErrNoReceiverAccepted, report.InvalidReceivers)
	}

	return &report, nil
}

// SetDeliveryCallback points the SMS account's service-level delivery-receipt
// callback at url. OVH has no per-job callback field, so this is the only way
// to receive delivery receipts; an operator can equally set it in the OVH
// control panel, which is why a failure here is reported rather than fatal.
func (c *Client) SetDeliveryCallback(ctx context.Context, callbackURL string) error {
	return c.do(ctx, http.MethodPut, "/sms/"+c.serviceName,
		map[string]string{"callBack": callbackURL}, nil)
}

// VerifyCredentials makes one side-effect-free authenticated call —
// GET /sms/{serviceName} — to confirm the credential triplet works for that
// account. Mirrors twilio.VerifyCredentials, including the deliberate split
// between "these credentials are wrong" (ErrCredentialsRejected) and "we
// could not check right now" (ErrCredentialsUnverifiable).
func (c *Client) VerifyCredentials(ctx context.Context) error {
	err := c.do(ctx, http.MethodGet, "/sms/"+c.serviceName, nil, nil)
	if err == nil {
		return nil
	}

	var statusErr *statusError
	if errors.As(err, &statusErr) {
		if statusErr.code == http.StatusUnauthorized || statusErr.code == http.StatusForbidden {
			return fmt.Errorf("%w: status %d", ErrCredentialsRejected, statusErr.code)
		}
	}

	return fmt.Errorf("%w: %w", ErrCredentialsUnverifiable, err)
}

// statusError carries the HTTP status of a failed OVH call so VerifyCredentials
// can distinguish a refusal from an outage.
type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", ErrRequestFailed.Error(), e.code, e.message)
}

func (e *statusError) Unwrap() error { return ErrRequestFailed }

type apiError struct {
	Message string `json:"message"`
	Class   string `json:"class"`
}

func (c *Client) do(ctx context.Context, method, path string, payload, out any) error {
	var bodyBytes []byte

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding ovh request: %w", err)
		}

		bodyBytes = encoded
	}

	endpoint := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating ovh request: %w", err)
	}

	timestamp := c.ovhTimestamp(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ovh-Application", c.appKey)
	req.Header.Set("X-Ovh-Consumer", c.consumerKey)
	req.Header.Set("X-Ovh-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-Ovh-Signature",
		Signature(c.appSecret, c.consumerKey, method, endpoint, string(bodyBytes), timestamp))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending ovh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(respBody, &apiErr)

		return &statusError{code: resp.StatusCode, message: apiErr.Message}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("parsing ovh response: %w", err)
		}
	}

	return nil
}

// Signature computes the OVH request signature:
//
//	"$1$" + SHA1(secret + "+" + consumerKey + "+" + METHOD + "+" + URL + "+" + body + "+" + timestamp)
//
// Exported so the scheme can be tested against known-good vectors independently
// of any HTTP round trip.
func Signature(appSecret, consumerKey, method, url, body string, timestamp int64) string {
	hasher := sha1.New() //nolint:gosec // OVH mandates SHA1 for this scheme.
	hasher.Write([]byte(strings.Join([]string{
		appSecret, consumerKey, method, url, body, strconv.FormatInt(timestamp, 10),
	}, "+")))

	return "$1$" + hex.EncodeToString(hasher.Sum(nil))
}

// ovhTimestamp returns the current time expressed on OVH's clock: the local
// clock plus the cached delta. When the delta has never been fetched
// successfully it is retried here, so a transient failure at construction
// heals on the next request rather than poisoning every send.
func (c *Client) ovhTimestamp(ctx context.Context) int64 {
	c.skewMu.Lock()
	fetched, skew := c.skewFetched, c.skew
	c.skewMu.Unlock()

	if !fetched {
		if err := c.syncTime(ctx); err == nil {
			c.skewMu.Lock()
			skew = c.skew
			c.skewMu.Unlock()
		}
	}

	return c.now().Add(skew).Unix()
}

// syncTime fetches /auth/time (unauthenticated, returns a bare unix second)
// and caches the difference against the local clock.
func (c *Client) syncTime(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/auth/time", http.NoBody)
	if err != nil {
		return fmt.Errorf("creating ovh time request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching ovh server time: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return fmt.Errorf("reading ovh server time: %w", err)
	}

	if resp.StatusCode >= 300 {
		return &statusError{code: resp.StatusCode, message: "auth/time"}
	}

	serverUnix, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return fmt.Errorf("parsing ovh server time %q: %w", string(body), err)
	}

	c.skewMu.Lock()
	c.skew = time.Unix(serverUnix, 0).Sub(c.now())
	c.skewFetched = true
	c.skewMu.Unlock()

	return nil
}
