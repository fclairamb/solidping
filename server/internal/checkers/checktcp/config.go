package checktcp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// TCPConfig holds the configuration for TCP connection checks.
type TCPConfig struct {
	URL            string        `json:"url,omitempty"`
	Host           string        `json:"host,omitempty"`
	Port           int           `json:"port,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	SendData       string        `json:"send_data,omitempty"`       //nolint:tagliatelle // API uses snake_case
	SendEncoding   string        `json:"send_encoding,omitempty"`   //nolint:tagliatelle // API uses snake_case
	ExpectData     string        `json:"expect_data,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectEncoding string        `json:"expect_encoding,omitempty"` //nolint:tagliatelle // API uses snake_case
	ExpectPattern  string        `json:"expect_pattern,omitempty"`  //nolint:tagliatelle // API uses snake_case
	TLS            bool          `json:"tls,omitempty"`
	TLSVerify      bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle // API uses snake_case
	TLSServerName  string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// exchangeFields projects the send/expect half of the config onto the shared
// type that parses, serializes and validates it.
func (c *TCPConfig) exchangeFields() *checkerdef.ExchangeFields {
	return &checkerdef.ExchangeFields{
		SendData:       c.SendData,
		SendEncoding:   c.SendEncoding,
		ExpectData:     c.ExpectData,
		ExpectEncoding: c.ExpectEncoding,
		ExpectPattern:  c.ExpectPattern,
	}
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
		if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
			c.Host = host
		} else if configMap[checkerdef.OutputKeyHost] != nil {
			return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
		}

		// Extract Port (required for legacy mode)
		if port, ok := configMap[checkerdef.OutputKeyPort].(int); ok {
			c.Port = port
		} else if portFloat, ok := configMap[checkerdef.OutputKeyPort].(float64); ok {
			// Handle JSON numbers which unmarshal as float64
			c.Port = int(portFloat)
		} else if configMap[checkerdef.OutputKeyPort] != nil {
			return checkerdef.NewConfigError(checkerdef.OutputKeyPort, "must be a number")
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

	// Extract the send/expect payload keys (optional) — parsed once, in
	// checkerdef, so `tcp` and `udp` cannot drift apart.
	exchange := checkerdef.ExchangeFields{}
	if err := exchange.FromMap(configMap); err != nil {
		return err
	}

	c.SendData = exchange.SendData
	c.SendEncoding = exchange.SendEncoding
	c.ExpectData = exchange.ExpectData
	c.ExpectEncoding = exchange.ExpectEncoding
	c.ExpectPattern = exchange.ExpectPattern

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
		checkerdef.OutputKeyHost: c.Host,
		checkerdef.OutputKeyPort: c.Port,
	}

	if c.URL != "" {
		cfg["url"] = c.URL
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	c.exchangeFields().Apply(cfg)

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
