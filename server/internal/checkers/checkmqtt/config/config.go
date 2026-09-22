// Package config holds the mqtt check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkmqtt package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// DefaultPort is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultPort    = 1883
	defaultTLSPort = 8883
	defaultTopic   = "solidping/healthcheck"
	// DefaultTimeout is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
)

// MQTTConfig holds the configuration for MQTT broker health checks.
type MQTTConfig struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username,omitempty"`
	Password string        `json:"password,omitempty"`
	Topic    string        `json:"topic,omitempty"`
	TLS      bool          `json:"tls,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *MQTTConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	if topic, ok := configMap["topic"].(string); ok {
		c.Topic = topic
	} else if configMap["topic"] != nil {
		return checkerdef.NewConfigError("topic", "must be a string")
	}

	if tlsVal, ok := configMap["tls"].(bool); ok {
		c.TLS = tlsVal
	} else if configMap["tls"] != nil {
		return checkerdef.NewConfigError("tls", "must be a boolean")
	}

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
func (c *MQTTConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != DefaultPort {
		cfg["port"] = c.Port
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Topic != "" && c.Topic != defaultTopic {
		cfg["topic"] = c.Topic
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *MQTTConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	if c.Topic != "" && (strings.Contains(c.Topic, "#") || strings.Contains(c.Topic, "+")) {
		return checkerdef.NewConfigError("topic", "must not contain wildcards (# or +)")
	}

	return nil
}

// BrokerURL builds the MQTT broker URL from the configuration.
func (c *MQTTConfig) BrokerURL() string {
	port := c.Port
	scheme := "tcp"

	if c.TLS {
		scheme = "ssl"

		if port == 0 {
			port = defaultTLSPort
		}
	} else if port == 0 {
		port = DefaultPort
	}

	return fmt.Sprintf("%s://%s:%d", scheme, c.Host, port)
}

// EffectiveTopic returns the configured topic, or the default when none is set.
func (c *MQTTConfig) EffectiveTopic() string {
	if c.Topic != "" {
		return c.Topic
	}

	return defaultTopic
}
