package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Errors returned by the client.
var (
	ErrNoSession        = errors.New("jmap: no session discovered")
	ErrNoMailAccount    = errors.New("jmap: no account with mail capability")
	ErrUnexpectedStatus = errors.New("jmap: unexpected HTTP status")
)

// DefaultClientTimeout is used by the underlying HTTP client for non-streaming
// calls (session discovery, Call). EventSource uses its own no-timeout client.
const DefaultClientTimeout = 30 * time.Second

// Client is a minimal JMAP client. It is safe for concurrent use after
// DiscoverSession returns; before then, only DiscoverSession should be called.
type Client struct {
	httpClient   *http.Client
	streamClient *http.Client
	username     string
	password     string
	sessionURL   string
	rewriteBase  string

	mu             sync.RWMutex
	apiURL         string
	eventSourceURL string
	downloadURL    string
	uploadURL      string
	accountID      string
	session        *Session
}

// NewClient builds a Client from cfg. The HTTP transports are stdlib defaults.
func NewClient(cfg *Config) *Client {
	return &Client{
		httpClient:   &http.Client{Timeout: DefaultClientTimeout},
		streamClient: &http.Client{}, // no timeout — used for SSE
		username:     cfg.Username,
		password:     cfg.Password,
		sessionURL:   cfg.SessionURL,
		rewriteBase:  cfg.RewriteBaseURL,
	}
}

// SetRewriteBaseURL overrides the proxy rewrite base for setups where the
// JMAP session returns internal URLs the client cannot reach directly.
func (c *Client) SetRewriteBaseURL(base string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.rewriteBase = base
}

// AccountID returns the discovered mail account ID. Empty before
// DiscoverSession returns.
func (c *Client) AccountID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.accountID
}

// EventSourceURL returns the discovered eventSourceUrl, or empty if the server
// did not advertise one.
func (c *Client) EventSourceURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.eventSourceURL
}

// Session returns the cached session document, or nil if DiscoverSession has
// not yet returned successfully.
func (c *Client) Session() *Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.session
}

// DiscoverSession fetches the JMAP session document, populates URLs and
// accountID for subsequent calls, and returns the parsed session.
func (c *Client) DiscoverSession(ctx context.Context) (*Session, error) {
	if c.sessionURL == "" {
		return nil, ErrNoSession
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sessionURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build session request: %w", err)
	}

	c.applyAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("session discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read session body: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d: %s", ErrUnexpectedStatus, resp.StatusCode, string(body))
	}

	var session Session
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}

	accountID := session.PrimaryAccounts[CapabilityMail]
	if accountID == "" {
		return nil, ErrNoMailAccount
	}

	c.mu.Lock()
	c.session = &session
	c.accountID = accountID
	c.apiURL = c.rewrite(session.APIURL)
	c.eventSourceURL = c.rewrite(session.EventSourceURL)
	c.downloadURL = c.rewrite(session.DownloadURL)
	c.uploadURL = c.rewrite(session.UploadURL)
	c.mu.Unlock()

	return &session, nil
}

// Call sends a JMAP request envelope and returns the parsed response.
// The capabilities used default to core+mail.
func (c *Client) Call(ctx context.Context, calls []MethodCall) (*Response, error) {
	c.mu.RLock()
	apiURL := c.apiURL
	c.mu.RUnlock()

	if apiURL == "" {
		return nil, ErrNoSession
	}

	envelope := Request{
		Using:       []string{CapabilityCore, CapabilityMail},
		MethodCalls: calls,
	}

	buf, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d: %s", ErrUnexpectedStatus, resp.StatusCode, string(body))
	}

	var out Response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &out, nil
}

// applyAuth sets HTTP Basic credentials on req. JMAP servers commonly accept
// either Basic or Bearer; we use Basic since the configured password may be a
// long-lived application password.
func (c *Client) applyAuth(req *http.Request) {
	if c.username == "" && c.password == "" {
		return
	}

	req.SetBasicAuth(c.username, c.password)
}

// rewrite applies the rewriteBase prefix substitution if configured. It
// preserves the path/query of the original URL.
func (c *Client) rewrite(raw string) string {
	if raw == "" || c.rewriteBase == "" {
		return raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	base, err := url.Parse(c.rewriteBase)
	if err != nil {
		return raw
	}

	parsed.Scheme = base.Scheme
	parsed.Host = base.Host

	if base.Path != "" && base.Path != "/" {
		parsed.Path = strings.TrimRight(base.Path, "/") + parsed.Path
	}

	return parsed.String()
}
