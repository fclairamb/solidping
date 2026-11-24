// Package config holds the postgres check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkpostgres package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

const (
	// DefaultPort is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultPort = 5432
	// DefaultTimeout is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
	// DefaultDatabase is the database a check connects to when it names none.
	DefaultDatabase = "postgres"
	// DefaultSSLMode is the ssl_mode a check uses when it sets none.
	DefaultSSLMode = "prefer"
	// DefaultQuery is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultQuery = "SELECT 1"
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
	// URL is only read, to expand a `postgres+unix:///path/.s.PGSQL.5432?dbname=app&user=monitor`
	// quick-create URL into socket + database + username; it is never written back.
	URL string `json:"url,omitempty"`
	// Socket is the Unix socket file (not libpq's directory form) the check
	// connects to instead of host:port (spec 2026-11-24-03).
	Socket   string        `json:"socket,omitempty"`
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username"`
	Password string        `json:"password,omitempty"`
	Database string        `json:"database,omitempty"`
	SSLMode  string        `json:"ssl_mode,omitempty"` //nolint:tagliatelle // API uses snake_case
	Timeout  time.Duration `json:"timeout,omitempty"`
	Query    string        `json:"query,omitempty"`
}

// configKeySocket is the config key of the Unix socket path.
const configKeySocket = "socket"

// LocalSocket implements checkerdef.LocalSocketTarget.
func (c *PostgreSQLConfig) LocalSocket() (string, string) {
	return configKeySocket, c.Socket
}

// SchemaExclusiveGroups implements checkerdef.SchemaExclusiveGrouper: a
// postgres check targets either a host (with its port) or a Unix socket.
func (c *PostgreSQLConfig) SchemaExclusiveGroups() [][]string {
	return [][]string{{configKeySocket, checkerdef.OutputKeyHost}}
}

// SchemaBaselines implements checkerdef.SchemaBaseliner: the socket shape.
func (c *PostgreSQLConfig) SchemaBaselines() []map[string]any {
	return []map[string]any{{configKeySocket: "/var/run/postgresql/.s.PGSQL.5432", "username": "monitor"}}
}

// SchemaNotes implements checkerdef.SchemaNoter.
func (c *PostgreSQLConfig) SchemaNotes() []string {
	return []string{
		"`socket` (the socket file, e.g. `/var/run/postgresql/.s.PGSQL.5432`, also set by a " +
			"`postgres+unix:///path?dbname=app&user=monitor` URL) replaces `host` and `port`; " +
			"it cannot be combined with them, and there is no TLS over it. The server only dials " +
			"paths listed in its `checkers.unix_socket_paths` setting.",
	}
}

// expandUnixURL rewrites a `postgres+unix://` url into socket + database + username.
func (c *PostgreSQLConfig) expandUnixURL(configMap map[string]any) error {
	if c.URL == "" {
		return nil
	}

	parsed, err := urlparse.Parse(c.URL)
	if err != nil || parsed.Socket == "" || parsed.CheckType != checkerdef.CheckTypePostgreSQL {
		return checkerdef.NewConfigError("url", "must be a postgres+unix:///path/to/.s.PGSQL.5432 URL")
	}

	if socket, _ := configMap[configKeySocket].(string); socket != "" {
		return checkerdef.NewConfigError(configKeySocket, "socket and url are mutually exclusive")
	}

	c.Socket = parsed.Socket

	query, err := url.ParseQuery(parsed.Query)
	if err != nil {
		return checkerdef.NewConfigError("url", err.Error())
	}

	if database := query.Get("dbname"); database != "" {
		c.Database = database
	}

	if user := query.Get("user"); user != "" {
		c.Username = user
	}

	c.URL = ""

	return nil
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,gocyclo // Configuration parsing requires checking multiple field types
func (c *PostgreSQLConfig) FromMap(configMap map[string]any) error {
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

	return c.expandUnixURL(configMap)
}

// GetConfig returns the configuration as a map.
func (c *PostgreSQLConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"username": c.Username,
	}

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

	if c.Database != "" && c.Database != DefaultDatabase {
		cfg["database"] = c.Database
	}

	if c.SSLMode != "" && c.SSLMode != DefaultSSLMode {
		cfg["ssl_mode"] = c.SSLMode
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Query != "" && c.Query != DefaultQuery {
		cfg["query"] = c.Query
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *PostgreSQLConfig) Validate() error {
	if err := c.validateTarget(); err != nil {
		return err
	}

	if c.Username == "" {
		return checkerdef.NewRequiredConfigError("username")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
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

// validateTarget checks the host:port or socket target.
func (c *PostgreSQLConfig) validateTarget() error {
	if c.Socket == "" {
		if c.Host == "" {
			return checkerdef.NewRequiredConfigError("host")
		}

		// lib/pq dials a Unix socket for an absolute or `@` host: a path here
		// would skip the socket allowlist (spec 2026-11-24-08).
		if filepath.IsAbs(c.Host) || strings.HasPrefix(c.Host, "@") || strings.Contains(c.Host, "/") {
			return checkerdef.NewConfigErrorf("host",
				"must be a hostname or an IP address, not a path; for a local socket use the socket field "+
					"(the .s.PGSQL.<port> file) with %s", checkerdef.LocalSocketSetting)
		}

		return nil
	}

	if c.Host != "" || c.Port != 0 {
		return checkerdef.NewConfigError(configKeySocket, "socket and host/port are mutually exclusive")
	}

	if c.SSLMode != "" && c.SSLMode != "disable" {
		return checkerdef.NewConfigError("ssl_mode", "TLS is not supported over a Unix socket (use disable)")
	}

	if err := checkerdef.ValidateLocalSocketPath(c.Socket); err != nil {
		return checkerdef.NewConfigError(configKeySocket, err.Error())
	}

	return nil
}
