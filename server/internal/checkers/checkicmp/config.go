package checkicmp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// ICMPConfig holds the configuration for ICMP ping checks.
type ICMPConfig struct {
	URL        string        `json:"url,omitempty"`
	Host       string        `json:"host,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	Count      int           `json:"count,omitempty"`
	Interval   time.Duration `json:"interval,omitempty"`
	PacketSize int           `json:"packet_size,omitempty"` //nolint:tagliatelle // API uses snake_case
	TTL        int           `json:"ttl,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,nestif // Configuration parsing requires checking multiple field types
func (c *ICMPConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeICMP {
			return checkerdef.NewConfigError("url", "must be an ICMP URL (ping:// or icmp://)")
		}
		c.Host = parsed.Host
	} else {
		// Fall back to legacy host
		if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
			c.Host = host
		} else if configMap[checkerdef.OutputKeyHost] != nil {
			return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
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

	// Extract Count (optional)
	if count, ok := configMap["count"].(int); ok {
		c.Count = count
	} else if countFloat, ok := configMap["count"].(float64); ok {
		// Handle JSON numbers which unmarshal as float64
		c.Count = int(countFloat)
	} else if configMap["count"] != nil {
		return checkerdef.NewConfigError("count", "must be a number")
	}

	// Extract Interval (optional, duration string)
	if interval, ok := configMap["interval"].(string); ok {
		duration, err := time.ParseDuration(interval)
		if err != nil {
			return checkerdef.NewConfigError("interval", "must be a valid duration string")
		}

		c.Interval = duration
	} else if configMap["interval"] != nil {
		return checkerdef.NewConfigError("interval", "must be a string")
	}

	// Extract PacketSize (optional)
	if packetSize, ok := configMap["packet_size"].(int); ok {
		c.PacketSize = packetSize
	} else if packetSizeFloat, ok := configMap["packet_size"].(float64); ok {
		// Handle JSON numbers which unmarshal as float64
		c.PacketSize = int(packetSizeFloat)
	} else if configMap["packet_size"] != nil {
		return checkerdef.NewConfigError("packet_size", "must be a number")
	}

	// Extract TTL (optional)
	if ttl, ok := configMap["ttl"].(int); ok {
		c.TTL = ttl
	} else if ttlFloat, ok := configMap["ttl"].(float64); ok {
		// Handle JSON numbers which unmarshal as float64
		c.TTL = int(ttlFloat)
	} else if configMap["ttl"] != nil {
		return checkerdef.NewConfigError("ttl", "must be a number")
	}

	return nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *ICMPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.URL != "" {
		cfg["url"] = c.URL
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Count != 0 {
		cfg["count"] = c.Count
	}

	if c.Interval != 0 {
		cfg["interval"] = c.Interval.String()
	}

	if c.PacketSize != 0 {
		cfg["packet_size"] = c.PacketSize
	}

	if c.TTL != 0 {
		cfg["ttl"] = c.TTL
	}

	return cfg
}
