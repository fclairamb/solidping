package checkudp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// UDPConfig holds the configuration for UDP port checks.
type UDPConfig struct {
	Host       string        `json:"host,omitempty"`
	Port       int           `json:"port,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	SendData   string        `json:"send_data,omitempty"`   //nolint:tagliatelle // API uses snake_case
	ExpectData string        `json:"expect_data,omitempty"` //nolint:tagliatelle // API uses snake_case
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

	if sendData, ok := configMap["send_data"].(string); ok {
		c.SendData = sendData
	} else if configMap["send_data"] != nil {
		return checkerdef.NewConfigError("send_data", "must be a string")
	}

	if expectData, ok := configMap["expect_data"].(string); ok {
		c.ExpectData = expectData
	} else if configMap["expect_data"] != nil {
		return checkerdef.NewConfigError("expect_data", "must be a string")
	}

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

	if c.SendData != "" {
		cfg["send_data"] = c.SendData
	}

	if c.ExpectData != "" {
		cfg["expect_data"] = c.ExpectData
	}

	return cfg
}
