package checkrabbitmq

import (
	"fmt"
	"net/url"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort           = 5672
	defaultTimeout        = 10 * time.Second
	maxTimeout            = 60 * time.Second
	defaultVhost          = "/"
	defaultMode           = ModeAMQP
	defaultManagementPort = 15672
)

// Mode constants for RabbitMQ check modes.
const (
	ModeAMQP       = "amqp"
	ModeManagement = "management"
)

// RabbitMQConfig holds the configuration for RabbitMQ health checks.
type RabbitMQConfig struct {
	Host           string        `json:"host"`
	Port           int           `json:"port,omitempty"`
	Username       string        `json:"username"`
	Password       string        `json:"password,omitempty"`
	Vhost          string        `json:"vhost,omitempty"`
	TLS            bool          `json:"tls,omitempty"`
	Mode           string        `json:"mode,omitempty"`
	ManagementPort int           `json:"managementPort,omitempty"`
	Queue          string        `json:"queue,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *RabbitMQConfig) FromMap(configMap map[string]any) error {
	if err := c.parseStringFields(configMap); err != nil {
		return err
	}

	if err := c.parsePortFields(configMap); err != nil {
		return err
	}

	if err := c.parseBoolAndDurationFields(configMap); err != nil {
		return err
	}

	return nil
}

func (c *RabbitMQConfig) parseStringFields(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	} else if configMap[checkerdef.OutputKeyHost] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
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

	if vhost, ok := configMap["vhost"].(string); ok {
		c.Vhost = vhost
	} else if configMap["vhost"] != nil {
		return checkerdef.NewConfigError("vhost", "must be a string")
	}

	if mode, ok := configMap["mode"].(string); ok {
		c.Mode = mode
	} else if configMap["mode"] != nil {
		return checkerdef.NewConfigError("mode", "must be a string")
	}

	if queue, ok := configMap["queue"].(string); ok {
		c.Queue = queue
	} else if configMap["queue"] != nil {
		return checkerdef.NewConfigError("queue", "must be a string")
	}

	return nil
}

func (c *RabbitMQConfig) parsePortFields(configMap map[string]any) error {
	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if mp, ok := configMap["managementPort"].(int); ok {
		c.ManagementPort = mp
	} else if mpFloat, ok := configMap["managementPort"].(float64); ok {
		c.ManagementPort = int(mpFloat)
	} else if configMap["managementPort"] != nil {
		return checkerdef.NewConfigError("managementPort", "must be a number")
	}

	return nil
}

func (c *RabbitMQConfig) parseBoolAndDurationFields(configMap map[string]any) error {
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
func (c *RabbitMQConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
		"username":               c.Username,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Vhost != "" && c.Vhost != defaultVhost {
		cfg["vhost"] = c.Vhost
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.Mode != "" && c.Mode != defaultMode {
		cfg["mode"] = c.Mode
	}

	if c.ManagementPort != 0 && c.ManagementPort != defaultManagementPort {
		cfg["managementPort"] = c.ManagementPort
	}

	if c.Queue != "" {
		cfg["queue"] = c.Queue
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *RabbitMQConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "is required")
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.ManagementPort != 0 && (c.ManagementPort < 1 || c.ManagementPort > 65535) {
		return checkerdef.NewConfigErrorf("managementPort", "must be between 1 and 65535, got %d", c.ManagementPort)
	}

	if c.Mode != "" && c.Mode != ModeAMQP && c.Mode != ModeManagement {
		return checkerdef.NewConfigErrorf("mode", "must be %q or %q, got %q", ModeAMQP, ModeManagement, c.Mode)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	return nil
}

// buildAMQPURI builds an AMQP connection URI from the configuration.
func (c *RabbitMQConfig) buildAMQPURI() string {
	scheme := "amqp"
	if c.TLS {
		scheme = "amqps"
	}

	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	vhost := c.Vhost
	if vhost == "" {
		vhost = defaultVhost
	}

	amqpURL := &url.URL{
		Scheme: scheme,
		User:   url.UserPassword(c.Username, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, port),
		Path:   vhost,
	}

	return amqpURL.String()
}
