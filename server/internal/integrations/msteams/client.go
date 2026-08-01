package msteams

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

const (
	// DefaultTokenURL is the Bot Framework client-credentials token endpoint.
	// The `botframework.com` tenant segment is not a typo: bot credentials
	// are always minted against Microsoft's own tenant, whatever tenant the
	// bot is talking to.
	DefaultTokenURL = "https://login.microsoftonline.com/botframework.com/oauth2/v2.0/token"
	// BotFrameworkScope is the scope requested for connector calls.
	BotFrameworkScope = "https://api.botframework.com/.default"
	// DefaultTimeout bounds every Bot Connector HTTP call.
	DefaultTimeout = 30 * time.Second
	// tokenRenewSkew renews a token slightly before it actually expires so a
	// send never races the expiry.
	tokenRenewSkew = 2 * time.Minute
)

var (
	// ErrBotConnector is returned when a Bot Connector call fails.
	ErrBotConnector = errors.New("bot connector API error")
	// ErrTokenRequest is returned when the client-credentials exchange fails.
	ErrTokenRequest = errors.New("bot framework token request failed")
	// ErrMissingCredentials is returned when the app id/secret are unset.
	ErrMissingCredentials = errors.New("microsoft teams app credentials are not configured")
	// ErrMissingServiceURL is returned when no service URL is known for a
	// conversation, so there is no connector endpoint to call.
	ErrMissingServiceURL = errors.New("no service URL for this conversation")
)

// tokenEntry is a cached client-credentials token.
type tokenEntry struct {
	token   string
	expires time.Time
}

// tokenCache memoizes Bot Framework tokens across clients. Tokens are
// per-app-id (not per-conversation) and valid for ~1h, so caching them here
// keeps a burst of notifications from minting one token per message.
//
//nolint:gochecknoglobals // process-wide token cache, intentionally shared
var tokenCache = struct {
	mu      sync.Mutex
	entries map[string]tokenEntry
}{entries: map[string]tokenEntry{}}

// Client talks to the Bot Connector REST API for one service URL.
//
// serviceURL is per-tenant and regional (e.g.
// https://smba.trafficmanager.net/emea/); it comes from the inbound activity
// and is stored on the connection, never hard-coded.
type Client struct {
	httpClient *http.Client
	appID      string
	appSecret  string
	serviceURL string
	tokenURL   string
}

// NewClient creates a Bot Connector client for a service URL.
func NewClient(appID, appSecret, serviceURL string) *Client {
	return NewClientWithTokenURL(appID, appSecret, serviceURL, DefaultTokenURL)
}

// NewClientWithTokenURL creates a client with a custom token endpoint.
// Intended for tests pointing at an httptest fake connector.
func NewClientWithTokenURL(appID, appSecret, serviceURL, tokenURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: DefaultTimeout},
		appID:      appID,
		appSecret:  appSecret,
		serviceURL: normalizeServiceURL(serviceURL),
		tokenURL:   tokenURL,
	}
}

// ServiceURL returns the connector base URL this client targets.
func (c *Client) ServiceURL() string { return c.serviceURL }

// token returns a valid bearer token, minting one when the cache is cold or
// close to expiry.
func (c *Client) token(ctx context.Context) (string, error) {
	if c.appID == "" || c.appSecret == "" {
		return "", ErrMissingCredentials
	}

	cacheKey := c.tokenURL + "|" + c.appID

	tokenCache.mu.Lock()
	entry, ok := tokenCache.entries[cacheKey]
	tokenCache.mu.Unlock()

	if ok && time.Now().Before(entry.expires.Add(-tokenRenewSkew)) {
		return entry.token, nil
	}

	fresh, expiresIn, err := c.requestToken(ctx)
	if err != nil {
		return "", err
	}

	tokenCache.mu.Lock()
	tokenCache.entries[cacheKey] = tokenEntry{
		token:   fresh,
		expires: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	tokenCache.mu.Unlock()

	return fresh, nil
}

// tokenResponse is the client-credentials response shape.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// requestToken performs the client-credentials exchange.
func (c *Client) requestToken(ctx context.Context) (string, int, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.appID)
	form.Set("client_secret", c.appSecret)
	form.Set("scope", BotFrameworkScope)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrTokenRequest, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrTokenRequest, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrTokenRequest, err)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrTokenRequest, err)
	}

	if resp.StatusCode != http.StatusOK || tokenResp.AccessToken == "" {
		return "", 0, fmt.Errorf("%w: status %d: %s %s",
			ErrTokenRequest, resp.StatusCode, tokenResp.Error, tokenResp.Description)
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return tokenResp.AccessToken, expiresIn, nil
}

// ResourceResponse is the Bot Connector's reply to a send/update: the id of
// the activity that was created or modified.
type ResourceResponse struct {
	ID string `json:"id"`
}

// SendToConversation posts a new activity into a conversation
// (`POST /v3/conversations/{conversationId}/activities`). This is the
// proactive-message path used by the notification sender — the Bot Framework
// name for what the spec calls `continueConversation`.
func (c *Client) SendToConversation(
	ctx context.Context, conversationID string, activity *Activity,
) (*ResourceResponse, error) {
	path := fmt.Sprintf("/v3/conversations/%s/activities", url.PathEscape(conversationID))

	return c.do(ctx, http.MethodPost, path, activity)
}

// ReplyToActivity posts an activity as a reply to an existing one
// (`POST /v3/conversations/{conversationId}/activities/{activityId}`). In
// Teams this threads the reply under the original message, giving the
// Slack-thread-like grouping the spec asks for on resolve/reopen.
func (c *Client) ReplyToActivity(
	ctx context.Context, conversationID, activityID string, activity *Activity,
) (*ResourceResponse, error) {
	path := fmt.Sprintf("/v3/conversations/%s/activities/%s",
		url.PathEscape(conversationID), url.PathEscape(activityID))

	if activity.ReplyToID == "" {
		activity.ReplyToID = activityID
	}

	return c.do(ctx, http.MethodPost, path, activity)
}

// UpdateActivity replaces an activity in place
// (`PUT /v3/conversations/{conversationId}/activities/{activityId}`), which
// is how an incident card is escalated or flipped to resolved without
// spamming the channel with a second card.
func (c *Client) UpdateActivity(
	ctx context.Context, conversationID, activityID string, activity *Activity,
) (*ResourceResponse, error) {
	path := fmt.Sprintf("/v3/conversations/%s/activities/%s",
		url.PathEscape(conversationID), url.PathEscape(activityID))

	activity.ID = activityID

	return c.do(ctx, http.MethodPut, path, activity)
}

// do performs an authenticated Bot Connector call.
func (c *Client) do(ctx context.Context, method, path string, activity *Activity) (*ResourceResponse, error) {
	if c.serviceURL == "" {
		return nil, ErrMissingServiceURL
	}

	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(activity)
	if err != nil {
		return nil, fmt.Errorf("marshaling activity: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.serviceURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating bot connector request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerPrefix+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling bot connector: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading bot connector response: %w", err)
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: status %d: %s", ErrBotConnector, resp.StatusCode, string(body))
	}

	// 202 Accepted (and some update responses) carry no body.
	if len(bytes.TrimSpace(body)) == 0 {
		return &ResourceResponse{}, nil
	}

	var result ResourceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("%w: decoding response: %w", ErrBotConnector, err)
	}

	return &result, nil
}

// ResetTokenCache drops every cached bearer token. Exported for tests, which
// reuse the same app id across fake connectors.
func ResetTokenCache() {
	tokenCache.mu.Lock()
	defer tokenCache.mu.Unlock()

	tokenCache.entries = map[string]tokenEntry{}
}
