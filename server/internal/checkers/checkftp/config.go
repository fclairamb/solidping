package checkftp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort       = 21
	defaultTimeout    = 10 * time.Second
	maxTimeout        = 60 * time.Second
	defaultUsername   = "anonymous"
	implicitTLSPort   = 990
	microsecondsPerMs = 1000.0
)

// TLS mode constants.
const (
	TLSModeNone     = "none"
	TLSModeExplicit = "explicit"
	TLSModeImplicit = "implicit"
)

// FTPConfig holds the configuration for FTP checks.
type FTPConfig struct {
	Host        string        `json:"host"`
	Port        int           `json:"port,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	Username    string        `json:"username,omitempty"`
	Password    string        `json:"password,omitempty"`
	TLSMode     string        `json:"tls_mode,omitempty"`     //nolint:tagliatelle // API uses snake_case
	TLSVerify   bool          `json:"tls_verify,omitempty"`   //nolint:tagliatelle // API uses snake_case
	PassiveMode bool          `json:"passive_mode,omitempty"` //nolint:tagliatelle // API uses snake_case
	Path        string        `json:"path,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *FTPConfig) FromMap(configMap map[string]any) error {
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

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
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

	if tlsMode, ok := configMap["tls_mode"].(string); ok {
		c.TLSMode = tlsMode
	} else if configMap["tls_mode"] != nil {
		return checkerdef.NewConfigError("tls_mode", "must be a string")
	}

	if tlsVerify, ok := configMap["tls_verify"].(bool); ok {
		c.TLSVerify = tlsVerify
	} else if configMap["tls_verify"] != nil {
		return checkerdef.NewConfigError("tls_verify", "must be a boolean")
	}

	if passiveMode, ok := configMap["passive_mode"].(bool); ok {
		c.PassiveMode = passiveMode
	} else if configMap["passive_mode"] != nil {
		return checkerdef.NewConfigError("passive_mode", "must be a boolean")
	}

	if path, ok := configMap["path"].(string); ok {
		c.Path = path
	} else if configMap["path"] != nil {
		return checkerdef.NewConfigError("path", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *FTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Username != "" && c.Username != defaultUsername {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.TLSMode != "" && c.TLSMode != TLSModeNone {
		cfg["tls_mode"] = c.TLSMode
	}

	if c.TLSVerify {
		cfg["tls_verify"] = c.TLSVerify
	}

	if !c.PassiveMode {
		cfg["passive_mode"] = c.PassiveMode
	}

	if c.Path != "" {
		cfg["path"] = c.Path
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *FTPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	if c.TLSMode != "" && c.TLSMode != TLSModeNone &&
		c.TLSMode != TLSModeExplicit && c.TLSMode != TLSModeImplicit {
		return checkerdef.NewConfigErrorf(
			"tls_mode", "must be none, explicit, or implicit, got %s", c.TLSMode,
		)
	}

	return nil
}
