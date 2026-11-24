// Package config holds the mysql check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkmysql package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

const (
	// DefaultPort is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultPort = 3306
	// DefaultTimeout is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
	// DefaultQuery is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	DefaultQuery = "SELECT 1"

	// connStringMetaChars are the characters that delimit a go-sql-driver DSN
	// (user:pass@net(addr)/db?k=v&k=v). The checker builds its connection from
	// structured fields, never a DSN string, so these are harmless there; the
	// rule is defense in depth against any future string round-trip
	// (spec 2026-10-16-04).
	connStringMetaChars = "@/()?&:"
)

// MySQLConfig holds the configuration for MySQL/MariaDB database checks.
type MySQLConfig struct {
	// URL is only read, to expand a `mysql+unix:///path` quick-create URL into the
	// socket; it is never written back.
	URL string `json:"url,omitempty"`
	// Socket is the Unix socket file the check connects to instead of
	// host:port (spec 2026-11-24-04).
	Socket   string        `json:"socket,omitempty"`
	Host     string        `json:"host"`
	Port     int           `json:"port,omitempty"`
	Username string        `json:"username"`
	Password string        `json:"password,omitempty"`
	Database string        `json:"database,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
	Query    string        `json:"query,omitempty"`
}

// configKeySocket is the config key of the Unix socket path.
const configKeySocket = "socket"

// configKeyUsername is the config key of the database user.
const configKeyUsername = "username"

// LocalSocket implements checkerdef.LocalSocketTarget.
func (c *MySQLConfig) LocalSocket() (string, string) {
	return configKeySocket, c.Socket
}

// SchemaExclusiveGroups implements checkerdef.SchemaExclusiveGrouper: the
// check targets either a host (with its port) or a Unix socket.
func (c *MySQLConfig) SchemaExclusiveGroups() [][]string {
	return [][]string{{configKeySocket, checkerdef.OutputKeyHost}}
}

// SchemaBaselines implements checkerdef.SchemaBaseliner: the socket shape.
func (c *MySQLConfig) SchemaBaselines() []map[string]any {
	return []map[string]any{{configKeySocket: "/run/mysqld/mysqld.sock", configKeyUsername: "monitor"}}
}

// SchemaNotes implements checkerdef.SchemaNoter.
func (c *MySQLConfig) SchemaNotes() []string {
	return []string{
		"`socket` (an absolute Unix socket path, also set by a `mysql+unix:///path` URL) " +
			"replaces `host` and `port`; it cannot be combined with them. The server only dials " +
			"paths listed in its `checkers.unix_socket_paths` setting.",
	}
}

// expandUnixURL rewrites a `mysql+unix://` url into the socket and the credentials.
func (c *MySQLConfig) expandUnixURL(configMap map[string]any) error {
	if c.URL == "" {
		return nil
	}

	parsed, err := urlparse.Parse(c.URL)
	if err != nil || parsed.Socket == "" || parsed.CheckType != checkerdef.CheckTypeMySQL {
		return checkerdef.NewConfigError("url", "must be a mysql+unix:///path/to/socket URL")
	}

	if socket, _ := configMap[configKeySocket].(string); socket != "" {
		return checkerdef.NewConfigError(configKeySocket, "socket and url are mutually exclusive")
	}

	c.Socket = parsed.Socket

	query, err := url.ParseQuery(parsed.Query)
	if err != nil {
		return checkerdef.NewConfigError("url", err.Error())
	}

	if db := query.Get("db"); db != "" {
		c.Database = db
	}

	if user := query.Get("user"); user != "" {
		c.Username = user
	}

	c.URL = ""

	return nil
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *MySQLConfig) FromMap(configMap map[string]any) error {
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

	if username, ok := configMap[configKeyUsername].(string); ok {
		c.Username = username
	} else if configMap[configKeyUsername] != nil {
		return checkerdef.NewConfigError(configKeyUsername, "must be a string")
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

	if query, ok := configMap["query"].(string); ok {
		c.Query = query
	} else if configMap["query"] != nil {
		return checkerdef.NewConfigError("query", "must be a string")
	}

	return c.expandUnixURL(configMap)
}

// GetConfig returns the configuration as a map.
func (c *MySQLConfig) GetConfig() map[string]any {
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

	if c.Database != "" {
		cfg["database"] = c.Database
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
//
//nolint:cyclop // one rule per field
func (c *MySQLConfig) Validate() error {
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

	if c.Username == "" {
		return checkerdef.NewRequiredConfigError("username")
	}

	fields := []struct{ name, value string }{
		{"username", c.Username},
		{"password", c.Password},
		{"database", c.Database},
	}
	for i := range fields {
		field := &fields[i]

		// A `${env:NAME}` / `${param:KEY}` reference is stored as-is and only
		// resolved at execution, where the value reaches the driver as a
		// structured field; its own `:` is grammar, not data.
		if strings.ContainsAny(secretref.Pattern.ReplaceAllString(field.value, ""), connStringMetaChars) {
			return checkerdef.NewConfigErrorf(
				field.name, "must not contain any of the characters %s", connStringMetaChars,
			)
		}
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	if c.Query != "" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Query)), "SELECT") {
		return checkerdef.NewConfigError("query", "must start with SELECT")
	}

	return nil
}
