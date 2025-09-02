package slack

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
	"time"
)

var (
	// ErrSlackAPI is returned when a Slack API call fails.
	ErrSlackAPI = errors.New("slack API error")
	// ErrUnexpectedStatus is returned when an unexpected HTTP status is received.
	ErrUnexpectedStatus = errors.New("unexpected HTTP status")
)

const (
	// SlackAPIBaseURL is the base URL for Slack API calls.
	SlackAPIBaseURL = "https://slack.com/api"
	// SlackOAuthURL is the URL for OAuth token exchange.
	SlackOAuthURL = "https://slack.com/api/oauth.v2.access"
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
)

// Client is a Slack API client.
type Client struct {
	httpClient *http.Client
	token      string
}

// NewClient creates a new Slack API client.
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		token: token,
	}
}

// ExchangeCode exchanges an OAuth code for an access token.
func ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (*OAuthResponse, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	if redirectURI != "" {
		data.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SlackOAuthURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: DefaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var oauthResp OAuthResponse
	if err := json.Unmarshal(body, &oauthResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !oauthResp.OK {
		return nil, fmt.Errorf("%w: %s", ErrSlackAPI, oauthResp.Error)
	}

	return &oauthResp, nil
}

// PostMessageOptions contains options for posting a message.
type PostMessageOptions struct {
	Channel  string
	ThreadTS string // If set, posts as a reply in the thread
	Message  *MessageResponse
}

// PostMessageResult contains the result of posting a message.
type PostMessageResult struct {
	TS      string `json:"ts"`      // Message timestamp (can be used as thread_ts)
	Channel string `json:"channel"` // Channel ID where message was posted
}

// PostMessage sends a message to a Slack channel and returns the message timestamp.
func (c *Client) PostMessage(ctx context.Context, opts PostMessageOptions) (*PostMessageResult, error) {
	payload := map[string]any{
		"channel": opts.Channel,
		"text":    opts.Message.Text,
	}
	if len(opts.Message.Blocks) > 0 {
		payload["blocks"] = opts.Message.Blocks
	}
	if len(opts.Message.Attachments) > 0 {
		payload["attachments"] = opts.Message.Attachments
	}
	if opts.ThreadTS != "" {
		payload["thread_ts"] = opts.ThreadTS
	}

	var result PostMessageResult
	if err := c.callAPI(ctx, "chat.postMessage", payload, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// UpdateMessageOptions contains options for updating a message.
type UpdateMessageOptions struct {
	Channel string
	TS      string // Message timestamp to update
	Message *MessageResponse
}

// UpdateMessage updates an existing message in a Slack channel.
func (c *Client) UpdateMessage(ctx context.Context, opts UpdateMessageOptions) error {
	payload := map[string]any{
		"channel": opts.Channel,
		"ts":      opts.TS,
		"text":    opts.Message.Text,
	}
	if len(opts.Message.Blocks) > 0 {
		payload["blocks"] = opts.Message.Blocks
	}
	if len(opts.Message.Attachments) > 0 {
		payload["attachments"] = opts.Message.Attachments
	}

	return c.callAPI(ctx, "chat.update", payload, nil)
}

// PostEphemeral sends an ephemeral message to a user in a channel.
func (c *Client) PostEphemeral(ctx context.Context, channel, user string, msg *MessageResponse) error {
	payload := map[string]any{
		"channel": channel,
		"user":    user,
		"text":    msg.Text,
	}
	if len(msg.Blocks) > 0 {
		payload["blocks"] = msg.Blocks
	}
	if len(msg.Attachments) > 0 {
		payload["attachments"] = msg.Attachments
	}

	return c.callAPI(ctx, "chat.postEphemeral", payload, nil)
}

// UnfurlLinks unfurls links in a message.
func (c *Client) UnfurlLinks(ctx context.Context, channel, ts string, unfurls map[string]Unfurl) error {
	return c.callAPI(ctx, "chat.unfurl", map[string]any{
		"channel": channel,
		"ts":      ts,
		"unfurls": unfurls,
	}, nil)
}

// PublishView publishes a view to the App Home tab.
func (c *Client) PublishView(ctx context.Context, userID string, view *AppHomeView) error {
	return c.callAPI(ctx, "views.publish", map[string]any{
		"user_id": userID,
		"view":    view,
	}, nil)
}

// OpenModal opens a modal view.
func (c *Client) OpenModal(ctx context.Context, triggerID string, view *ModalView) error {
	return c.callAPI(ctx, "views.open", map[string]any{
		"trigger_id": triggerID,
		"view":       view,
	}, nil)
}

// UpdateModal updates an existing modal view.
func (c *Client) UpdateModal(ctx context.Context, viewID, hash string, view *ModalView) error {
	payload := map[string]any{
		"view_id": viewID,
		"view":    view,
	}
	if hash != "" {
		payload["hash"] = hash
	}

	return c.callAPI(ctx, "views.update", payload, nil)
}

// AddReaction adds a reaction to a message.
func (c *Client) AddReaction(ctx context.Context, channel, timestamp, emoji string) error {
	return c.callAPI(ctx, "reactions.add", map[string]any{
		"channel":   channel,
		"timestamp": timestamp,
		"name":      emoji,
	}, nil)
}

// GetUserInfo gets information about a user.
func (c *Client) GetUserInfo(ctx context.Context, userID string) (*User, error) {
	var result struct {
		OK   bool `json:"ok"`
		User User `json:"user"`
	}

	if err := c.callAPI(ctx, "users.info", map[string]any{
		"user": userID,
	}, &result); err != nil {
		return nil, err
	}

	return &result.User, nil
}

// GetUserDetails gets detailed information about a user including email.
// Requires the users:read.email scope.
func (c *Client) GetUserDetails(ctx context.Context, userID string) (*UserDetails, error) {
	var result struct {
		OK   bool        `json:"ok"`
		User UserDetails `json:"user"`
	}

	if err := c.callAPI(ctx, "users.info", map[string]any{
		"user": userID,
	}, &result); err != nil {
		return nil, err
	}

	return &result.User, nil
}

// FetchUserDetails fetches user details using an access token.
// This is a standalone function for use during OAuth flow.
func FetchUserDetails(ctx context.Context, accessToken, userID string) (*UserDetails, error) {
	client := NewClient(accessToken)

	return client.GetUserDetails(ctx, userID)
}

// OpenIDUserInfo represents the response from openid.connect.userInfo.
//
//nolint:tagliatelle // Slack OpenID Connect API uses snake_case and custom field names
type OpenIDUserInfo struct {
	OK                bool   `json:"ok"`
	Sub               string `json:"sub"` // Slack user ID (e.g., U013ZGBT0SJ)
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	Name              string `json:"name"`
	Picture           string `json:"picture"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Locale            string `json:"locale"`
	SlackTeamID       string `json:"https://slack.com/team_id"`
	SlackTeamName     string `json:"https://slack.com/team_name"`
	SlackTeamDomain   string `json:"https://slack.com/team_domain"`
	SlackTeamImage230 string `json:"https://slack.com/team_image_230"`
	Error             string `json:"error,omitempty"`
}

// FetchOpenIDUserInfo fetches user info via OpenID Connect.
// This requires the openid, email, and profile scopes on the user token.
func FetchOpenIDUserInfo(ctx context.Context, userAccessToken string) (*OpenIDUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SlackAPIBaseURL+"/openid.connect.userInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userAccessToken)

	client := &http.Client{Timeout: DefaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var userInfo OpenIDUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !userInfo.OK {
		return nil, fmt.Errorf("%w: %s", ErrSlackAPI, userInfo.Error)
	}

	return &userInfo, nil
}

// ListChannels lists public channels in the workspace.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var result struct {
		OK       bool      `json:"ok"`
		Channels []Channel `json:"channels"`
	}

	if err := c.callAPI(ctx, "conversations.list", map[string]any{
		"types":            "public_channel",
		"exclude_archived": true,
	}, &result); err != nil {
		return nil, err
	}

	return result.Channels, nil
}

// callAPI makes a Slack API call.
func (c *Client) callAPI(ctx context.Context, method string, payload map[string]any, result any) error {
	url := fmt.Sprintf("%s/%s", SlackAPIBaseURL, method)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Parse to check for errors
	var apiResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !apiResp.OK {
		return fmt.Errorf("%w: %s", ErrSlackAPI, apiResp.Error)
	}

	// If a result struct was provided, unmarshal into it
	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse result: %w", err)
		}
	}

	return nil
}

// RespondToWebhook sends a response to a Slack response URL.
func RespondToWebhook(ctx context.Context, responseURL string, msg *MessageResponse) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: DefaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	return nil
}
