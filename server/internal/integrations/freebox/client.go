package freebox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Sentinel errors callers can branch on. Each one maps to a known
// Freebox API failure mode so the channel handler can return clean
// validation messages instead of leaking raw HTTP errors.
var (
	// ErrAuthRequired is returned when the Freebox refuses the current
	// session token. The client transparently re-opens a session and
	// retries once before surfacing this — so seeing it means the
	// app_token itself is no longer accepted (typically: revoked from
	// the Freebox admin).
	ErrAuthRequired = errors.New("freebox: auth required (app_token revoked or invalid)")
	// ErrPairingTimeout is returned when the pairing track expired
	// before the user pressed the LCD arrow.
	ErrPairingTimeout = errors.New("freebox: pairing timeout")
	// ErrPairingDenied is returned when the user rejected the pairing.
	ErrPairingDenied = errors.New("freebox: pairing denied by user")
	// ErrUnexpectedStatus is returned for any non-2xx HTTP response not
	// otherwise classified.
	ErrUnexpectedStatus = errors.New("freebox: unexpected HTTP status")
	// ErrAPI is returned when the Freebox envelope reports success=false
	// with no more specific mapping.
	ErrAPI = errors.New("freebox: api error")
	// ErrFreeboxNotGranted is returned when an authenticated call targets a
	// Freebox channel that hasn't completed the LCD-pairing flow yet (no
	// app_token, or status != granted). Callers map it to a 409 so the UI
	// can prompt the operator to finish pairing. Exported from the freebox
	// package so both the channels handler and the discovery job can branch
	// on it without importing each other.
	ErrFreeboxNotGranted = errors.New("freebox: channel is not paired yet")
)

const (
	apiV4Prefix = "/api/v4"

	// DefaultTimeout is the per-request HTTP timeout. Freebox endpoints
	// are local-LAN and usually respond in milliseconds; 30 s leaves
	// plenty of headroom for slow remote-access HTTPS round-trips.
	DefaultTimeout = 30 * time.Second
)

// Client is a minimal Freebox OS API client. It is safe for concurrent
// use: session refreshes are serialized by sessionMu, and the underlying
// http.Client is goroutine-safe by design.
//
// Higher-level packages (checkers, the channel handler) should not
// construct Client directly — go through the service.go helpers, which
// know how to wire the encrypted app_token from the Channel record.
type Client struct {
	baseURL    string
	appID      string
	appToken   string
	httpClient *http.Client

	sessionMu sync.Mutex
	session   string
}

// NewClient builds a client for a given Freebox + app_token pair. The
// app_token is the permanent secret stored in IntegrationConnection
// settings_private; the client never persists or logs it.
func NewClient(baseURL, appToken string) *Client {
	return NewClientWithAppID(baseURL, DefaultAppID, appToken)
}

// NewClientWithAppID lets callers override the app_id used when opening
// a session. Most callers should use NewClient; only the pairing flow
// needs an explicit app_id (which must match the one used at authorize
// time).
func NewClientWithAppID(baseURL, appID, appToken string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	if appID == "" {
		appID = DefaultAppID
	}

	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		appID:    appID,
		appToken: appToken,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// Authorize starts a pairing flow. The Freebox LCD will display a
// pairing prompt; the user must press the right-arrow key within ~30 s.
//
// The returned AppToken must be persisted immediately, encrypted — even
// before the LCD approval — because the same token will be activated
// once approval succeeds, and re-running Authorize would orphan the
// previous prompt.
func (c *Client) Authorize(ctx context.Context, deviceName string) (*AuthorizeResult, error) {
	if deviceName == "" {
		deviceName = DefaultDeviceName
	}

	body, err := json.Marshal(AuthorizeRequest{
		AppID:      c.appID,
		AppName:    DefaultAppName,
		AppVersion: DefaultAppVersion,
		DeviceName: deviceName,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal authorize request: %w", err)
	}

	envelope, err := c.do(ctx, http.MethodPost, apiV4Prefix+"/login/authorize/", body, false)
	if err != nil {
		return nil, err
	}

	var out AuthorizeResult
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		return nil, fmt.Errorf("decode authorize result: %w", err)
	}

	return &out, nil
}

// PollPairing returns the current pairing status. Callers should poll
// every ~2 s; the Freebox does not push.
//
// Returns one of StatusUnknown / StatusPending / StatusGranted /
// StatusDenied / StatusTimeout. The caller is responsible for stopping
// the poll loop on any terminal status.
func (c *Client) PollPairing(ctx context.Context, trackID int) (string, error) {
	path := fmt.Sprintf("%s/login/authorize/%d", apiV4Prefix, trackID)

	envelope, err := c.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return "", err
	}

	var out PairingStatus
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		return "", fmt.Errorf("decode pairing status: %w", err)
	}

	return out.Status, nil
}

