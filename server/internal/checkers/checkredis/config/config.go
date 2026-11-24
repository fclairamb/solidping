// Package config holds the redis check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkredis package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

const (
	// DefaultPort is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultPort = 6379
	// DefaultTimeout is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
)

// RedisConfig holds the configuration for Redis health checks.
type RedisConfig struct {
	// URL is only read, to expand a `redis+unix:///run/redis/redis.sock?db=0`
	// quick-create URL into socket + database; it is never written back.
	URL string `json:"url,omitempty"`
	// Socket is the Unix socket file the check connects to instead of
	// host:port (spec 2026-11-24-03).
	Socket   string        `json:"socket,omitempty"`
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Password string        `json:"password,omitempty"`
	Database int           `json:"database,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// configKeySocket is the config key of the Unix socket path.
const configKeySocket = "socket"

// LocalSocket implements checkerdef.LocalSocketTarget.
func (c *RedisConfig) LocalSocket() (string, string) {
	return configKeySocket, c.Socket
}

// SchemaExclusiveGroups implements checkerdef.SchemaExclusiveGrouper: a redis
// check targets either a host (with its port) or a Unix socket.
func (c *RedisConfig) SchemaExclusiveGroups() [][]string {
	return [][]string{{configKeySocket, checkerdef.OutputKeyHost}}
}

// SchemaBaselines implements checkerdef.SchemaBaseliner: the socket shape.
func (c *RedisConfig) SchemaBaselines() []map[string]any {
	return []map[string]any{{configKeySocket: "/run/redis/redis.sock"}}
}

// SchemaNotes implements checkerdef.SchemaNoter.
func (c *RedisConfig) SchemaNotes() []string {
	return []string{
		"`socket` (an absolute Unix socket path, also set by a `redis+unix:///path?db=0` URL) " +
			"replaces `host` and `port`; it cannot be combined with them. The server only dials " +
			"paths listed in its `checkers.unix_socket_paths` setting.",
	}
}

// expandUnixURL rewrites a `redis+unix://` url into socket + database.
func (c *RedisConfig) expandUnixURL(configMap map[string]any) error {
	if c.URL == "" {
		return nil
	}

	parsed, err := urlparse.Parse(c.URL)
	if err != nil || parsed.Socket == "" || parsed.CheckType != checkerdef.CheckTypeRedis {
		return checkerdef.NewConfigError("url", "must be a redis+unix:///path/to/redis.sock URL")
	}

	if socket, _ := configMap[configKeySocket].(string); socket != "" {
		return checkerdef.NewConfigError(configKeySocket, "socket and url are mutually exclusive")
	}

	c.Socket = parsed.Socket

	query, err := url.ParseQuery(parsed.Query)
	if err != nil {
		return checkerdef.NewConfigError("url", err.Error())
	}

	if dbStr := query.Get("db"); dbStr != "" {
		db, convErr := strconv.Atoi(dbStr)
		if convErr != nil {
			return checkerdef.NewConfigError("url", "db must be a number")
		}

		c.Database = db
	}

	c.URL = ""

	return nil
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *RedisConfig) FromMap(configMap map[string]any) error {
	if urlStr, ok := configMap["url"].(string); ok {
		c.URL = urlStr
	} else if configMap["url"] != nil {
		return checkerdef.NewConfigError("url", "must be a string")
	}

	if socket, ok := configMap[configKeySocket].(string); ok {
		c.Socket = socket
	} else if configMap[configKeySocket] != nil {
		return checkerdef.NewConfigError(configKeySocket, "must be a string")
	}

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

	return c.expandUnixURL(configMap)
}

// GetConfig returns the configuration as a map.
func (c *RedisConfig) GetConfig() map[string]any {
	cfg := map[string]any{}

	if c.Socket != "" {
		cfg[configKeySocket] = c.Socket
	} else {
		cfg["host"] = c.Host
	}

	if c.Port != 0 && c.Port != DefaultPort {
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
	if c.Socket != "" {
		if c.Host != "" || c.Port != 0 {
			return checkerdef.NewConfigError(configKeySocket, "socket and host/port are mutually exclusive")
		}

		if err := checkerdef.ValidateLocalSocketPath(c.Socket); err != nil {
			return checkerdef.NewConfigError(configKeySocket, err.Error())
		}
	} else if c.Host == "" {
		return checkerdef.NewRequiredConfigError("host")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Database < 0 || c.Database > 15 {
		return checkerdef.NewConfigErrorf("database", "must be between 0 and 15, got %d", c.Database)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	return nil
}

// Addr returns the Redis address in host:port format.
func (c *RedisConfig) Addr() string {
	port := c.Port
	if port == 0 {
		port = DefaultPort
	}

	return fmt.Sprintf("%s:%d", c.Host, port)
}
