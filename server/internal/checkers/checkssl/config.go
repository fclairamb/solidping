package checkssl

import (
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var errHostRequired = errors.New("host is required")

const (
	defaultPort          = 443
	defaultThresholdDays = 30
	defaultTimeout       = 10 * time.Second
	maxTimeout           = 60 * time.Second
)

// SSLConfig defines the configuration for SSL certificate checks.
type SSLConfig struct {
	// Host is the target hostname to connect to.
	Host string `json:"host"`

	// Port is the TCP port to connect to (default: 443).
	Port int `json:"port"`

	// ThresholdDays is the number of days before expiration to mark as down (default: 30).
	ThresholdDays int `json:"thresholdDays"`

	// Timeout is the maximum time for connection + handshake (default: 10s).
	Timeout time.Duration `json:"timeout,omitempty"`

	// ServerName overrides the SNI server name (defaults to Host).
	ServerName string `json:"serverName"`
}

// FromMap populates the configuration from a map.
func (c *SSLConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	}

	if port, ok := configMap["port"].(float64); ok {
		c.Port = int(port)
	} else if port, ok := configMap["port"].(int); ok {
		c.Port = port
	}

	if threshold, ok := configMap["threshold_days"].(float64); ok {
		c.ThresholdDays = int(threshold)
	} else if threshold, ok := configMap["threshold_days"].(int); ok {
		c.ThresholdDays = threshold
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	}

	if serverName, ok := configMap["server_name"].(string); ok {
		c.ServerName = serverName
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SSLConfig) GetConfig() map[string]any {
	config := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		config["port"] = c.Port
	}

	if c.ThresholdDays != 0 {
		config["threshold_days"] = c.ThresholdDays
	}

	if c.Timeout != 0 {
		config["timeout"] = c.Timeout.String()
	}

	if c.ServerName != "" {
		config["server_name"] = c.ServerName
	}

	return config
}

// Validate checks if the configuration is valid.
func (c *SSLConfig) Validate() error {
	if c.Host == "" {
		return errHostRequired
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigError("port", "must be between 1 and 65535")
	}

	if c.ThresholdDays < 0 {
		return checkerdef.NewConfigError("threshold_days", "must be >= 0")
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	return nil
}
