// Package telegram is a minimal client for the Telegram Bot API
// (api.telegram.org). Like internal/integrations/whatsapp it depends on nothing
// but the standard library and internal/config, so both the escalation
// dispatcher and the HTTP handlers can import it without an import cycle.
//
// The whole feature runs on ONE instance-level bot: the token identifies the
// deployment, and a per-user "chat id" contact identifies the recipient.
// A Telegram bot can hold only one webhook URL, which is why dev and prod each
// need their own bot — that is a platform constraint, not a convention.
//
// Messages are sent with parse_mode=HTML. Telegram rejects the ENTIRE message
// when the entities are malformed, so every interpolated value must go through
// EscapeHTML. That single rule is the most likely production bug in this
// package: an unescaped '&' in a check name would silently kill alerting for
// that check.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
)

const (
	// DefaultBaseURL is the real Bot API base.
	DefaultBaseURL = config.DefaultTelegramBaseURL
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
	// maxResponseBody caps how much of a Bot API response we will read. Bot API
	// responses are tiny; this only bounds a misbehaving proxy.
	maxResponseBody = 1 << 20
)

// Typed delivery-failure classes. Every one of these is a *handled* outcome:
// the caller logs it, records it on the notification audit row and moves on to
// the next escalation step — none of them is ever fatal to the server.
var (
	// ErrUnauthorized means the bot token is wrong, revoked, or the bot was
	// deleted. Permanent and instance-wide: every contact on every org is
	// affected, so it must be logged loudly.
	ErrUnauthorized = errors.New("telegram: bot token unauthorized or revoked")
	// ErrBotBlocked means the user blocked the bot, deleted the chat, or the
	// account was deactivated. Permanent **per contact**: the contact must be
	// un-verified so it stops silently swallowing pages.
	ErrBotBlocked = errors.New("telegram: bot was blocked by the user")
	// ErrChatNotFound means the chat id does not resolve. Permanent per contact,
	// same handling as ErrBotBlocked.
	ErrChatNotFound = errors.New("telegram: chat not found")
	// ErrReplyTargetMissing means reply_to_message_id pointed at a message that
	// no longer exists (the user deleted it, or it aged out of the chat).
	// Threading-only: the caller MUST degrade to a standalone message rather
	// than lose the delivery.
	ErrReplyTargetMissing = errors.New("telegram: message to reply to was not found")
	// ErrRateLimited means Telegram throttled us. Transient; the error carries
	// RetryAfter when Telegram supplied one.
	ErrRateLimited = errors.New("telegram: rate limited")
	// ErrRequestFailed is the unclassified fallback for a non-2xx response or a
	// transport error. Transient.
	ErrRequestFailed = errors.New("telegram: request failed")
	// ErrNotConfigured is returned when the client is built from a config that
	// cannot send (kill switch off, or missing bot token / username).
	ErrNotConfigured = errors.New("telegram: not configured on this instance")
	// ErrInvalidChat is returned when no destination chat id was supplied.
	ErrInvalidChat = errors.New("telegram: destination chat id is required")
	// ErrEmptyMessage is returned when the message body is empty — Telegram
	// rejects those, and an empty page helps nobody.
	ErrEmptyMessage = errors.New("telegram: message text is required")
)

// htmlEscaper replaces exactly the three characters Telegram's HTML parse mode
// reserves. Deliberately NOT html.EscapeString: that also rewrites quotes to
// numeric references (&#34; / &#39;), which Telegram's restricted entity parser
// does not accept in every position.
//
//nolint:gochecknoglobals // immutable replacer, effectively a constant.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// EscapeHTML makes an arbitrary string safe to interpolate into a Telegram
// parse_mode=HTML message body.
//
// This is THE escaping entry point for the whole feature: check names, incident
// titles and org slugs are all attacker-adjacent free text, and Telegram
// rejects the entire message (not just the offending span) when the entities do
// not parse. A check named "A & B <script>" must not be able to kill alerting.
func EscapeHTML(s string) string {
	return htmlEscaper.Replace(s)
}

