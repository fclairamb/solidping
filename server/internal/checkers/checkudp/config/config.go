package config

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// UDPConfig holds the configuration for UDP port checks.
type UDPConfig struct {
	Host           string        `json:"host,omitempty"`
	Port           int           `json:"port,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	SendData       string        `json:"send_data,omitempty"`       //nolint:tagliatelle // API uses snake_case
	SendEncoding   string        `json:"send_encoding,omitempty"`   //nolint:tagliatelle // API uses snake_case
	ExpectData     string        `json:"expect_data,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectEncoding string        `json:"expect_encoding,omitempty"` //nolint:tagliatelle // API uses snake_case
	ExpectPattern  string        `json:"expect_pattern,omitempty"`  //nolint:tagliatelle // API uses snake_case
}

// exchangeFields projects the send/expect half of the config onto the shared
// type that parses, serializes and validates it — the same one `tcp` uses.
func (c *UDPConfig) exchangeFields() *checkerdef.ExchangeFields {
	return &checkerdef.ExchangeFields{
		SendData:       c.SendData,
		SendEncoding:   c.SendEncoding,
		ExpectData:     c.ExpectData,
		ExpectEncoding: c.ExpectEncoding,
		ExpectPattern:  c.ExpectPattern,
	}
}

// FromMap populates the configuration from a map.
func (c *UDPConfig) FromMap(configMap map[string]any) error {
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

	exchange := checkerdef.ExchangeFields{}
	if err := exchange.FromMap(configMap); err != nil {
		return err
	}

	c.SendData = exchange.SendData
	c.SendEncoding = exchange.SendEncoding
	c.ExpectData = exchange.ExpectData
	c.ExpectEncoding = exchange.ExpectEncoding
	c.ExpectPattern = exchange.ExpectPattern

	return nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *UDPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
		"port": c.Port,
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	c.exchangeFields().Apply(cfg)

	return cfg
}
