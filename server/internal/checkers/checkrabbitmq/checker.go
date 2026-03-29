// Package checkrabbitmq provides RabbitMQ server health checks.
package checkrabbitmq

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// RabbitMQChecker implements the Checker interface for RabbitMQ health checks.
type RabbitMQChecker struct{}

// Type returns the check type identifier.
func (c *RabbitMQChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRabbitMQ
}

// Validate checks if the configuration is valid.
func (c *RabbitMQChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &RabbitMQConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("RabbitMQ: %s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = "rabbitmq-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the RabbitMQ check and returns the result.
func (c *RabbitMQChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*RabbitMQConfig](config)
	if err != nil {
		return nil, err
	}

	mode := cfg.Mode
	if mode == "" {
		mode = defaultMode
	}

	if mode == ModeManagement {
		return c.executeManagement(ctx, cfg)
	}

	return c.executeAMQP(ctx, cfg)
}

// executeAMQP performs AMQP protocol health check.
func (c *RabbitMQChecker) executeAMQP(
	ctx context.Context,
	cfg *RabbitMQConfig,
) (*checkerdef.Result, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	metrics := map[string]any{}
	output := map[string]any{
		"host": cfg.Host,
		"port": port,
		"mode": ModeAMQP,
	}

	result := c.dialAndCheck(ctx, cfg, start, metrics, output)

	return result, nil
}

func (c *RabbitMQChecker) dialAndCheck(
	ctx context.Context,
	cfg *RabbitMQConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	uri := cfg.buildAMQPURI()

	amqpConfig := amqp.Config{}
	if cfg.TLS {
		amqpConfig.TLSClientConfig = &tls.Config{
			ServerName: cfg.Host,
		}
	}

	conn, err := amqp.DialConfig(uri, amqpConfig)
	if err != nil {
		return handleAMQPError(ctx, err, start, "connection failed", output)
	}

	defer func() { _ = conn.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	channel, err := conn.Channel()
	if err != nil {
		return handleAMQPError(ctx, err, start, "channel open failed", output)
	}

	defer func() { _ = channel.Close() }()

	if cfg.Queue != "" {
		if result := c.checkQueue(ctx, channel, cfg.Queue, start, metrics, output); result != nil {
			return result
		}
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *RabbitMQChecker) checkQueue(
	ctx context.Context,
	channel *amqp.Channel,
	queue string,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	queueInfo, err := channel.QueueDeclarePassive(queue, false, false, false, false, nil)
	if err != nil {
		return handleAMQPError(ctx, err, start, "queue inspect failed", output)
	}

	output["queue"] = queue
	metrics["queue_messages"] = queueInfo.Messages
	metrics["queue_consumers"] = queueInfo.Consumers

	return nil
}

// executeManagement performs HTTP management API health check.
func (c *RabbitMQChecker) executeManagement(
	ctx context.Context,
	cfg *RabbitMQConfig,
) (*checkerdef.Result, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	mgmtPort := cfg.ManagementPort
	if mgmtPort == 0 {
		mgmtPort = defaultManagementPort
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	output := map[string]any{
		"host": cfg.Host,
		"port": mgmtPort,
		"mode": ModeManagement,
	}

	return c.doManagementRequest(ctx, cfg, mgmtPort, start, output)
}

func (c *RabbitMQChecker) doManagementRequest(
	ctx context.Context,
	cfg *RabbitMQConfig,
	mgmtPort int,
	start time.Time,
	output map[string]any,
) (*checkerdef.Result, error) {
	scheme := "http"
	if cfg.TLS {
		scheme = "https"
	}

	healthURL := fmt.Sprintf("%s://%s:%d/api/health/checks/alarms", scheme, cfg.Host, mgmtPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{"error": "failed to create request: " + err.Error()}),
		}, nil
	}

	req.SetBasicAuth(cfg.Username, cfg.Password)

	metrics := map[string]any{}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return handleManagementError(ctx, err, start, output), nil
	}

	defer func() { _ = resp.Body.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	body, _ := io.ReadAll(resp.Body)
	metrics["total_time_ms"] = durationMs(time.Since(start))

	if resp.StatusCode != http.StatusOK {
		output["status_code"] = resp.StatusCode
		output["error"] = fmt.Sprintf("management API returned status %d: %s", resp.StatusCode, string(body))

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}, nil
	}

	output["status_code"] = resp.StatusCode

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func handleAMQPError(
	ctx context.Context,
	err error,
	start time.Time,
	prefix string,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{"error": "connection timeout"}),
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("%s: %v", prefix, err)}),
	}
}

func handleManagementError(
	ctx context.Context,
	err error,
	start time.Time,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{"error": "request timeout"}),
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   mergeOutput(output, map[string]any{"error": fmt.Sprintf("request failed: %v", err)}),
	}
}

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

func mergeOutput(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}

	for k, v := range extra {
		result[k] = v
	}

	return result
}
