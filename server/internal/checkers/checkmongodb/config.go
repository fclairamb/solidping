package checkmongodb

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort    = 27017
	defaultTimeout = 10 * time.Second
	maxTimeout     = 60 * time.Second
)

// MongoDBConfig holds the configuration for MongoDB health checks.
type MongoDBConfig struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username,omitempty"`
	Password string        `json:"password,omitempty"`
	Database string        `json:"database,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *MongoDBConfig) FromMap(configMap map[string]any) error {
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

	if database, ok := configMap["database"].(string); ok {
		c.Database = database
	} else if configMap["database"] != nil {
		return checkerdef.NewConfigError("database", "must be a string")
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
func (c *MongoDBConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Database != "" {
		cfg["database"] = c.Database
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *MongoDBConfig) Validate() error {
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

// buildURI builds a MongoDB connection URI from the configuration.
func (c *MongoDBConfig) buildURI() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	hostPort := net.JoinHostPort(c.Host, strconv.Itoa(port))

	if c.Username != "" && c.Password != "" {
		return fmt.Sprintf("mongodb://%s:%s@%s", c.Username, c.Password, hostPort)
	}

	return "mongodb://" + hostPort
}
