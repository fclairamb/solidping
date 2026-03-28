package checkoracle

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort        = 1521
	defaultTimeout     = 10 * time.Second
	maxTimeout         = 60 * time.Second
	defaultQuery       = "SELECT 1 FROM DUAL"
	defaultServiceName = "ORCL"
)

// OracleConfig holds the configuration for Oracle Database checks.
type OracleConfig struct {
	Host        string        `json:"host"`
	Port        int           `json:"port,omitempty"`
	Username    string        `json:"username"`
	Password    string        `json:"password,omitempty"`
	ServiceName string        `json:"service_name,omitempty"`
	SID         string        `json:"sid,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	Query       string        `json:"query,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *OracleConfig) FromMap(configMap map[string]any) error {
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

	if serviceName, ok := configMap["service_name"].(string); ok {
		c.ServiceName = serviceName
	} else if configMap["service_name"] != nil {
		return checkerdef.NewConfigError("service_name", "must be a string")
	}

	if sid, ok := configMap["sid"].(string); ok {
		c.SID = sid
	} else if configMap["sid"] != nil {
		return checkerdef.NewConfigError("sid", "must be a string")
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
func (c *OracleConfig) GetConfig() map[string]any {
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

	if c.ServiceName != "" && c.ServiceName != defaultServiceName {
		cfg["service_name"] = c.ServiceName
	}

	if c.SID != "" {
		cfg["sid"] = c.SID
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
func (c *OracleConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.ServiceName != "" && c.SID != "" {
		return checkerdef.NewConfigError("service_name", "cannot specify both service_name and sid")
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

// buildConnURL builds an Oracle connection URL using the go-ora format.
func (c *OracleConfig) buildConnURL() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}

	connURL := &url.URL{
		Scheme: "oracle",
		User:   url.UserPassword(c.Username, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(port)),
	}

	if c.SID != "" {
		params := connURL.Query()
		params.Set("SID", c.SID)
		connURL.RawQuery = params.Encode()
	} else {
		serviceName := c.ServiceName
		if serviceName == "" {
			serviceName = defaultServiceName
		}

		connURL.Path = "/" + serviceName
	}

	return connURL.String()
}
