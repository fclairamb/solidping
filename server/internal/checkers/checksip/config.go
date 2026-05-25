// Package checksip provides SIP server reachability (OPTIONS) and
// registration (REGISTER) monitoring checks.
package checksip

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// SIP defaults.
const (
	defaultTimeout = 5 * time.Second
	maxTimeout     = 60 * time.Second

	defaultPortPlain = 5060 // udp / tcp
	defaultPortTLS   = 5061 // tls

	transportUDP = "udp"
	transportTCP = "tcp"
	transportTLS = "tls"

	modeOptions  = "options"
	modeRegister = "register"
)

// SIPConfig holds the configuration for SIP server checks.
type SIPConfig struct {
	Host          string        `json:"host"`
	Port          int           `json:"port,omitempty"`
	Transport     string        `json:"transport,omitempty"`
	Mode          string        `json:"mode,omitempty"`
	Domain        string        `json:"domain,omitempty"`
	Username      string        `json:"username,omitempty"`
	Password      string        `json:"password,omitempty"`
	AuthUsername  string        `json:"auth_username,omitempty"` //nolint:tagliatelle // API uses snake_case
	ExpectStatus  string        `json:"expect_status,omitempty"` //nolint:tagliatelle // API uses snake_case
	Timeout       time.Duration `json:"timeout,omitempty"`
	TLSVerify     bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle // API uses snake_case
	TLSServerName string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,funlen // Configuration parsing requires checking multiple field types
func (c *SIPConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	} else if configMap[checkerdef.OutputKeyHost] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
	}

	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if transport, ok := configMap["transport"].(string); ok {
		c.Transport = transport
	} else if configMap["transport"] != nil {
		return checkerdef.NewConfigError("transport", "must be a string")
	}

	if mode, ok := configMap["mode"].(string); ok {
		c.Mode = mode
	} else if configMap["mode"] != nil {
		return checkerdef.NewConfigError("mode", "must be a string")
	}

	if domain, ok := configMap["domain"].(string); ok {
		c.Domain = domain
	} else if configMap["domain"] != nil {
		return checkerdef.NewConfigError("domain", "must be a string")
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

	if authUsername, ok := configMap["auth_username"].(string); ok {
		c.AuthUsername = authUsername
	} else if configMap["auth_username"] != nil {
		return checkerdef.NewConfigError("auth_username", "must be a string")
	}

	if expectStatus, ok := configMap["expect_status"].(string); ok {
		c.ExpectStatus = expectStatus
	} else if configMap["expect_status"] != nil {
		return checkerdef.NewConfigError("expect_status", "must be a string")
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

	return nil
}

// GetConfig returns the configuration as a map.
//
//nolint:cyclop // Serialization mirrors the field set
func (c *SIPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 {
		cfg["port"] = c.Port
	}

	if c.Transport != "" {
		cfg["transport"] = c.Transport
	}

	if c.Mode != "" {
		cfg["mode"] = c.Mode
	}

	if c.Domain != "" {
		cfg["domain"] = c.Domain
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.AuthUsername != "" {
		cfg["auth_username"] = c.AuthUsername
	}

	if c.ExpectStatus != "" {
		cfg["expect_status"] = c.ExpectStatus
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.TLSVerify {
		cfg["tls_verify"] = c.TLSVerify
	}

	if c.TLSServerName != "" {
		cfg["tls_server_name"] = c.TLSServerName
	}

	return cfg
}

// Validate checks if the configuration is valid. It performs no network operations.
//
//nolint:cyclop // Validation covers several independent fields
func (c *SIPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Transport != "" &&
		c.Transport != transportUDP &&
		c.Transport != transportTCP &&
		c.Transport != transportTLS {
		return checkerdef.NewConfigErrorf("transport", "must be one of udp, tcp, tls, got %q", c.Transport)
	}

	if c.Mode != "" && c.Mode != modeOptions && c.Mode != modeRegister {
		return checkerdef.NewConfigErrorf("mode", "must be one of options, register, got %q", c.Mode)
	}

	if c.Mode == modeRegister {
		if c.Username == "" {
			return checkerdef.NewConfigError("username", "is required for register mode")
		}

		if c.Password == "" {
			return checkerdef.NewConfigError("password", "is required for register mode")
		}
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	return nil
}
