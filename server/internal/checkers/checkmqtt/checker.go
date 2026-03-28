package checkmqtt

import (
	"context"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// MQTTChecker implements the Checker interface for MQTT broker health checks.
type MQTTChecker struct{}

// Type returns the check type identifier.
func (c *MQTTChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMQTT
}

// Validate checks if the configuration is valid.
func (c *MQTTChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &MQTTConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		if cfg.TLS {
			port = defaultTLSPort
		} else {
			port = defaultPort
		}
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d", cfg.Host, port)
	}

	if spec.Slug == "" {
		spec.Slug = "mqtt-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the MQTT broker health check and returns the result.
func (c *MQTTChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, ok := config.(*MQTTConfig)
	if !ok {
		return nil, ErrInvalidConfigType
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		"host":  cfg.Host,
		"topic": cfg.topic(),
	}

	// Connect to broker
	connectResult := c.connect(ctx, cfg, timeout)
	if connectResult.Status != checkerdef.StatusUp {
		connectResult.Duration = time.Since(start)

		return &connectResult, nil
	}

	// Merge connect metrics
	for k, v := range connectResult.Metrics {
		metrics[k] = v
	}

	client, ok := connectResult.Output["_client"].(mqtt.Client)
	if !ok {
		return nil, ErrInvalidConfigType
	}

	defer client.Disconnect(0)

	delete(connectResult.Output, "_client")

	// Publish/subscribe roundtrip
	rtResult := c.roundtrip(ctx, client, cfg, timeout)
	for k, v := range rtResult.Metrics {
		metrics[k] = v
	}

	for k, v := range rtResult.Output {
		output[k] = v
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   rtResult.Status,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

// connect establishes a connection to the MQTT broker.
func (c *MQTTChecker) connect(
	_ context.Context,
	cfg *MQTTConfig,
	timeout time.Duration,
) checkerdef.Result {
	clientID := fmt.Sprintf("solidping-check-%d", time.Now().UnixNano())

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.brokerURL()).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectTimeout(timeout)

	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}

	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}

	connectStart := time.Now()
	client := mqtt.NewClient(opts)

	token := client.Connect()
	if !token.WaitTimeout(timeout) {
		return checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{"error": "connection timeout"},
		}
	}

	if token.Error() != nil {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{"error": "connection failed: " + token.Error().Error()},
		}
	}

	return checkerdef.Result{
		Status: checkerdef.StatusUp,
		Metrics: map[string]any{
			"connection_time_ms": durationMs(time.Since(connectStart)),
		},
		Output: map[string]any{
			"_client": client,
		},
	}
}

// roundtrip performs a publish/subscribe roundtrip test on the MQTT broker.
func (c *MQTTChecker) roundtrip(
	_ context.Context,
	client mqtt.Client,
	cfg *MQTTConfig,
	timeout time.Duration,
) checkerdef.Result {
	topic := cfg.topic()
	testMessage := fmt.Sprintf("solidping-check-%d", time.Now().UnixNano())
	received := make(chan struct{}, 1)

	// Subscribe
	subToken := client.Subscribe(topic, 1, func(_ mqtt.Client, msg mqtt.Message) {
		if string(msg.Payload()) == testMessage {
			select {
			case received <- struct{}{}:
			default:
			}
		}
	})

	if !subToken.WaitTimeout(timeout) {
		return checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{"error": "subscribe timeout"},
		}
	}

	if subToken.Error() != nil {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{"error": "subscribe failed: " + subToken.Error().Error()},
		}
	}

	// Publish
	rtStart := time.Now()

	pubToken := client.Publish(topic, 1, false, testMessage)
	if !pubToken.WaitTimeout(timeout) {
		return checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{"error": "publish timeout"},
		}
	}

	if pubToken.Error() != nil {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{"error": "publish failed: " + pubToken.Error().Error()},
		}
	}

	// Wait for message
	select {
	case <-received:
		return checkerdef.Result{
			Status: checkerdef.StatusUp,
			Metrics: map[string]any{
				"roundtrip_time_ms": durationMs(time.Since(rtStart)),
			},
		}
	case <-time.After(timeout):
		return checkerdef.Result{
			Status:  checkerdef.StatusTimeout,
			Metrics: map[string]any{},
			Output:  map[string]any{"error": "message roundtrip timeout"},
		}
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
