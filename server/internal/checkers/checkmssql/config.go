package checkmssql

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort     = 1433
	defaultTimeout  = 10 * time.Second
	maxTimeout      = 60 * time.Second
	defaultQuery    = "SELECT 1"
	defaultDatabase = "master"
	defaultEncrypt  = "false"
)

// MSSQLConfig holds the configuration for Microsoft SQL Server checks.
type MSSQLConfig struct {
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username"`
	Password string        `json:"password,omitempty"`
	Database string        `json:"database,omitempty"`
	Encrypt  string        `json:"encrypt,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
	Query    string        `json:"query,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *MSSQLConfig) FromMap(configMap map[string]any) error {
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

	if encrypt, ok := configMap["encrypt"].(string); ok {
		c.Encrypt = encrypt
	} else if configMap["encrypt"] != nil {
		return checkerdef.NewConfigError("encrypt", "must be a string")
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
func (c *MSSQLConfig) GetConfig() map[string]any {
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

	if c.Encrypt != "" && c.Encrypt != defaultEncrypt {
		cfg["encrypt"] = c.Encrypt
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
func (c *MSSQLConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Encrypt != "" {
		switch c.Encrypt {
		case "true", defaultEncrypt, "disable":
			// valid
		default:
			return checkerdef.NewConfigError("encrypt", "must be one of: true, false, disable")
		}
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	if c.Query != "" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Query)), "SELECT") {
		return checkerdef.NewConfigError("query", "must start with SELECT")
	}

	return nil
}

// buildConnURL builds a SQL Server connection URL.
func (c *MSSQLConfig) buildConnURL() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	database := c.Database
	if database == "" {
		database = defaultDatabase
	}

	encrypt := c.Encrypt
	if encrypt == "" {
		encrypt = defaultEncrypt
	}

	params := url.Values{}
	params.Set("database", database)
	params.Set("encrypt", encrypt)

	timeout := c.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	params.Set("dial timeout", strconv.Itoa(int(timeout.Seconds())))

	connURL := &url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(c.Username, c.Password),
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(port)),
		RawQuery: params.Encode(),
	}

	return connURL.String()
}
