// Package checkwebsocket provides WebSocket connectivity checks.
package checkwebsocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket/config"
)

const (
	microsecondsPerMilli = 1000.0
	maxReadAttempts      = 8 // cap reads so a chatty server can't consume the full timeout
)

// WebSocketChecker implements the Checker interface for WebSocket checks.
type WebSocketChecker struct{}

// Type returns the check type identifier.
func (c *WebSocketChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeWebSocket
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *WebSocketChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the WebSocket check and returns the result.
func (c *WebSocketChecker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*WebSocketConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	conn, resp, err := c.dial(ctx, cfg)

	handshakeTime := time.Since(start)

	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	if err != nil {
		return c.handleDialError(ctx, err, start), nil
	}

	defer func() { _ = conn.CloseNow() }()

	output := map[string]any{
		configKeyURL: cfg.URL,
	}

	if checkerdef.TunnelDialerFrom(ctx) != nil {
		output["tunneled"] = true
	}

	if resp != nil {
		output["status_code"] = resp.StatusCode
	}

	if err := c.sendAndExpect(ctx, conn, cfg, output); err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics: map[string]any{
				"handshake_time_ms": durationMs(handshakeTime),
				"total_time_ms":     durationMs(time.Since(start)),
			},
			Output: output,
		}, nil
	}

	totalTime := time.Since(start)

	metrics := map[string]any{
		"handshake_time_ms": durationMs(handshakeTime),
		"total_time_ms":     durationMs(totalTime),
	}

	// Close cleanly
	_ = conn.Close(websocket.StatusNormalClosure, "")

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: totalTime,
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func (c *WebSocketChecker) dial(
	ctx context.Context, cfg *WebSocketConfig,
) (*websocket.Conn, *http.Response, error) {
	opts := &websocket.DialOptions{}

	if len(cfg.Headers) > 0 {
		header := http.Header{}
		for key, val := range cfg.Headers {
			header.Set(key, val)
		}

		opts.HTTPHeader = header
	}

	transport := &http.Transport{}
	needClient := false

	if cfg.TLSSkipVerify {
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
		needClient = true
	}

	// Tunneled: route the WebSocket handshake's underlying dial through the
	// bastion. http.Transport hands the raw host:port to DialContext (no local
	// resolution), so the bastion resolves private names. Untunneled, no custom
	// client is set and coder/websocket uses its default byte-for-byte —
	// unless the egress policy (spec 2026-09-25-19) is enforcing, in which case
	// the handshake dials through the guard.
	if dialer := checkerdef.OutboundDialer(ctx); dialer != nil {
		transport.DialContext = dialer.DialContext
		needClient = true
	}

	if needClient {
		opts.HTTPClient = &http.Client{Transport: transport}
	}

	conn, resp, err := websocket.Dial(ctx, cfg.URL, opts)
	if err != nil {
		return nil, resp, fmt.Errorf("dial: %w", err)
	}

	return conn, resp, nil
}

func (c *WebSocketChecker) sendAndExpect(
	ctx context.Context, conn *websocket.Conn, cfg *WebSocketConfig, output map[string]any,
) error {
	if cfg.Send != "" {
		if err := conn.Write(ctx, websocket.MessageText, []byte(cfg.Send)); err != nil {
			output["error"] = fmt.Sprintf("failed to send message: %v", err)

			return fmt.Errorf("write: %w", err)
		}
	}

	if cfg.Expect == "" {
		return nil
	}

	pattern, _ := regexp.Compile(cfg.Expect) // Already validated in Validate()

	var last string

	for attempt := 0; attempt < maxReadAttempts; attempt++ {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			output["error"] = fmt.Sprintf("failed to read message: %v", err)
			if last != "" {
				output["received"] = last
			}
			if attempt > 0 {
				output["received_skipped"] = attempt
			}

			return fmt.Errorf("read: %w", err)
		}

		last = string(msg)
		if pattern.MatchString(last) {
			output["received"] = last
			if attempt > 0 {
				output["received_skipped"] = attempt
			}

			return nil
		}
	}

	output["received"] = last
	output["received_skipped"] = maxReadAttempts
	output["error"] = fmt.Sprintf(
		"response did not match expected pattern %q after %d frames", cfg.Expect, maxReadAttempts,
	)

	return errPatternMismatch
}

func (c *WebSocketChecker) handleDialError(
	ctx context.Context, err error, start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)

	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: duration,
			Output:   map[string]any{"error": "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: duration,
		Output:   map[string]any{"error": fmt.Sprintf("connection failed: %v", err)},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
