package checksmtp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort       = 25
	defaultTimeout    = 10 * time.Second
	maxTimeout        = 60 * time.Second
	defaultEHLODomain = "solidping.local"
	implicitTLSPort   = 465
)

// SMTPConfig holds the configuration for SMTP server checks.
type SMTPConfig struct {
	Host           string        `json:"host"`
	Port           int           `json:"port,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	StartTLS       bool          `json:"starttls,omitempty"`
	TLSVerify      bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle // API uses snake_case
	TLSServerName  string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle // API uses snake_case
	EHLODomain     string        `json:"ehlo_domain,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectGreeting string        `json:"expect_greeting,omitempty"` //nolint:tagliatelle // API uses snake_case
	CheckAuth      bool          `json:"check_auth,omitempty"`      //nolint:tagliatelle // API uses snake_case
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *SMTPConfig) FromMap(configMap map[string]any) error {
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

	if starttls, ok := configMap["starttls"].(bool); ok {
		c.StartTLS = starttls
	} else if configMap["starttls"] != nil {
		return checkerdef.NewConfigError("starttls", "must be a boolean")
	}

	if tlsVerify, ok := configMap["tls_verify"].(bool); ok {
		c.TLSVerify = tlsVerify
	} else if configMap["tls_verify"] != nil {
		return checkerdef.NewConfigError("tls_verify", "must be a boolean")
	}

	if tlsServerName, ok := configMap["tls_server_name"].(string); ok {
		c.TLSServerName = tlsServerName
	} else if configMap["tls_server_name"] != nil {
		return checkerdef.NewConfigError("tls_server_name", "must be a string")
	}

	if ehloDomain, ok := configMap["ehlo_domain"].(string); ok {
		c.EHLODomain = ehloDomain
	} else if configMap["ehlo_domain"] != nil {
		return checkerdef.NewConfigError("ehlo_domain", "must be a string")
	}

	if expectGreeting, ok := configMap["expect_greeting"].(string); ok {
		c.ExpectGreeting = expectGreeting
	} else if configMap["expect_greeting"] != nil {
		return checkerdef.NewConfigError("expect_greeting", "must be a string")
	}

	if checkAuth, ok := configMap["check_auth"].(bool); ok {
		c.CheckAuth = checkAuth
	} else if configMap["check_auth"] != nil {
		return checkerdef.NewConfigError("check_auth", "must be a boolean")
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

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SMTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.StartTLS {
		cfg["starttls"] = c.StartTLS
	}

	if c.TLSVerify {
		cfg["tls_verify"] = c.TLSVerify
	}

	if c.TLSServerName != "" {
		cfg["tls_server_name"] = c.TLSServerName
	}

	if c.EHLODomain != "" && c.EHLODomain != defaultEHLODomain {
		cfg["ehlo_domain"] = c.EHLODomain
	}

	if c.ExpectGreeting != "" {
		cfg["expect_greeting"] = c.ExpectGreeting
	}

	if c.CheckAuth {
		cfg["check_auth"] = c.CheckAuth
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *SMTPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	if c.StartTLS && c.Port == implicitTLSPort {
		return checkerdef.NewConfigError("starttls", "cannot use STARTTLS with port 465 (implicit TLS)")
	}

	return nil
}
