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

// ErrNotFound is returned when Discord answers 404 — a deleted channel, an
// unknown message, a thread that no longer exists. Callers distinguish it so
// "the destination is gone" can degrade differently from "Discord is broken".
var ErrNotFound = errors.New("discord resource not found")

// maxBodySnippet bounds how much of an error response we quote back. Discord
// error bodies are small, but a proxy in front of it may not be.
const maxBodySnippet = 512

// BotClient is a Discord REST client authenticated as the application's bot.
//
// It is deliberately separate from Client (the legacy webhook poster): a
// webhook connection has no token and no bot capabilities, and keeping the two
// apart is what makes it structurally impossible for the bot rework to break
// the legacy path.
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

		return fmt.Errorf("%w: status %d on %s %s: %s",
			ErrUnexpectedStatus, resp.StatusCode, method, path, string(snippet))
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
			return nil, fmt.Errorf("marshalling discord request body: %w", err)
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