// Get performs an authenticated GET against the given path (e.g.
// "/api/v4/connection/") and unmarshals the `result` field into `out`.
// Session expiry is handled transparently: on auth_required the client
// re-opens a session and retries once before surfacing the error.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	envelope, err := c.do(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return err
	}

	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decode response from %s: %w", path, err)
		}
	}

	return nil
}

// do is the inner request helper. When authenticated is true it injects
// the current session token (refreshing it as needed) and retries once
// on auth_required.
func (c *Client) do(
	ctx context.Context, method, path string, body []byte, authenticated bool,
) (*APIResponse, error) {
	envelope, err := c.doOnce(ctx, method, path, body, authenticated)
	if err == nil {
		return envelope, nil
	}

	if !authenticated || !errors.Is(err, ErrAuthRequired) {
		return nil, err
	}

	// Transparent retry: drop the cached session and try one more time.
	c.sessionMu.Lock()
	c.session = ""
	c.sessionMu.Unlock()

	return c.doOnce(ctx, method, path, body, authenticated)
}

func (c *Client) doOnce(
	ctx context.Context, method, path string, body []byte, authenticated bool,
) (*APIResponse, error) {
	var session string

	if authenticated {
		s, err := c.ensureSession(ctx)
		if err != nil {
			return nil, err
		}

		session = s
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if session != "" {
		req.Header.Set("X-Fbx-App-Auth", session)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", path, err)
	}

	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf(
			"%w: %d %s — %s",
			ErrUnexpectedStatus, resp.StatusCode, http.StatusText(resp.StatusCode), truncate(respBody, 200),
		)
	}

	envelope := &APIResponse{}
	if err := json.Unmarshal(respBody, envelope); err != nil {
		return nil, fmt.Errorf(
			"decode envelope from %s (status %d): %w (body: %s)",
			path, resp.StatusCode, err, truncate(respBody, 200),
		)
	}

	if !envelope.Success {
		return envelope, classifyError(envelope)
	}

	return envelope, nil
}

func classifyError(env *APIResponse) error {
	switch env.ErrorCode {
	case "auth_required", "invalid_token":
		return fmt.Errorf("%w (%s)", ErrAuthRequired, env.Msg)
	case "pending_token":
		// We treat pending_token as success at the envelope layer —
		// callers should be polling explicitly via PollPairing.
		return fmt.Errorf("%w: %s (%s)", ErrAPI, env.Msg, env.ErrorCode)
	default:
		if env.ErrorCode == "" {
			return fmt.Errorf("%w: %s", ErrAPI, env.Msg)
		}

		return fmt.Errorf("%w: %s (%s)", ErrAPI, env.Msg, env.ErrorCode)
	}
}

func truncate(b []byte, limit int) string {
	if len(b) <= limit {
		return string(b)
	}

	return string(b[:limit]) + "…"
}

// ensureSession returns the current session token, opening a new one
// when none is cached. Concurrent callers serializethrough sessionMu —
// at most one /login/session/ round-trip in flight at a time.
func (c *Client) ensureSession(ctx context.Context) (string, error) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()

	if c.session != "" {
		return c.session, nil
	}

	if c.appToken == "" {
		return "", fmt.Errorf("%w: empty app_token", ErrAuthRequired)
	}

	challenge, err := c.fetchChallenge(ctx)
	if err != nil {
		return "", err
	}

	password := SignChallenge(c.appToken, challenge)

	body, err := json.Marshal(SessionRequest{AppID: c.appID, Password: password})
	if err != nil {
		return "", fmt.Errorf("marshal session request: %w", err)
	}

	envelope, err := c.doOnce(ctx, http.MethodPost, apiV4Prefix+"/login/session/", body, false)
	if err != nil {
		return "", err
	}

	var out SessionResult
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		return "", fmt.Errorf("decode session result: %w", err)
	}

	if out.SessionToken == "" {
		return "", fmt.Errorf("%w: empty session_token in response", ErrAPI)
	}

	c.session = out.SessionToken

	return c.session, nil
}

func (c *Client) fetchChallenge(ctx context.Context) (string, error) {
	envelope, err := c.doOnce(ctx, http.MethodGet, apiV4Prefix+"/login/", nil, false)
	if err != nil {
		return "", err
	}

	var out LoginChallenge
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		return "", fmt.Errorf("decode login challenge: %w", err)
	}

	if out.Challenge == "" {
		return "", fmt.Errorf("%w: empty challenge", ErrAPI)
	}

	return out.Challenge, nil
}

// SignChallenge computes the HMAC-SHA1 of `challenge` using `appToken`
// as the key and returns the lowercase hex digest. This is the exact
// form the Freebox session endpoint expects as the `password` field.
//
// It is exported so the pairing tests can assert correctness without
// reaching into the unexported helper.
func SignChallenge(appToken, challenge string) string {
	mac := hmac.New(sha1.New, []byte(appToken))
	_, _ = mac.Write([]byte(challenge))

	return hex.EncodeToString(mac.Sum(nil))
}
