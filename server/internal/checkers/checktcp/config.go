package checktcp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// TCPConfig holds the configuration for TCP connection checks.
type TCPConfig struct {
	URL           string        `json:"url,omitempty"`
	Host          string        `json:"host,omitempty"`
	Port          int           `json:"port,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	SendData      string        `json:"send_data,omitempty"`   //nolint:tagliatelle // API uses snake_case
	ExpectData    string        `json:"expect_data,omitempty"` //nolint:tagliatelle // API uses snake_case
	TLS           bool          `json:"tls,omitempty"`
	TLSVerify     bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle // API uses snake_case
	TLSServerName string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,nestif // Configuration parsing requires checking multiple field types
func (c *TCPConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeTCP {
			return checkerdef.NewConfigError("url", "must be a TCP URL (tcp:// or tcps://)")
		}
		c.Host = parsed.Host
		c.Port = parsed.Port
		c.TLS = parsed.TLS
	} else {
		// Fall back to legacy host+port
		if host, ok := configMap["host"].(string); ok {
			c.Host = host
		} else if configMap["host"] != nil {
			return checkerdef.NewConfigError("host", "must be a string")
		}

		// Extract Port (required for legacy mode)
		if port, ok := configMap["port"].(int); ok {
			c.Port = port
		} else if portFloat, ok := configMap["port"].(float64); ok {
			// Handle JSON numbers which unmarshal as float64
			c.Port = int(portFloat)
		} else if configMap["port"] != nil {
			return checkerdef.NewConfigError("port", "must be a number")
		}
	}

	// Extract Timeout (optional, duration string)
	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	// Extract SendData (optional)
	if sendData, ok := configMap["send_data"].(string); ok {
		c.SendData = sendData
	} else if configMap["send_data"] != nil {
		return checkerdef.NewConfigError("send_data", "must be a string")
	}

	// Extract ExpectData (optional)
	if expectData, ok := configMap["expect_data"].(string); ok {
		c.ExpectData = expectData
	} else if configMap["expect_data"] != nil {
		return checkerdef.NewConfigError("expect_data", "must be a string")
	}

	// Extract TLS (optional)
	if tls, ok := configMap["tls"].(bool); ok {
		c.TLS = tls
	} else if configMap["tls"] != nil {
		return checkerdef.NewConfigError("tls", "must be a boolean")
	}

	// Extract TLSVerify (optional)
	if tlsVerify, ok := configMap["tls_verify"].(bool); ok {
		c.TLSVerify = tlsVerify
	} else if configMap["tls_verify"] != nil {
		return checkerdef.NewConfigError("tls_verify", "must be a boolean")
	}

	// Extract TLSServerName (optional)
	if tlsServerName, ok := configMap["tls_server_name"].(string); ok {
		c.TLSServerName = tlsServerName
	} else if configMap["tls_server_name"] != nil {
		return checkerdef.NewConfigError("tls_server_name", "must be a string")
	}

	return nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *TCPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
		"port": c.Port,
	}

	if c.URL != "" {
		cfg["url"] = c.URL
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.SendData != "" {
		cfg["send_data"] = c.SendData
	}

	if c.ExpectData != "" {
		cfg["expect_data"] = c.ExpectData
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.TLSVerify {
		cfg["tls_verify"] = c.TLSVerify
	}

	if c.TLSServerName != "" {
		cfg["tls_server_name"] = c.TLSServerName
	}

	return cfg
}
