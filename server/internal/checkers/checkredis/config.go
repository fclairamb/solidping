package checkredis

import (
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort    = 6379
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
)

// RedisConfig holds the configuration for Redis health checks.
type RedisConfig struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Password string        `json:"password,omitempty"`
	Database int           `json:"database,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *RedisConfig) FromMap(configMap map[string]any) error {
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

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	if db, ok := configMap["database"].(int); ok {
		c.Database = db
	} else if dbFloat, ok := configMap["database"].(float64); ok {
		c.Database = int(dbFloat)
	} else if configMap["database"] != nil {
		return checkerdef.NewConfigError("database", "must be a number")
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
func (c *RedisConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Database != 0 {
		cfg["database"] = c.Database
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *RedisConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Database < 0 || c.Database > 15 {
		return checkerdef.NewConfigErrorf("database", "must be between 0 and 15, got %d", c.Database)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	return nil
}

// addr returns the Redis address in host:port format.
func (c *RedisConfig) addr() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	return fmt.Sprintf("%s:%d", c.Host, port)
}
