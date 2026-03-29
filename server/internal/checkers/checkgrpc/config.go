package checkgrpc

import (
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort    = 50051
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
)

// GRPCConfig holds the configuration for gRPC health checks.
type GRPCConfig struct {
	Host          string        `json:"host"`
	Port          int           `json:"port,omitempty"`
	TLS           bool          `json:"tls,omitempty"`
	TLSSkipVerify bool          `json:"tlsSkipVerify,omitempty"`
	ServiceName   string        `json:"serviceName,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	Keyword       string        `json:"keyword,omitempty"`
	InvertKeyword bool          `json:"invertKeyword,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *GRPCConfig) FromMap(configMap map[string]any) error {
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

	if tls, ok := configMap["tls"].(bool); ok {
		c.TLS = tls
	}

	if tlsSkipVerify, ok := configMap["tlsSkipVerify"].(bool); ok {
		c.TLSSkipVerify = tlsSkipVerify
	}

	if serviceName, ok := configMap["serviceName"].(string); ok {
		c.ServiceName = serviceName
	} else if configMap["serviceName"] != nil {
		return checkerdef.NewConfigError("serviceName", "must be a string")
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

	if keyword, ok := configMap["keyword"].(string); ok {
		c.Keyword = keyword
	} else if configMap["keyword"] != nil {
		return checkerdef.NewConfigError("keyword", "must be a string")
	}

	if invertKeyword, ok := configMap["invertKeyword"].(bool); ok {
		c.InvertKeyword = invertKeyword
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *GRPCConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.TLSSkipVerify {
		cfg["tlsSkipVerify"] = c.TLSSkipVerify
	}

	if c.ServiceName != "" {
		cfg["serviceName"] = c.ServiceName
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Keyword != "" {
		cfg["keyword"] = c.Keyword
	}

	if c.InvertKeyword {
		cfg["invertKeyword"] = c.InvertKeyword
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *GRPCConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	return nil
}

func (c *GRPCConfig) resolvePort() int {
	if c.Port != 0 {
		return c.Port
	}

	return defaultPort
}

func (c *GRPCConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

func (c *GRPCConfig) resolveTarget() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.resolvePort()))
}