// APIError carries Telegram's structured error so the audit trail can show the
// operator exactly what the Bot API said. Wrapped by one of the typed sentinels
// above, so callers match with errors.Is and read details with errors.As.
type APIError struct {
	// StatusCode is the HTTP status of the Bot API response.
	StatusCode int
	// ErrorCode is Telegram's `error_code` (usually mirrors StatusCode).
	ErrorCode int
	// Description is Telegram's human-readable `description`. Safe to store: it
	// never contains our bot token.
	Description string
	// RetryAfter is `parameters.retry_after` in seconds, set on 429 responses.
	// Zero when Telegram did not supply one.
	RetryAfter int
	// class is the typed sentinel this error unwraps to.
	class error
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%v (http %d, code %d): %s", e.class, e.StatusCode, e.ErrorCode, e.Description)
}

// Unwrap lets errors.Is match the typed class sentinel.
func (e *APIError) Unwrap() error { return e.class }

// RetryAfter returns the Telegram-supplied cooldown for a rate-limited send, or
// zero when the error is not a rate limit or carried no hint. Callers honor
// this instead of a generic backoff — Telegram's own number is the only one
// that avoids compounding the throttle.
func RetryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return time.Duration(apiErr.RetryAfter) * time.Second
	}

	return 0
}

// apiResponse is the envelope every Bot API method returns.
type apiResponse struct {
	OK bool `json:"ok"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  *struct {
		//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
		RetryAfter int `json:"retry_after"`
		//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
	} `json:"parameters"`
}

// Client talks to one Telegram bot.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// Options are the inputs to NewClient. Kept separate from config.TelegramConfig
// so tests can build a client without a whole server config.
type Options struct {
	BaseURL  string
	BotToken string
}

// NewClient builds a client from explicit options, defaulting the base URL.
// Pass a BaseURL to target an httptest fake.
func NewClient(opts Options) (*Client, error) {
	token := strings.TrimSpace(opts.BotToken)
	if token == "" {
		return nil, ErrNotConfigured
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	return &Client{
		httpClient: &http.Client{Timeout: DefaultTimeout},
		baseURL:    baseURL,
		token:      token,
	}, nil
}

// NewClientFromConfig builds a client from the instance Telegram config.
// Returns ErrNotConfigured when the instance cannot send.
func NewClientFromConfig(cfg *config.TelegramConfig) (*Client, error) {
	if cfg == nil || !cfg.Active() {
		return nil, ErrNotConfigured
	}

	return NewClient(Options{BaseURL: cfg.BaseURL, BotToken: cfg.BotToken})
}

// NewClientWithBaseURL builds a client pointed at an explicit base URL — the
// httptest seam. Mirrors the WhatsApp client's constructor pair so both
// integrations are wired the same way in tests.
func NewClientWithBaseURL(botToken, baseURL string) (*Client, error) {
	return NewClient(Options{BaseURL: baseURL, BotToken: botToken})
}

// Message describes one outbound Bot API sendMessage call.
type Message struct {
	// ChatID is the destination chat (a numeric id as a string, or "@channel").
	ChatID string
	// HTML is the message body, already escaped by EscapeHTML wherever it
	// interpolates untrusted text.
	HTML string
	// ReplyToMessageID threads this message under an earlier one. Zero sends a
	// standalone message.
	ReplyToMessageID int64
	// MessageThreadID targets a forum topic. Zero omits it.
	MessageThreadID int64
}

// sendMessagePayload is the wire shape of sendMessage.
type sendMessagePayload struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ParseMode string `json:"parse_mode"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	LinkPreviewOptions linkPreviewOptions `json:"link_preview_options"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ReplyToMessageID int64 `json:"reply_to_message_id,omitempty"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	MessageThreadID int64 `json:"message_thread_id,omitempty"`
}

type linkPreviewOptions struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	IsDisabled bool `json:"is_disabled"`
}

// messageResult is the subset of Telegram's Message object we need back.
type messageResult struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	MessageID int64 `json:"message_id"`
}

// SendMessage posts one message and returns Telegram's message id, which the
// threading state entry keys on.
//
// Link previews are disabled: every alert carries a dashboard link, and a
// preview card on every page is noise in an on-call chat.
func (c *Client) SendMessage(ctx context.Context, msg *Message) (int64, error) {
	chatID := strings.TrimSpace(msg.ChatID)
	if chatID == "" {
		return 0, ErrInvalidChat
	}

	if strings.TrimSpace(msg.HTML) == "" {
		return 0, ErrEmptyMessage
	}

	payload := sendMessagePayload{
		ChatID:             chatID,
		Text:               msg.HTML,
		ParseMode:          "HTML",
		LinkPreviewOptions: linkPreviewOptions{IsDisabled: true},
		ReplyToMessageID:   msg.ReplyToMessageID,
		MessageThreadID:    msg.MessageThreadID,
	}

	raw, err := c.call(ctx, "sendMessage", payload)
	if err != nil {
		return 0, err
	}

	var result messageResult
	if unmarshalErr := json.Unmarshal(raw, &result); unmarshalErr != nil {
		return 0, fmt.Errorf("parsing telegram sendMessage result: %w", unmarshalErr)
	}

	return result.MessageID, nil
}

