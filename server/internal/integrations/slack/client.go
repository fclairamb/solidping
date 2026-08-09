package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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

// Cursor pagination settings for Slack list APIs (conversations.list,
// users.list). Slack returns ~100 items per call by default and signals more
// data via response_metadata.next_cursor — ignoring the cursor silently
// truncates large workspaces.
const (
	// listPageSize is the per-page limit requested from Slack list APIs
	// (200 is Slack's recommended maximum page size).
	listPageSize = 200
	// maxListPages caps cursor-pagination loops (listPageSize*maxListPages =
	// 5000 items). If the cap is hit, a warning is logged and the partial
	// list is returned rather than looping forever.
	maxListPages = 25
)

// Slack API request payload field keys.
const (
	apiKeyChannel = "channel"
	apiKeyText    = "text"
	apiKeyUser    = "user"
	apiKeyView    = "view"
	apiKeyName    = "name"
)

// Client is a Slack API client.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

// NewClient creates a new Slack API client targeting the real Slack API.
func NewClient(token string) *Client {
	return NewClientWithBaseURL(token, SlackAPIBaseURL)
}

// NewClientWithBaseURL creates a Slack API client targeting a custom API base
// URL. Intended for tests that point the client at an httptest fake server.
func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		token:   token,
		baseURL: baseURL,
	}
}

// ExchangeCode exchanges an OAuth code for an access token at endpoint
// (SlackOAuthURL in production, an httptest stand-in in tests).
func ExchangeCode(
	ctx context.Context, endpoint, clientID, clientSecret, code, redirectURI string,
) (*OAuthResponse, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("code", code)
	if redirectURI != "" {
		data.Set("redirect_uri", redirectURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
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
		apiKeyChannel: opts.Channel,
		apiKeyText:    opts.Message.Text,
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
		apiKeyChannel: opts.Channel,
		"ts":          opts.TS,
		apiKeyText:    opts.Message.Text,
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
		apiKeyChannel: channel,
		apiKeyUser:    user,
		apiKeyText:    msg.Text,
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
		apiKeyChannel: channel,
		"ts":          ts,
		"unfurls":     unfurls,
	}, nil)
}

// PublishView publishes a view to the App Home tab.
func (c *Client) PublishView(ctx context.Context, userID string, view *AppHomeView) error {
	return c.callAPI(ctx, "views.publish", map[string]any{
		"user_id":  userID,
		apiKeyView: view,
	}, nil)
}

// OpenModal opens a modal view.
func (c *Client) OpenModal(ctx context.Context, triggerID string, view *ModalView) error {
	return c.callAPI(ctx, "views.open", map[string]any{
		"trigger_id": triggerID,
		apiKeyView:   view,
	}, nil)
}

// UpdateModal updates an existing modal view.
func (c *Client) UpdateModal(ctx context.Context, viewID, hash string, view *ModalView) error {
	payload := map[string]any{
		"view_id":  viewID,
		apiKeyView: view,
	}
	if hash != "" {
		payload["hash"] = hash
	}

	return c.callAPI(ctx, "views.update", payload, nil)
}

// AddReaction adds a reaction to a message.
func (c *Client) AddReaction(ctx context.Context, channel, timestamp, emoji string) error {
	return c.callAPI(ctx, "reactions.add", map[string]any{
		apiKeyChannel: channel,
		"timestamp":   timestamp,
		apiKeyName:    emoji,
	}, nil)
}

