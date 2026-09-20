package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ErrBotTokenMissing is returned when a bot call is attempted with no token.
var ErrBotTokenMissing = errors.New("discord bot token not configured")

// ErrUnexpectedStatus is returned when an unexpected HTTP status is received.
var ErrUnexpectedStatus = errors.New("unexpected HTTP status")

// DefaultTimeout is the default HTTP client timeout.
const DefaultTimeout = 30 * time.Second

// ErrEmptyRecipient is returned when a DM is requested for no recipient. A
// blank Discord user id would otherwise reach Discord as a malformed request
// whose 400 says nothing about the cause.
var ErrEmptyRecipient = errors.New("discord dm recipient is empty")

// ErrNotFound is returned when Discord answers 404 — a deleted channel, an
// unknown message, a thread that no longer exists. Callers distinguish it so
// "the destination is gone" can degrade differently from "Discord is broken".
var ErrNotFound = errors.New("discord resource not found")

// maxBodySnippet bounds how much of an error response we quote back. Discord
// error bodies are small, but a proxy in front of it may not be.
const maxBodySnippet = 512

// ErrCodeCannotSendToUser is Discord's JSON error code 50007, "Cannot send
// messages to this user". It is the one outcome of a DM that is not a fault:
// the recipient has direct messages from server members switched off, has
// blocked the bot, or shares no server with it. Nothing an operator can fix,
// and nothing a retry will change.
const ErrCodeCannotSendToUser = 50007

// APIError is a non-2xx answer from Discord with its error envelope decoded.
//
// The envelope was previously thrown away and only the raw body quoted into a
// message string, which left callers with substring matching as the only way to
// tell one failure from another. That is fine for logging and useless for
// policy, and DMs need policy: code 50007 must fall through to the member's
// next paging route, while a 500 must be counted as a failure so somebody is
// told about it.
//
// Unwrap reports ErrUnexpectedStatus, so every pre-existing
// `errors.Is(err, ErrUnexpectedStatus)` caller keeps working unchanged.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is Discord's own numeric error code, 0 when the body carried none
	// (a proxy error page, an empty body, a non-JSON response).
	Code int
	// Message is Discord's human-readable message, or the quoted body snippet
	// when there was no envelope to decode.
	Message string
	// Method and Path name the call, so a log line is actionable without the
	// surrounding context.
	Method string
	Path   string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: status %d on %s %s: code %d: %s",
		ErrUnexpectedStatus.Error(), e.Status, e.Method, e.Path, e.Code, e.Message)
}

// Unwrap keeps errors.Is(err, ErrUnexpectedStatus) true for callers written
// before this type existed.
func (e *APIError) Unwrap() error { return ErrUnexpectedStatus }

// errorEnvelope is Discord's standard error body.
type errorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// IsCannotDMUser reports whether err is Discord refusing to open or use a DM
// with a user (error code 50007).
//
// This is NOT a delivery failure: the member simply cannot be reached this way
// — DMs from server members are off, the bot is blocked, or they share no
// server with it. Callers degrade rather than alert: paging falls through to the
// next route, and the account page's Test button says what the member can do
// about it.
func IsCannotDMUser(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	return apiErr.Code == ErrCodeCannotSendToUser
}

// BotClient is a Discord REST client authenticated as the application's bot.
type BotClient struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

// NewBotClient creates a bot-authenticated Discord REST client.
func NewBotClient(token string) *BotClient {
	return &BotClient{
		httpClient: &http.Client{Timeout: DefaultTimeout},
		token:      token,
		baseURL:    APIBaseURL,
	}
}

// WithBaseURL points the client at another base URL. Used by tests to drive
// the real request-building code against an httptest stand-in.
func (c *BotClient) WithBaseURL(base string) *BotClient {
	c.baseURL = base

	return c
}

// do performs one authenticated request, decoding a JSON response into out
// when out is non-nil. A 429 is retried once after the advertised delay —
// Discord's rate limits are per-route and short, and a single incident
// notification is worth one retry.
func (c *BotClient) do(ctx context.Context, method, path string, body, out any) error {
	if c.token == "" {
		return ErrBotTokenMissing
	}

	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		wait := retryAfter(resp)
		drain(resp)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		resp, err = c.send(ctx, method, path, body)
		if err != nil {
			return err
		}
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySnippet))

		return newAPIError(resp.StatusCode, method, path, snippet)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding discord response for %s %s: %w", method, path, err)
	}

	return nil
}

// send builds and performs one request without any retry or status handling.
func (c *BotClient) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling discord request body: %w", err)
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("building discord request: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+c.token)
	req.Header.Set("User-Agent", "SolidPing (https://solidping.io, 1.0)")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling discord %s %s: %w", method, path, err)
	}

	return resp, nil
}

// retryAfter reads Discord's Retry-After header, clamped so a hostile or
// broken value can never park a notification job for minutes.
func retryAfter(resp *http.Response) time.Duration {
	const (
		fallback = 1 * time.Second
		maxWait  = 5 * time.Second
	)

	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return fallback
	}

	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds <= 0 {
		return fallback
	}

	wait := time.Duration(seconds * float64(time.Second))
	if wait > maxWait {
		return maxWait
	}

	return wait
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodySnippet))
	_ = resp.Body.Close()
}

// newAPIError builds an APIError from a non-2xx response body, decoding
// Discord's error envelope when the body carries one. A body that is not the
// envelope (a proxy error page, an empty body) yields Code 0 and the quoted
// snippet as the message — strictly more information than before, never less.
func newAPIError(status int, method, path string, body []byte) *APIError {
	apiErr := &APIError{
		Status:  status,
		Message: string(body),
		Method:  method,
		Path:    path,
	}

	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Code != 0 {
		apiErr.Code = envelope.Code

		if envelope.Message != "" {
			apiErr.Message = envelope.Message
		}
	}

	return apiErr
}