// editMessagePayload is the wire shape of editMessageText.
type editMessagePayload struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ChatID string `json:"chat_id"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	ParseMode string `json:"parse_mode"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	LinkPreviewOptions linkPreviewOptions `json:"link_preview_options"`
}

// EditMessageText rewrites an earlier message in place — used to mark the
// original alert resolved so someone scrolling back does not read a stale red
// alert as live. Best-effort by contract: a failure here never costs a
// delivery, because the resolution message itself is sent separately.
func (c *Client) EditMessageText(ctx context.Context, chatID string, messageID int64, html string) error {
	if strings.TrimSpace(chatID) == "" {
		return ErrInvalidChat
	}

	if messageID == 0 {
		return ErrEmptyMessage
	}

	_, err := c.call(ctx, "editMessageText", editMessagePayload{
		ChatID:             strings.TrimSpace(chatID),
		MessageID:          messageID,
		Text:               html,
		ParseMode:          "HTML",
		LinkPreviewOptions: linkPreviewOptions{IsDisabled: true},
	})

	return err
}

// BotUser is the subset of Telegram's User object getMe returns that we care
// about.
type BotUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	FirstName string `json:"first_name"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	IsBot bool `json:"is_bot"`
}

// GetMe returns the bot's own identity. Used by the boot-time sanity check that
// the configured BotUsername actually matches the token: a stale username
// produces connect links pointing at the wrong bot, which is otherwise a
// silent, baffling failure.
func (c *Client) GetMe(ctx context.Context) (*BotUser, error) {
	raw, err := c.call(ctx, "getMe", struct{}{})
	if err != nil {
		return nil, err
	}

	var user BotUser
	if unmarshalErr := json.Unmarshal(raw, &user); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing telegram getMe result: %w", unmarshalErr)
	}

	return &user, nil
}

// AllowedUpdates is the exact update set the bot subscribes to. Narrow on
// purpose: "message" carries /start and /stop, "my_chat_member" carries the
// block/kick transition. Subscribing to more would only grow the attack and
// parse surface of a public endpoint.
//
//nolint:gochecknoglobals // immutable list, effectively a constant.
var AllowedUpdates = []string{"message", "my_chat_member"}

// setWebhookPayload is the wire shape of setWebhook.
type setWebhookPayload struct {
	URL string `json:"url"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	SecretToken string `json:"secret_token"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	AllowedUpdates []string `json:"allowed_updates"`
}

// SetWebhook registers the inbound webhook URL and its secret token. Called at
// boot when the registered URL differs from ours, so a deploy to a new hostname
// self-heals instead of requiring a manual curl.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	_, err := c.call(ctx, "setWebhook", setWebhookPayload{
		URL:            webhookURL,
		SecretToken:    secret,
		AllowedUpdates: AllowedUpdates,
	})

	return err
}

// WebhookInfo is the subset of Telegram's WebhookInfo we need to decide whether
// a re-registration is required.
type WebhookInfo struct {
	URL string `json:"url"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	PendingUpdateCount int `json:"pending_update_count"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	LastErrorMessage string `json:"last_error_message"`
}

// GetWebhookInfo returns the currently registered webhook.
func (c *Client) GetWebhookInfo(ctx context.Context) (*WebhookInfo, error) {
	raw, err := c.call(ctx, "getWebhookInfo", struct{}{})
	if err != nil {
		return nil, err
	}

	var info WebhookInfo
	if unmarshalErr := json.Unmarshal(raw, &info); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing telegram getWebhookInfo result: %w", unmarshalErr)
	}

	return &info, nil
}

// call posts a JSON payload to one Bot API method and returns the raw `result`.
// The token lives in the PATH (Telegram's scheme), never in a header — which is
// exactly why no error string built here may ever include the endpoint.
func (c *Client) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal telegram %s payload: %w", method, err)
	}

	endpoint := c.baseURL + "/bot" + c.token + "/" + method

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating telegram %s request: %w", method, err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Transport failures are transient and must never leak the URL (it
		// carries the bot token), so the endpoint is deliberately not wrapped in.
		return nil, fmt.Errorf("%w: %s transport error", ErrRequestFailed, method)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))

	var parsed apiResponse
	// A body that does not parse is still classified from the HTTP status
	// below — a proxy returning an HTML 502 must not become a parse error.
	_ = json.Unmarshal(respBody, &parsed)

	if resp.StatusCode >= http.StatusMultipleChoices || !parsed.OK {
		return nil, classifyError(resp.StatusCode, &parsed)
	}

	return parsed.Result, nil
}

// permanentChatFailures maps a lowercase description fragment to the typed
// class it implies. Telegram's error_code alone is too coarse (403 covers both
// "blocked" and "kicked"; 400 covers everything else), so the description is
// the only reliable discriminator.
//
//nolint:gochecknoglobals // immutable lookup table, effectively a constant.
var descriptionClasses = []struct {
	fragment string
	class    error
}{
	{"bot was blocked by the user", ErrBotBlocked},
	{"user is deactivated", ErrBotBlocked},
	{"bot was kicked", ErrBotBlocked},
	{"chat not found", ErrChatNotFound},
	{"chat_id is empty", ErrChatNotFound},
	// Telegram has used several wordings for a dangling reply target over the
	// years; all of them mention the reply and a missing message.
	{"message to be replied not found", ErrReplyTargetMissing},
	{"message to reply not found", ErrReplyTargetMissing},
	{"replied message not found", ErrReplyTargetMissing},
	{"message to edit not found", ErrReplyTargetMissing},
}

// classifyError turns a failed Bot API call into a typed *APIError.
func classifyError(status int, resp *apiResponse) error {
	apiErr := &APIError{
		StatusCode:  status,
		ErrorCode:   resp.ErrorCode,
		Description: resp.Description,
	}

	if resp.Parameters != nil {
		apiErr.RetryAfter = resp.Parameters.RetryAfter
	}

	lowered := strings.ToLower(resp.Description)
	for i := range descriptionClasses {
		if strings.Contains(lowered, descriptionClasses[i].fragment) {
			apiErr.class = descriptionClasses[i].class

			return apiErr
		}
	}

	// Fall back to the status code. Telegram mirrors it in error_code, so use
	// whichever is set — a transport-level proxy error has no error_code.
	code := resp.ErrorCode
	if code == 0 {
		code = status
	}

	switch code {
	case http.StatusUnauthorized:
		apiErr.class = ErrUnauthorized
	case http.StatusForbidden:
		// A bare 403 from Telegram always means "this bot may not message this
		// chat" — permanent, per contact.
		apiErr.class = ErrBotBlocked
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
	case errors.Is(err, ErrUnauthorized):
		return "telegram_unauthorized"
	case errors.Is(err, ErrBotBlocked):
		return "telegram_bot_blocked"
	case errors.Is(err, ErrChatNotFound):
		return "telegram_chat_not_found"
	case errors.Is(err, ErrReplyTargetMissing):
		return "telegram_reply_target_missing"
	case errors.Is(err, ErrRateLimited):
		return "telegram_rate_limited"
	case errors.Is(err, ErrNotConfigured):
		return "telegram_not_configured"
	case errors.Is(err, ErrInvalidChat):
		return "telegram_invalid_chat"
	default:
		return "telegram_send_failed"
	}
}

// ContactDisabling reports whether a send error means the contact itself can no
// longer receive anything — the user blocked the bot, deleted their account, or
// the chat is gone. The dispatcher clears the contact's VerifiedAt on these:
// a blocked contact that stays "verified" is a route that silently swallows
// every future page, which is the worst failure mode a paging channel has.
func ContactDisabling(err error) bool {
	return errors.Is(err, ErrBotBlocked) || errors.Is(err, ErrChatNotFound)
}

// ValidChatID reports whether s is a usable Telegram chat id: a non-zero
// (possibly negative, for groups) integer.
//
// Zero is rejected on purpose. Telegram has no chat 0, so a "0" here always
// means an update arrived without a real chat — and storing it would create a
// contact that can never be delivered to and that no user can recognize in
// their contacts list.
func ValidChatID(s string) bool {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)

	return err == nil && id != 0
}
