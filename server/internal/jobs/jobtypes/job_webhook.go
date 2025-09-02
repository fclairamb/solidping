package jobtypes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/version"
)

const (
	webhookTimeout       = 30 * time.Second
	httpSuccessThreshold = 300
)

// ErrWebhookHTTPError is returned when webhook returns an HTTP error status code.
var ErrWebhookHTTPError = errors.New("webhook HTTP error")

// WebhookJobConfig configures webhook parameters.
type WebhookJobConfig struct {
	URL     string                 `json:"url"`
	Method  string                 `json:"method"`
	Headers map[string]string      `json:"headers"`
	Body    map[string]interface{} `json:"body"`
}

// WebhookJobDefinition is the factory for webhook jobs.
type WebhookJobDefinition struct{}

// Type returns the job type identifier.
func (d *WebhookJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeWebhook
}

// CreateJobRun creates a new webhook job run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *WebhookJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg WebhookJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	// Default to POST if no method specified
	if cfg.Method == "" {
		cfg.Method = "POST"
	}

	return &WebhookJobRun{config: cfg}, nil
}

// WebhookJobRun is the executable instance of a webhook job.
type WebhookJobRun struct {
	config WebhookJobConfig
}

// Run executes the webhook job.
func (r *WebhookJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	jctx.Logger.InfoContext(ctx, "Sending webhook",
		"url", r.config.URL,
		"method", r.config.Method,
	)

	// Marshal body to JSON
	var bodyBytes []byte

	var err error

	if r.config.Body != nil {
		bodyBytes, err = json.Marshal(r.config.Body)
		if err != nil {
			return fmt.Errorf("marshaling webhook body: %w", err)
		}
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, r.config.Method, r.config.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SolidPing/"+version.Version)

	for key, value := range r.config.Headers {
		req.Header.Set(key, value)
	}

	// Send request with timeout
	client := &http.Client{
		Timeout: webhookTimeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		// Network errors are retryable
		return jobdef.NewRetryableError(fmt.Errorf("sending webhook: %w", err))
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.WarnContext(ctx, "Failed to close response body", "error", closeErr)
		}
	}()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.WarnContext(ctx, "Failed to read webhook response body", "error", err)
	}

	slog.InfoContext(ctx, "Webhook sent",
		"status_code", resp.StatusCode,
		"response_length", len(respBody),
	)

	// Check for success status codes
	if resp.StatusCode >= httpSuccessThreshold {
		return fmt.Errorf("%w: HTTP %d: %s", ErrWebhookHTTPError, resp.StatusCode, string(respBody))
	}

	return nil
}
