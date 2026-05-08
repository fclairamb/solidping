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

// FromMap populates the configuration from a map. Accepts both camelCase
// (canonical, matching JSON tags) and snake_case (legacy) for thresholdDays
// and serverName so existing rows keep working.
func (c *SSLConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	}

	if port, ok := configMap["port"].(float64); ok {
		c.Port = int(port)
	} else if port, ok := configMap["port"].(int); ok {
		c.Port = port
	}

	if threshold, ok := readIntKey(configMap, "thresholdDays", "threshold_days"); ok {
		c.ThresholdDays = threshold
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	}

	if serverName, ok := readStringKey(configMap, "serverName", "server_name"); ok {
		c.ServerName = serverName
	}

	return nil
}

// readIntKey returns the first integer value found at any of the supplied keys.
func readIntKey(configMap map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		switch v := configMap[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}

	return 0, false
}

// readStringKey returns the first non-empty string value found at any of the supplied keys.
func readStringKey(configMap map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s, ok := configMap[key].(string); ok && s != "" {
			return s, true
		}
	}

	return "", false
}

// GetConfig returns the configuration as a map using the canonical camelCase
// keys that match the JSON tags (and the keys the dash0 form writes).
func (c *SSLConfig) GetConfig() map[string]any {
	config := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		config["port"] = c.Port
	}

	if c.ThresholdDays != 0 {
		config["thresholdDays"] = c.ThresholdDays
	}

	if c.Timeout != 0 {
		config["timeout"] = c.Timeout.String()
	}

	if c.ServerName != "" {
		config["serverName"] = c.ServerName
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
