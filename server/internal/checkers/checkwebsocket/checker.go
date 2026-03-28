// Package checkwebsocket provides WebSocket connectivity checks.
package checkwebsocket

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nhooyr.io/websocket" //nolint:staticcheck // using nhooyr.io/websocket v1

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// WebSocketChecker implements the Checker interface for WebSocket checks.
type WebSocketChecker struct{}

// Type returns the check type identifier.
func (c *WebSocketChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeWebSocket
}

// Validate checks if the configuration is valid.
func (c *WebSocketChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &WebSocketConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.URL == "" {
		return checkerdef.NewConfigError("url", "URL is required")
	}

	if !strings.HasPrefix(cfg.URL, "ws://") && !strings.HasPrefix(cfg.URL, "wss://") {
		return checkerdef.NewConfigError("url", "must start with ws:// or wss://")
	}

	if cfg.Expect != "" {
		if _, err := regexp.Compile(cfg.Expect); err != nil {
			return checkerdef.NewConfigErrorf("expect", "invalid regex pattern: %s", err.Error())
		}
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String(),
		)
	}

	if spec.Name == "" {
		host := hostFromURL(cfg.URL)
		spec.Name = "WebSocket: " + host
	}

	if spec.Slug == "" {
		host := hostFromURL(cfg.URL)
		spec.Slug = "ws-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
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

	defer func() { _ = conn.CloseNow() }() //nolint:staticcheck // using nhooyr.io/websocket v1

	output := map[string]any{
		"url": cfg.URL,
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
	_ = conn.Close(websocket.StatusNormalClosure, "") //nolint:staticcheck // using nhooyr.io/websocket v1

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: totalTime,
		Metrics:  metrics,
		Output:   output,
	}, nil
}

//nolint:staticcheck // using nhooyr.io/websocket v1
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

	if cfg.TLSSkipVerify {
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: true, //nolint:staticcheck // User-configured TLS skip
				},
			},
		}
	}

	conn, resp, err := websocket.Dial(ctx, cfg.URL, opts) //nolint:staticcheck // using nhooyr.io/websocket v1
	if err != nil {
		return nil, resp, fmt.Errorf("dial: %w", err)
	}

	return conn, resp, nil
}

//nolint:staticcheck // using nhooyr.io/websocket v1
func (c *WebSocketChecker) sendAndExpect(
	ctx context.Context, conn *websocket.Conn, cfg *WebSocketConfig, output map[string]any,
) error {
	if cfg.Send != "" {
		if err := conn.Write(ctx, websocket.MessageText, []byte(cfg.Send)); err != nil { //nolint:staticcheck // v1
			output["error"] = fmt.Sprintf("failed to send message: %v", err)

			return fmt.Errorf("write: %w", err)
		}
	}

	if cfg.Expect != "" {
		_, msg, err := conn.Read(ctx) //nolint:staticcheck // using nhooyr.io/websocket v1
		if err != nil {
			output["error"] = fmt.Sprintf("failed to read message: %v", err)

			return fmt.Errorf("read: %w", err)
		}

		received := string(msg)
		output["received"] = received

		pattern, _ := regexp.Compile(cfg.Expect) // Already validated in Validate()

		if !pattern.MatchString(received) {
			output["error"] = fmt.Sprintf(
				"response did not match expected pattern %q", cfg.Expect,
			)

			return errPatternMismatch
		}
	}

	return nil
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
