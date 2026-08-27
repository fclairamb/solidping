package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const zulipTimeout = 30 * time.Second

// zulipTopicMaxLen is Zulip's hard limit on the length of a topic string.
// A longer topic is rejected outright by the API, so the sender truncates
// before sending rather than letting the request fail.
const zulipTopicMaxLen = 60

var (
	// ErrZulipSiteURLNotConfigured is returned when the Zulip realm URL is missing.
	ErrZulipSiteURLNotConfigured = errors.New("zulip site url not configured")
	// ErrZulipBotEmailNotConfigured is returned when the Zulip bot's email is missing.
	ErrZulipBotEmailNotConfigured = errors.New("zulip bot email not configured")
	// ErrZulipAPIKeyNotConfigured is returned when the Zulip bot's API key is missing.
	ErrZulipAPIKeyNotConfigured = errors.New("zulip api key not configured")
	// ErrZulipStreamNotConfigured is returned when the target stream is missing.
	ErrZulipStreamNotConfigured = errors.New("zulip stream not configured")
	// errZulipRequestFailed is returned when the Zulip request fails, either at
	// the HTTP level (non-2xx) or at the application level (a 200 response
	// carrying "result": "error" — Zulip reports invalid stream/topic/auth
	// combinations this way instead of a 4xx).
	errZulipRequestFailed = errors.New("zulip request failed")
)

// ZulipSender sends notifications through Zulip's bot ("send message") REST
// API, threading every lifecycle event of one incident into the same topic
// (see zulipTopic) so Zulip groups the whole incident automatically.
type ZulipSender struct{}

// zulipSettings is the per-connection settings shape for a Zulip channel.
// The keys use snake_case to match the dashboard form field names
// (integration-form.tsx writes "site_url" / "bot_email" / "api_key" /
// "stream"), not Go's default camelCase convention.
type zulipSettings struct {
	SiteURL  string `json:"site_url"`  //nolint:tagliatelle // matches dashboard form key
	BotEmail string `json:"bot_email"` //nolint:tagliatelle // matches dashboard form key
	APIKey   string `json:"api_key"`   //nolint:tagliatelle // matches dashboard form key
	Stream   string `json:"stream"`
}

// zulipAPIResponse is the shape common to every Zulip REST API response.
// Zulip always answers with HTTP 200 for an application-level failure (bad
// stream, bad topic, ...) and signals the failure through "result" instead —
// only a genuine HTTP-level problem (auth, routing, 5xx) uses a non-2xx
// status. Callers must check both.
type zulipAPIResponse struct {
	Result string `json:"result"`
	Msg    string `json:"msg"`
}

// Send sends a notification to Zulip.
func (s *ZulipSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	topic := zulipTopic(payload)
	content := s.buildContent(payload)

	form := url.Values{}
	form.Set("type", "stream")
	form.Set("to", settings.Stream)
	form.Set("topic", topic)
	form.Set("content", content)

	endpoint := strings.TrimRight(settings.SiteURL, "/") + "/api/v1/messages"

	// Zulip's message API is form-encoded, not JSON — unlike every other chat
	// sender in this file. Sending a JSON body here would silently be ignored
	// server-side (the same trap the Slack list APIs sprung: see
	// slack_mentions_test.go / wiki notes on form-encoded Slack endpoints), so
	// this must stay a urlencoded body, never json.Marshal.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("creating zulip request: %w", err)
	}

	req.SetBasicAuth(settings.BotEmail, settings.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", productName)

	client := newHTTPClient(zulipTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending zulip message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d: %s", errZulipRequestFailed, resp.StatusCode, string(respBody))
	}

	var apiResp zulipAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err == nil && apiResp.Result == "error" {
		return fmt.Errorf("%w: %s", errZulipRequestFailed, apiResp.Msg)
	}

	return nil
}

func (s *ZulipSender) parseSettings(payload *Payload) (*zulipSettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing zulip settings: %w", err)
	}

	var settings zulipSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing zulip settings: %w", err)
	}

	if settings.SiteURL == "" {
		return nil, ErrZulipSiteURLNotConfigured
	}

	if settings.BotEmail == "" {
		return nil, ErrZulipBotEmailNotConfigured
	}

	if settings.APIKey == "" {
		return nil, ErrZulipAPIKeyNotConfigured
	}

	if settings.Stream == "" {
		return nil, ErrZulipStreamNotConfigured
	}

	return &settings, nil
}

// zulipTopic derives Zulip's thread key for one incident: the check name and
// its short per-org reference, e.g. "API health (#42)". It is computed
// purely from the payload — no state is stored anywhere — and every
// lifecycle event of the same incident (created, escalated, comment,
// acknowledged, resolved) derives the identical string, which is what makes
// Zulip thread the whole incident into one topic automatically. Falls back
// to the bare check name when the incident has no short reference yet
// (created before the numbering existed and never backfilled), and is
// truncated to zulipTopicMaxLen, Zulip's hard limit on a topic string.
func zulipTopic(payload *Payload) string {
	checkName := getCheckName(payload.Check)

	topic := checkName
	if payload.Incident != nil && payload.Incident.Number > 0 {
		topic = fmt.Sprintf("%s (#%d)", checkName, payload.Incident.Number)
	}

	return truncateZulipTopic(topic)
}

// truncateZulipTopic hard-truncates topic to zulipTopicMaxLen runes. No
// ellipsis: Zulip only groups messages whose topic is byte-for-byte
// identical, so any suffix added here would have to be reproduced by every
// caller rather than derived once, which defeats the point of a stateless
// derivation.
func truncateZulipTopic(topic string) string {
	runes := []rune(topic)
	if len(runes) <= zulipTopicMaxLen {
		return topic
	}

	return string(runes[:zulipTopicMaxLen])
}

// buildContent renders the Zulip Markdown message body, reusing the
// Mattermost sender's event title/field helpers (eventColorAndTitle,
// buildFields) rather than formatting from scratch — Mattermost's
// attachment-style "colored card" has no Zulip equivalent, but the title and
// field content are channel-agnostic.
func (s *ZulipSender) buildContent(payload *Payload) string {
	checkName := getCheckName(payload.Check)
	mm := &MattermostSender{}
	_, title := mm.eventColorAndTitle(payload, checkName)
	fields := mm.buildFields(payload, checkName)

	var b strings.Builder

	fmt.Fprintf(&b, "**%s**\n", title)

	for _, field := range fields {
		fmt.Fprintf(&b, "* **%s:** %s\n", field.Title, field.Value)
	}

	return b.String()
}
