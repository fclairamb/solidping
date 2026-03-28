package checkkafka

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
)

// KafkaConfig holds the configuration for Kafka cluster health checks.
type KafkaConfig struct {
	Brokers        []string      `json:"brokers"`
	Topic          string        `json:"topic,omitempty"`
	SASLMechanism  string        `json:"saslMechanism,omitempty"`
	SASLUsername   string        `json:"saslUsername,omitempty"`
	SASLPassword   string        `json:"saslPassword,omitempty"`
	TLS            bool          `json:"tls,omitempty"`
	TLSSkipVerify  bool          `json:"tlsSkipVerify,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	ProduceTest    bool          `json:"produceTest,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *KafkaConfig) FromMap(configMap map[string]any) error {
	if err := c.parseBrokers(configMap); err != nil {
		return err
	}

	if err := c.parseStringFields(configMap); err != nil {
		return err
	}

	if err := c.parseBoolFields(configMap); err != nil {
		return err
	}

	return c.parseTimeout(configMap)
}

func (c *KafkaConfig) parseBrokers(configMap map[string]any) error {
	switch v := configMap["brokers"].(type) {
	case []any:
		brokers := make([]string, 0, len(v))

		for _, b := range v {
			if s, ok := b.(string); ok {
				brokers = append(brokers, s)
			}
		}

		c.Brokers = brokers
	case string:
		if v != "" {
			c.Brokers = strings.Split(v, ",")
		}
	case nil:
		// no brokers specified
	default:
		return checkerdef.NewConfigError("brokers", "must be an array of strings or a comma-separated string")
	}

	return nil
}

func (c *KafkaConfig) parseStringFields(configMap map[string]any) error {
	if topic, ok := configMap["topic"].(string); ok {
		c.Topic = topic
	} else if configMap["topic"] != nil {
		return checkerdef.NewConfigError("topic", "must be a string")
	}

	if mechanism, ok := configMap["saslMechanism"].(string); ok {
		c.SASLMechanism = mechanism
	} else if configMap["saslMechanism"] != nil {
		return checkerdef.NewConfigError("saslMechanism", "must be a string")
	}

	if username, ok := configMap["saslUsername"].(string); ok {
		c.SASLUsername = username
	} else if configMap["saslUsername"] != nil {
		return checkerdef.NewConfigError("saslUsername", "must be a string")
	}

	if password, ok := configMap["saslPassword"].(string); ok {
		c.SASLPassword = password
	} else if configMap["saslPassword"] != nil {
		return checkerdef.NewConfigError("saslPassword", "must be a string")
	}

	return nil
}

func (c *KafkaConfig) parseBoolFields(configMap map[string]any) error {
	if tlsVal, ok := configMap["tls"].(bool); ok {
		c.TLS = tlsVal
	} else if configMap["tls"] != nil {
		return checkerdef.NewConfigError("tls", "must be a boolean")
	}

	if skipVerify, ok := configMap["tlsSkipVerify"].(bool); ok {
		c.TLSSkipVerify = skipVerify
	} else if configMap["tlsSkipVerify"] != nil {
		return checkerdef.NewConfigError("tlsSkipVerify", "must be a boolean")
	}

	if produce, ok := configMap["produceTest"].(bool); ok {
		c.ProduceTest = produce
	} else if configMap["produceTest"] != nil {
		return checkerdef.NewConfigError("produceTest", "must be a boolean")
	}

	return nil
}

func (c *KafkaConfig) parseTimeout(configMap map[string]any) error {
	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *KafkaConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"brokers": c.Brokers,
	}

	if c.Topic != "" {
		cfg["topic"] = c.Topic
	}

	if c.SASLMechanism != "" {
		cfg["saslMechanism"] = c.SASLMechanism
	}

	if c.SASLUsername != "" {
		cfg["saslUsername"] = c.SASLUsername
	}

	if c.SASLPassword != "" {
		cfg["saslPassword"] = c.SASLPassword
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.TLSSkipVerify {
		cfg["tlsSkipVerify"] = c.TLSSkipVerify
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.ProduceTest {
		cfg["produceTest"] = c.ProduceTest
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *KafkaConfig) Validate() error {
	if len(c.Brokers) == 0 {
		return checkerdef.NewConfigError("brokers", "at least one broker is required")
	}

	for _, broker := range c.Brokers {
		if !strings.Contains(broker, ":") {
			return checkerdef.NewConfigErrorf("brokers", "broker %q must contain a port (host:port)", broker)
		}
	}

	if err := c.validateSASL(); err != nil {
		return err
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	if c.ProduceTest && c.Topic == "" {
		return checkerdef.NewConfigError("topic", "is required when produceTest is enabled")
	}

	return nil
}

func (c *KafkaConfig) validateSASL() error {
	if c.SASLMechanism != "" {
		validMechanisms := map[string]bool{
			"PLAIN":          true,
			"SCRAM-SHA-256":  true,
			"SCRAM-SHA-512":  true,
		}

		if !validMechanisms[c.SASLMechanism] {
			return checkerdef.NewConfigError(
				"saslMechanism", "must be one of: PLAIN, SCRAM-SHA-256, SCRAM-SHA-512",
			)
		}

		if c.SASLUsername == "" {
			return checkerdef.NewConfigError("saslUsername", "is required when saslMechanism is set")
		}

		if c.SASLPassword == "" {
			return checkerdef.NewConfigError("saslPassword", "is required when saslMechanism is set")
		}
	}

	return nil
}