// CreateMessage posts a message into a channel (or a thread — a thread id is
// a channel id in Discord's model).
func (c *BotClient) CreateMessage(
	ctx context.Context, channelID string, msg *Message,
) (*MessageResult, error) {
	var result MessageResult

	if err := c.do(ctx, http.MethodPost,
		"/channels/"+channelID+"/messages", msg, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// EditMessage rewrites an existing bot message in place. This is what turns
// the original "New incident" embed into a resolved one instead of leaving a
// stale red card at the top of the channel.
func (c *BotClient) EditMessage(
	ctx context.Context, channelID, messageID string, msg *Message,
) error {
	return c.do(ctx, http.MethodPatch,
		"/channels/"+channelID+"/messages/"+messageID, msg, nil)
}

// startThreadRequest is the body of "start thread from message".
//
//nolint:tagliatelle // Discord API uses snake_case
type startThreadRequest struct {
	Name                string `json:"name"`
	AutoArchiveDuration int    `json:"auto_archive_duration,omitempty"`
}

// StartThreadFromMessage creates a thread hanging off an existing message.
// Discord truncates thread names at 100 characters, so we do it first rather
// than letting the API 400 on a long check name.
func (c *BotClient) StartThreadFromMessage(
	ctx context.Context, channelID, messageID, name string,
) (*ChannelInfo, error) {
	var thread ChannelInfo

	body := &startThreadRequest{
		Name:                truncateThreadName(name),
		AutoArchiveDuration: autoArchiveDurationOneWeek,
	}

	if err := c.do(ctx, http.MethodPost,
		"/channels/"+channelID+"/messages/"+messageID+"/threads", body, &thread); err != nil {
		return nil, err
	}

	return &thread, nil
}

// maxThreadNameLen is Discord's hard limit on a thread name.
const maxThreadNameLen = 100

func truncateThreadName(name string) string {
	runes := []rune(name)
	if len(runes) <= maxThreadNameLen {
		return name
	}

	return string(runes[:maxThreadNameLen])
}

// unarchiveRequest is the body of the un-archive PATCH.
type unarchiveRequest struct {
	Archived bool `json:"archived"`
}

// UnarchiveThread clears a thread's archived flag.
//
// Discord auto-archives a thread after its inactivity window (1 day to 1 week)
// and a POST into an archived thread fails, so every late follow-up — the
// resolve message on a long incident above all — must un-archive first. This
// is deliberately unconditional and best-effort at the call site: asking
// Discord whether the thread is archived costs the same round trip as simply
// clearing the flag.
func (c *BotClient) UnarchiveThread(ctx context.Context, threadID string) error {
	return c.do(ctx, http.MethodPatch,
		"/channels/"+threadID, &unarchiveRequest{Archived: false}, nil)
}

// ListGuildChannels returns every channel of a guild.
func (c *BotClient) ListGuildChannels(ctx context.Context, guildID string) ([]ChannelInfo, error) {
	var channels []ChannelInfo

	if err := c.do(ctx, http.MethodGet,
		"/guilds/"+guildID+"/channels", nil, &channels); err != nil {
		return nil, err
	}

	return channels, nil
}

// GetGuild fetches a guild by id.
func (c *BotClient) GetGuild(ctx context.Context, guildID string) (*Guild, error) {
	var guild Guild

	if err := c.do(ctx, http.MethodGet, "/guilds/"+guildID, nil, &guild); err != nil {
		return nil, err
	}

	return &guild, nil
}

// GetCurrentUser returns the bot's own user object (used to learn the bot user
// id so the Gateway can ignore its own messages).
func (c *BotClient) GetCurrentUser(ctx context.Context) (*User, error) {
	var user User

	if err := c.do(ctx, http.MethodGet, "/users/@me", nil, &user); err != nil {
		return nil, err
	}

	return &user, nil
}

// GetChannel fetches one channel (or thread) by id.
func (c *BotClient) GetChannel(ctx context.Context, channelID string) (*ChannelInfo, error) {
	var channel ChannelInfo

	if err := c.do(ctx, http.MethodGet, "/channels/"+channelID, nil, &channel); err != nil {
		return nil, err
	}

	return &channel, nil
}

// createDMRequest is the body of POST /users/@me/channels.
//
//nolint:tagliatelle // Discord API uses snake_case
type createDMRequest struct {
	RecipientID string `json:"recipient_id"`
}

// CreateDM opens (or returns the existing) 1:1 DM channel between the bot and
// one user — POST /users/@me/channels.
//
// The call is idempotent on Discord's side: the same recipient always yields the
// same channel id, forever. That is why callers cache the result on the contact
// instead of calling this before every message.
//
// It needs NO additional bot permission and NO privileged gateway intent: a bot
// may always open a DM, and whether the user ACCEPTS one is decided by their own
// privacy settings at post time, which surfaces as APIError code 50007
// (IsCannotDMUser). Opening the channel can therefore succeed while posting into
// it fails.
func (c *BotClient) CreateDM(ctx context.Context, userID string) (*ChannelInfo, error) {
	if userID == "" {
		return nil, ErrEmptyRecipient
	}

	var info ChannelInfo

	if err := c.do(ctx, http.MethodPost, "/users/@me/channels",
		&createDMRequest{RecipientID: userID}, &info); err != nil {
		return nil, err
	}

	return &info, nil
}