// GetUserInfo gets information about a user.
func (c *Client) GetUserInfo(ctx context.Context, userID string) (*User, error) {
	var result struct {
		OK   bool `json:"ok"`
		User User `json:"user"`
	}

	if err := c.callAPI(ctx, "users.info", map[string]any{
		apiKeyUser: userID,
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
		apiKeyUser: userID,
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

// FetchOpenIDUserInfo fetches user info via OpenID Connect at endpoint.
// This requires the openid, email, and profile scopes on the user token.
func FetchOpenIDUserInfo(ctx context.Context, endpoint, userAccessToken string) (*OpenIDUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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

// paginate drives a cursor-pagination loop over a Slack list API. fetchPage
// is called with the current cursor ("" on the first call) and returns the
// next cursor; an empty next cursor ends the loop. If maxListPages is reached
// while Slack still returns a cursor, a warning is logged and the pages
// fetched so far are kept rather than looping forever.
//
// The loop also stops if the cursor stops advancing — either Slack hands back
// the same cursor it was given (next == cursor) or a cursor already followed
// on an earlier page (tracked in seen). Both mean no forward progress:
// continuing would only re-fetch pages already seen, which is what causes the
// same channels to be appended maxListPages times (conversations.list is known
// to do this on its final page). See the duplicate-destinations spec.
func paginate(ctx context.Context, method string, fetchPage func(cursor string) (string, error)) error {
	cursor := ""
	seen := make(map[string]struct{})

	for range maxListPages {
		next, err := fetchPage(cursor)
		if err != nil {
			return err
		}

		if next == "" {
			return nil
		}

		if next == cursor {
			// Non-advancing cursor: Slack returned the same cursor it was
			// handed. Stop rather than re-fetch the same page forever.
			return nil
		}

		if _, ok := seen[next]; ok {
			// Repeating cursor: we already followed this cursor on an earlier
			// page, so pagination is cycling. Stop.
			return nil
		}

		seen[next] = struct{}{}
		cursor = next
	}

	slog.WarnContext(ctx, "Slack list API pagination hit the page cap, returning a partial list",
		"method", method,
		"max_pages", maxListPages,
	)

	return nil
}

// ListChannels lists public and private channels the bot has access to,
// following cursor pagination so workspaces with more than one page of
// channels are fully listed.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	const method = "conversations.list"

	channels := make([]Channel, 0, listPageSize)
	seen := make(map[string]struct{})

	err := paginate(ctx, method, func(cursor string) (string, error) {
		// Form-encoded, not JSON: conversations.list ignores JSON-body args.
		params := url.Values{}
		params.Set("types", "public_channel,private_channel")
		params.Set("exclude_archived", "true")
		params.Set("limit", strconv.Itoa(listPageSize))

		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var result struct {
			OK               bool             `json:"ok"`
			Channels         []Channel        `json:"channels"`
			ResponseMetadata ResponseMetadata `json:"response_metadata"` //nolint:tagliatelle // Slack API field
		}

		if err := c.callFormAPI(ctx, method, params, &result); err != nil {
			return "", err
		}

		// De-duplicate by Slack ID: a repeating cursor or overlapping pages
		// must never surface the same channel twice in the picker.
		for i := range result.Channels {
			channel := result.Channels[i]
			if _, ok := seen[channel.ID]; ok {
				continue
			}

			seen[channel.ID] = struct{}{}
			channels = append(channels, channel)
		}

		return result.ResponseMetadata.NextCursor, nil
	})
	if err != nil {
		return nil, err
	}

	return channels, nil
}

// ListUsers lists non-bot, non-deleted workspace members, following cursor
// pagination so large workspaces are fully listed.
func (c *Client) ListUsers(ctx context.Context) ([]SlackUser, error) {
	const method = "users.list"

	users := make([]SlackUser, 0, listPageSize)
	seen := make(map[string]struct{})

	err := paginate(ctx, method, func(cursor string) (string, error) {
		// Form-encoded, not JSON: users.list ignores JSON-body args.
		params := url.Values{}
		params.Set("limit", strconv.Itoa(listPageSize))

		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var result struct {
			OK               bool             `json:"ok"`
			Members          []SlackUser      `json:"members"`
			ResponseMetadata ResponseMetadata `json:"response_metadata"` //nolint:tagliatelle // Slack API field
		}

		if err := c.callFormAPI(ctx, method, params, &result); err != nil {
			return "", err
		}

		// Filter out bots/deleted members and de-duplicate by Slack ID so a
		// repeating cursor or overlapping pages cannot surface duplicates.
		for i := range result.Members {
			member := result.Members[i]
			if member.IsBot || member.Deleted {
				continue
			}

			if _, ok := seen[member.ID]; ok {
				continue
			}

			seen[member.ID] = struct{}{}
			users = append(users, member)
		}

		return result.ResponseMetadata.NextCursor, nil
	})
	if err != nil {
		return nil, err
	}

	return users, nil
}

// callAPI makes a Slack API call with a JSON-encoded body. This is the right
// transport for Slack's write methods (chat.*, views.*, reactions.*), which
// accept application/json. It is NOT suitable for the cursor-paginated read
// methods (conversations.list, users.list) — see callFormAPI.
func (c *Client) callAPI(ctx context.Context, method string, payload map[string]any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/%s", c.baseURL, method)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	return c.do(req, result)
}

// callFormAPI makes a Slack API call with form-encoded (application/x-www-form-urlencoded)
// parameters. Slack's cursor-paginated read methods (conversations.list,
// users.list) silently IGNORE arguments sent in a JSON body and fall back to
// defaults — limit 100 (not the requested 200), the first page only (cursor
// ignored, so later pages are never fetched), and public channels only (the
// `types` filter is dropped, hiding private channels). They must be called
// form-encoded so types/limit/cursor/exclude_archived are honored.
func (c *Client) callFormAPI(ctx context.Context, method string, params url.Values, result any) error {
	url := fmt.Sprintf("%s/%s", c.baseURL, method)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return c.do(req, result)
}

// do sets auth on a prepared Slack request, sends it, checks the `ok` flag, and
// unmarshals the body into result when provided. Shared by callAPI/callFormAPI.
func (c *Client) do(req *http.Request, result any) error {
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
