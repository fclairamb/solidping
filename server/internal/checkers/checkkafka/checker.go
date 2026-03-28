package checkkafka

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/IBM/sarama"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// KafkaChecker implements the Checker interface for Kafka cluster health checks.
type KafkaChecker struct{}

// Type returns the check type identifier.
func (c *KafkaChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeKafka
}

// Validate checks if the configuration is valid.
func (c *KafkaChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &KafkaConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" && len(cfg.Brokers) > 0 {
		spec.Name = cfg.Brokers[0]
	}

	if spec.Slug == "" && len(cfg.Brokers) > 0 {
		host := cfg.Brokers[0]
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}

		spec.Slug = "kafka-" + strings.ReplaceAll(host, ".", "-")
	}

	return nil
}

// Execute performs the Kafka cluster health check and returns the result.
func (c *KafkaChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, ok := config.(*KafkaConfig)
	if !ok {
		return nil, ErrInvalidConfigType
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	metrics := map[string]any{}
	output := map[string]any{
		"brokers": cfg.Brokers,
	}

	saramaCfg := c.buildSaramaConfig(cfg, timeout)

	client, err := sarama.NewClient(cfg.Brokers, saramaCfg)
	if err != nil {
		return handleConnectError(ctx, err, start), nil
	}

	defer func() { _ = client.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	result := c.collectMetadata(client, output)
	if result != nil {
		result.Duration = time.Since(start)
		result.Metrics = metrics

		return result, nil
	}

	if cfg.Topic != "" {
		if topicResult := c.checkTopic(client, cfg.Topic, output); topicResult != nil {
			topicResult.Duration = time.Since(start)
			topicResult.Metrics = metrics

			return topicResult, nil
		}
	}

	if cfg.ProduceTest && cfg.Topic != "" {
		if produceResult := c.runProduceTest(client, cfg, start, metrics, output); produceResult != nil {
			return produceResult, nil
		}
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func (c *KafkaChecker) buildSaramaConfig(cfg *KafkaConfig, timeout time.Duration) *sarama.Config {
	saramaCfg := sarama.NewConfig()
	saramaCfg.Net.DialTimeout = timeout
	saramaCfg.Net.ReadTimeout = timeout
	saramaCfg.Net.WriteTimeout = timeout
	saramaCfg.Producer.Return.Successes = true

	if cfg.TLS {
		saramaCfg.Net.TLS.Enable = true
		saramaCfg.Net.TLS.Config = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify,
		}
	}

	configureSASL(saramaCfg, cfg)

	return saramaCfg
}

func configureSASL(saramaCfg *sarama.Config, cfg *KafkaConfig) {
	if cfg.SASLMechanism == "" {
		return
	}

	saramaCfg.Net.SASL.Enable = true
	saramaCfg.Net.SASL.User = cfg.SASLUsername
	saramaCfg.Net.SASL.Password = cfg.SASLPassword

	switch cfg.SASLMechanism {
	case "PLAIN":
		saramaCfg.Net.SASL.Mechanism = sarama.SASLTypePlaintext
	case "SCRAM-SHA-256":
		saramaCfg.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA256
		saramaCfg.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient {
			return &scramClient{hashGen: newSHA256Generator()}
		}
	case "SCRAM-SHA-512":
		saramaCfg.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA512
		saramaCfg.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient {
			return &scramClient{hashGen: newSHA512Generator()}
		}
	}
}

func (c *KafkaChecker) collectMetadata(
	client sarama.Client,
	output map[string]any,
) *checkerdef.Result {
	brokers := client.Brokers()
	output["brokerCount"] = len(brokers)

	controller, err := client.Controller()
	if err != nil {
		output["error"] = "failed to get controller: " + err.Error()

		return &checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: output,
		}
	}

	output["controllerID"] = controller.ID()

	return nil
}

func (c *KafkaChecker) checkTopic(
	client sarama.Client,
	topic string,
	output map[string]any,
) *checkerdef.Result {
	topics, err := client.Topics()
	if err != nil {
		output["error"] = "failed to list topics: " + err.Error()

		return &checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: output,
		}
	}

	found := false

	for _, t := range topics {
		if t == topic {
			found = true

			break
		}
	}

	output["topicExists"] = found

	if !found {
		output["error"] = fmt.Sprintf("topic %q not found", topic)

		return &checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: output,
		}
	}

	return nil
}

func (c *KafkaChecker) runProduceTest(
	client sarama.Client,
	cfg *KafkaConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	produceStart := time.Now()

	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		output["error"] = "failed to create producer: " + err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	defer func() { _ = producer.Close() }()

	msg := &sarama.ProducerMessage{
		Topic: cfg.Topic,
		Value: sarama.StringEncoder(fmt.Sprintf("solidping-health-check-%d", time.Now().UnixNano())),
	}

	partition, offset, err := producer.SendMessage(msg)
	if err != nil {
		output["error"] = "failed to produce message: " + err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	metrics["produce_time_ms"] = durationMs(time.Since(produceStart))
	output["producePartition"] = partition
	output["produceOffset"] = offset

	return nil
}

func handleConnectError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{"error": "failed to connect: " + err.Error()},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
