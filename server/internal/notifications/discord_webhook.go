package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/httpclientpool"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const discordWebhookTimeout = 30 * time.Second

// Bounds on how long a 429 parks the notification job before its one retry.
const (
	discordWebhookRetryFallback = 1 * time.Second
	discordWebhookRetryMax      = 5 * time.Second
	discordWebhookBodySnippet   = 512
)

var (
	// errDiscordWebhookFailed is a permanent webhook failure (4xx other than
	// 429): a deleted webhook, a revoked token, a rejected payload.
	errDiscordWebhookFailed = errors.New("discord webhook failed")
	// errDiscordWebhookTransient is a 429 that persisted through the retry, or
	// a 5xx. Always wrapped as retryable.
	errDiscordWebhookTransient = errors.New("discord webhook temporarily unavailable")
)

// discordWebhookMessage is the body of an execute-webhook call.
//
//nolint:tagliatelle // Discord API uses snake_case
type discordWebhookMessage struct {
	Username        string                   `json:"username,omitempty"`
	Embeds          []discord.Embed          `json:"embeds"`
	AllowedMentions *discord.AllowedMentions `json:"allowed_mentions"`
}

// discordRateLimitBody is the JSON body Discord sends with a 429.
//
//nolint:tagliatelle // Discord API uses snake_case
type discordRateLimitBody struct {
	RetryAfter float64 `json:"retry_after"`
}

// sendViaWebhook is the webhook delivery path: one standalone embed per event,
// the same title, color, fields and link as the bot message. A webhook cannot
// receive interactions and returns no thread, so there are no buttons, no
// threads and no mentions.
func (ds *DiscordSender) sendViaWebhook(
	ctx context.Context, jctx *jobdef.JobContext, webhookURL string, payload *Payload,
) error {
	guard := egressGuardFrom(jctx)
	if err := ValidateSenderURL(ctx, guard, webhookURL); err != nil {
		return err
	}

	target, err := discordWebhookWaitURL(webhookURL)
	if err != nil {
		return err
	}

	body, err := json.Marshal(&discordWebhookMessage{
		Username: productName,
		Embeds:   []discord.Embed{ds.buildEmbed(payload)},
		// An empty parse list disables @everyone/@here/role pings that a check
		// name or comment could otherwise smuggle in.
		AllowedMentions: &discord.AllowedMentions{Parse: []string{}},
	})
	if err != nil {
		return fmt.Errorf("marshaling discord webhook payload: %w", err)
	}

	client := httpclientpool.NewGuardedClient(discordWebhookTimeout, guard)

	status, respBody, header, err := postDiscordWebhook(ctx, client, target, body)
	if err != nil {
		return err
	}

	if status == http.StatusTooManyRequests {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(discordWebhookRetryAfter(header, respBody)):
		}

		status, respBody, _, err = postDiscordWebhook(ctx, client, target, body)
		if err != nil {
			return err
		}
	}

	return classifyDiscordWebhookResponse(status, respBody)
}

// discordWebhookWaitURL adds ?wait=true so Discord answers with the created
// message (200) or a real error, instead of an unconditional 204. Any query
// the operator pasted (thread_id) is kept.
func discordWebhookWaitURL(raw string) (string, error) {
	parsed, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSenderURLInvalid, err)
	}

	query := parsed.Query()
	query.Set("wait", "true")
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

// postDiscordWebhook performs one execute-webhook call.
func postDiscordWebhook(
	ctx context.Context, client *http.Client, target string, body []byte,
) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("creating discord webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SolidPing (https://solidping.io, 1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("sending discord webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, discordWebhookBodySnippet))

	return resp.StatusCode, respBody, resp.Header, nil
}

// discordWebhookRetryAfter reads the advertised delay, from the JSON body's
// retry_after (seconds, fractional) or the Retry-After header, clamped so a
// hostile or broken value cannot park the job for minutes.
func discordWebhookRetryAfter(header http.Header, body []byte) time.Duration {
	seconds := 0.0

	var rl discordRateLimitBody
	if json.Unmarshal(body, &rl) == nil && rl.RetryAfter > 0 {
		seconds = rl.RetryAfter
	} else if parsed, err := strconv.ParseFloat(header.Get("Retry-After"), 64); err == nil && parsed > 0 {
		seconds = parsed
	}

	if seconds <= 0 {
		return discordWebhookRetryFallback
	}

	wait := time.Duration(seconds * float64(time.Second))
	if wait > discordWebhookRetryMax {
		return discordWebhookRetryMax
	}

	return wait
}

// classifyDiscordWebhookResponse maps the final status to nil, a permanent
// error, or a retryable one (429 after the retry, 5xx).
func classifyDiscordWebhookResponse(status int, body []byte) error {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return nil
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return jobdef.NewRetryableError(
			fmt.Errorf("%w: status %d: %s", errDiscordWebhookTransient, status, string(body)))
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: status 404, the webhook was deleted in Discord: %s",
			errDiscordWebhookFailed, string(body))
	default:
		return fmt.Errorf("%w: status %d: %s", errDiscordWebhookFailed, status, string(body))
	}
}
