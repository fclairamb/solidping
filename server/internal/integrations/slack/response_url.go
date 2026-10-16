package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/httpclientpool"
)

// asyncCheckTimeout bounds how long the detached goroutine that finishes
// `/solidping check` (a DB write plus a response_url follow-up) is allowed to
// run: comfortably inside response_url's validity window (Slack documents it
// as usable for up to 30 minutes / 5 uses), but not unbounded.
const asyncCheckTimeout = 25 * time.Second

// responseURLTimeout bounds a single response_url delivery.
const responseURLTimeout = 10 * time.Second

// slackResponseURLHost is the only host a response_url may point at. Slack
// hands out response URLs under https://hooks.slack.com/commands/... and
// https://hooks.slack.com/actions/...; GovSlack is deliberately not
// supported (nothing else in the integration is, see client.go).
const (
	slackResponseURLHost = "hooks.slack.com"
	schemeHTTPS          = "https"
)

// ErrInvalidResponseURL is returned when a response_url is not a Slack hooks URL.
var ErrInvalidResponseURL = errors.New("response_url must be a https://hooks.slack.com/ URL")

// responseURLHTTPClient is the client used to POST follow-up messages to a
// slash command's response_url. A short, fixed timeout — this is a single
// best-effort delivery, not something worth retrying indefinitely.
//
//nolint:gochecknoglobals // shared HTTP client, same pattern as Client.httpClient
var responseURLHTTPClient = newResponseURLClient(nil)

// newResponseURLClient builds a response_url client that never follows a
// redirect: the allowed host must not be able to bounce the POST elsewhere.
// A nil transport keeps the shared pooled transport; tests inject their own.
func newResponseURLClient(transport http.RoundTripper) *http.Client {
	client := httpclientpool.NewClient(responseURLTimeout)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	if transport != nil {
		client.Transport = transport
	}

	return client
}

// validateResponseURL accepts only https://hooks.slack.com/... with no
// userinfo and no explicit port. The response_url of an HTTP slash command
// comes from the request body, so it is caller-supplied until the signature
// says otherwise, and even then it must never reach anything but Slack.
func validateResponseURL(raw string) error {
	_, err := slackResponseURL(raw)

	return err
}

// slackResponseURL returns the URL to post to, rebuilt on the fixed Slack
// host so only the path and query come from the caller-supplied value.
func slackResponseURL(raw string) (string, error) {
	if !isSlackResponseURL(raw) {
		return "", ErrInvalidResponseURL
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidResponseURL
	}

	safe := url.URL{
		Scheme:   schemeHTTPS,
		Host:     slackResponseURLHost,
		Path:     parsed.Path,
		RawPath:  parsed.RawPath,
		RawQuery: parsed.RawQuery,
	}

	return safe.String(), nil
}

// isSlackResponseURL is validateResponseURL as a predicate.
func isSlackResponseURL(raw string) bool {
	if raw == "" {
		return false
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	return parsed.Scheme == schemeHTTPS && parsed.User == nil && parsed.Port() == "" &&
		strings.EqualFold(parsed.Hostname(), slackResponseURLHost)
}

// postResponseURL delivers a follow-up message to a slash command's
// response_url — Slack's documented mechanism for answering after the
// initial 3-second ACK window has passed. No Authorization header: the URL
// itself is the one-time credential Slack hands back in the command payload.
func postResponseURL(ctx context.Context, responseURL string, msg *MessageResponse) error {
	return postResponseURLWith(ctx, responseURLHTTPClient, responseURL, msg)
}

// postResponseURLWith is postResponseURL with an explicit client. It sends
// nothing when responseURL is not a Slack hooks URL.
func postResponseURLWith(ctx context.Context, client *http.Client, responseURL string, msg *MessageResponse) error {
	target, err := slackResponseURL(responseURL)
	if err != nil {
		return err
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal response_url payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build response_url request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call response_url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: response_url returned status %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	return nil
}
