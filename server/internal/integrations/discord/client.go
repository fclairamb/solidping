// Package discord provides a client for sending messages via Discord webhooks.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	// ErrDiscordWebhook is returned when a Discord webhook call fails.
	ErrDiscordWebhook = errors.New("discord webhook error")
	// ErrUnexpectedStatus is returned when an unexpected HTTP status is received.
	ErrUnexpectedStatus = errors.New("unexpected HTTP status")
)

const (
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 30 * time.Second
)

// Client is a Discord webhook client.
type Client struct {
	httpClient *http.Client
	webhookURL string
}

// NewClient creates a new Discord webhook client.
func NewClient(webhookURL string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		webhookURL: webhookURL,
	}
}

// SendWebhookMessage sends a message via a Discord webhook.
func (cl *Client) SendWebhookMessage(ctx context.Context, msg *WebhookMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := cl.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Discord webhooks return 204 No Content on success, or 200 OK with wait=true
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)

	return fmt.Errorf("%w: status %d, body: %s", ErrUnexpectedStatus, resp.StatusCode, string(respBody))
}
