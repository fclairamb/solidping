package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// asyncCheckTimeout bounds how long the detached goroutine that finishes
// `/solidping check` (a DB write plus a response_url follow-up) is allowed to
// run: comfortably inside response_url's validity window (Slack documents it
// as usable for up to 30 minutes / 5 uses), but not unbounded.
const asyncCheckTimeout = 25 * time.Second

// responseURLHTTPClient is the client used to POST follow-up messages to a
// slash command's response_url. A short, fixed timeout — this is a single
// best-effort delivery, not something worth retrying indefinitely.
//
//nolint:gochecknoglobals // shared HTTP client, same pattern as Client.httpClient
var responseURLHTTPClient = &http.Client{Timeout: 10 * time.Second}

// postResponseURL delivers a follow-up message to a slash command's
// response_url — Slack's documented mechanism for answering after the
// initial 3-second ACK window has passed. No Authorization header: the URL
// itself is the one-time credential Slack hands back in the command payload.
func postResponseURL(ctx context.Context, responseURL string, msg *MessageResponse) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal response_url payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build response_url request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := responseURLHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call response_url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: response_url returned status %d", ErrUnexpectedStatus, resp.StatusCode)
	}

	return nil
}
