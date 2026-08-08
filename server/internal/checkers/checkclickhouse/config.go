package checkclickhouse

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// defaultPort is ClickHouse's native-protocol port; TLS moves it to defaultSecurePort.
	defaultPort       = 9000
	defaultSecurePort = 9440
	defaultTimeout    = 10 * time.Second
	maxTimeout        = 30 * time.Second
	defaultQuery      = "SELECT 1"
	// defaultUser and defaultDatabase are ClickHouse's own out-of-the-box
	// defaults, so neither field is required in the check config.
	defaultUser     = "default"
	defaultDatabase = "default"
	maxPort         = 65535
)

// ClickHouseConfig holds the configuration for ClickHouse database checks over
// the native protocol.
type ClickHouseConfig struct {
	Host      string        `json:"host"`
	Port      int           `json:"port,omitempty"`
	Username  string        `json:"username,omitempty"`
	Password  string        `json:"password,omitempty"`
	Database  string        `json:"database,omitempty"`
	Secure    bool          `json:"secure,omitempty"`
	TLSVerify bool          `json:"tls_verify,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
	Query     string        `json:"query,omitempty"`
}

// GetConfig returns the configuration as a map, omitting anything left at its
// default so stored configs stay minimal.
func (c *ClickHouseConfig) GetConfig() map[string]any {
	cfg := map[string]any{"host": c.Host}

	if c.Port != 0 && c.Port != c.defaultPort() {
		cfg["port"] = c.Port
	}

	optionalStrings := map[string]string{
		"username": c.Username,
		"password": c.Password,
		"database": c.Database,
	}
	for key, value := range optionalStrings {
		if value != "" {
			cfg[key] = value
		}
	}

	if c.Secure {
		cfg["secure"] = true
	}

	if c.TLSVerify {
		cfg["tls_verify"] = true
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
func (c *ClickHouseConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > maxPort {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and %d, got %d", maxPort, c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= %s, got %s", maxTimeout, c.Timeout.String(),
		)
	}

	if c.TLSVerify && !c.Secure {
		return checkerdef.NewConfigError("tls_verify", "requires secure to be enabled")
	}

	if c.Query != "" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Query)), "SELECT") {
		return checkerdef.NewConfigError("query", "must start with SELECT")
	}

	return nil
}

// defaultPort returns the port implied by the transport: ClickHouse listens for
// plain native traffic on 9000 and for native-over-TLS on 9440.
func (c *ClickHouseConfig) defaultPort() int {
	if c.Secure {
		return defaultSecurePort
	}

	return defaultPort
}

// resolvedPort returns the effective port, applying the transport default.
func (c *ClickHouseConfig) resolvedPort() int {
	if c.Port != 0 {
		return c.Port
	}

	return c.defaultPort()
}

// resolvedDatabase returns the effective database name.
func (c *ClickHouseConfig) resolvedDatabase() string {
	if c.Database != "" {
		return c.Database
	}

	return defaultDatabase
}

// resolvedUsername returns the effective username.
func (c *ClickHouseConfig) resolvedUsername() string {
	if c.Username != "" {
		return c.Username
	}

	return defaultUser
}

// resolvedTimeout returns the effective timeout.
func (c *ClickHouseConfig) resolvedTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

// resolvedQuery returns the effective health-check query.
func (c *ClickHouseConfig) resolvedQuery() string {
	if c.Query != "" {
		return c.Query
	}

	return defaultQuery
}
