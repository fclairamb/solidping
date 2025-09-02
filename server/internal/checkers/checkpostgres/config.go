package checkpostgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort     = 5432
	defaultTimeout  = 10 * time.Second
	maxTimeout      = 60 * time.Second
	defaultDatabase = "postgres"
	defaultSSLMode  = "prefer"
	defaultQuery    = "SELECT 1"
)

func isValidSSLMode(mode string) bool {
	switch mode {
	case "disable", "require", "verify-ca", "verify-full", "prefer":
		return true
	default:
		return false
	}
}

// PostgreSQLConfig holds the configuration for PostgreSQL database checks.
type PostgreSQLConfig struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username"`
	Password string        `json:"password,omitempty"`
	Database string        `json:"database,omitempty"`
	SSLMode  string        `json:"ssl_mode,omitempty"` //nolint:tagliatelle // API uses snake_case
	Timeout  time.Duration `json:"timeout,omitempty"`
	Query    string        `json:"query,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *PostgreSQLConfig) FromMap(configMap map[string]any) error {
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

	if sslMode, ok := configMap["ssl_mode"].(string); ok {
		c.SSLMode = sslMode
	} else if configMap["ssl_mode"] != nil {
		return checkerdef.NewConfigError("ssl_mode", "must be a string")
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

	if query, ok := configMap["query"].(string); ok {
		c.Query = query
	} else if configMap["query"] != nil {
		return checkerdef.NewConfigError("query", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *PostgreSQLConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host":     c.Host,
		"username": c.Username,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.Database != "" && c.Database != defaultDatabase {
		cfg["database"] = c.Database
	}

	if c.SSLMode != "" && c.SSLMode != defaultSSLMode {
		cfg["ssl_mode"] = c.SSLMode
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Query != "" && c.Query != defaultQuery {
		cfg["query"] = c.Query
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *PostgreSQLConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	if c.SSLMode != "" && !isValidSSLMode(c.SSLMode) {
		return checkerdef.NewConfigErrorf(
			"ssl_mode",
			"must be one of: disable, require, verify-ca, verify-full, prefer; got %q",
			c.SSLMode,
		)
	}

	if c.Query != "" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Query)), "SELECT") {
		return checkerdef.NewConfigError("query", "must start with SELECT")
	}

	return nil
}

// buildConnStr builds a PostgreSQL connection string from the configuration.
func (c *PostgreSQLConfig) buildConnStr() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	database := c.Database
	if database == "" {
		database = defaultDatabase
	}

	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = defaultSSLMode
	}

	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s dbname=%s sslmode=%s",
		escapePQParam(c.Host),
		port,
		escapePQParam(c.Username),
		escapePQParam(database),
		escapePQParam(sslMode),
	)

	if c.Password != "" {
		connStr += " password=" + escapePQParam(c.Password)
	}

	return connStr
}

// escapePQParam escapes a value for use in a PostgreSQL connection string.
// Single quotes are escaped by doubling them, and the value is wrapped in quotes
// if it contains spaces or special characters.
func escapePQParam(value string) string {
	if value == "" {
		return "''"
	}

	needsQuoting := strings.ContainsAny(value, " '\\")
	if !needsQuoting {
		return value
	}

	escaped := strings.ReplaceAll(value, "'", "\\'")

	return "'" + escaped + "'"
}
