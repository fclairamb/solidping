package checkdns

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// DNSConfig holds the configuration for DNS checks.
type DNSConfig struct {
	URL            string        `json:"url,omitempty"`
	Host           string        `json:"host,omitempty"` // Domain to query (renamed from Hostname)
	Timeout        time.Duration `json:"timeout,omitempty"`
	Nameserver     string        `json:"nameserver,omitempty"`
	RecordType     string        `json:"record_type,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectedIPs    []string      `json:"expected_ips,omitempty"`    //nolint:tagliatelle // API uses snake_case
	ExpectedValues []string      `json:"expected_values,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,gocognit,nestif // Configuration parsing requires checking multiple field types
func (c *DNSConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	// Format: dns://resolver/domain?type=A
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeDNS {
			return checkerdef.NewConfigError("url", "must be a DNS URL (dns://)")
		}
		// Domain to query comes from the path
		c.Host = parsed.DNSDomain
		c.RecordType = parsed.RecordType
		// Resolver comes from the host (empty = system resolver)
		if parsed.Resolver() != "" {
			c.Nameserver = parsed.Resolver()
		}
	} else {
		// Fall back to legacy host/hostname
		if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
			c.Host = host
		} else if hostname, ok := configMap["hostname"].(string); ok {
			// Backward compatibility with old field name
			c.Host = hostname
		} else if configMap[checkerdef.OutputKeyHost] != nil {
			return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
		} else if configMap["hostname"] != nil {
			return checkerdef.NewConfigError("hostname", "must be a string")
		}

		// Extract Nameserver (optional) - only in legacy mode
		if nameserver, ok := configMap["nameserver"].(string); ok {
			c.Nameserver = nameserver
		} else if configMap["nameserver"] != nil {
			return checkerdef.NewConfigError("nameserver", "must be a string")
		}

		// Extract RecordType (optional) - only in legacy mode
		if recordType, ok := configMap["record_type"].(string); ok {
			c.RecordType = recordType
		} else if configMap["record_type"] != nil {
			return checkerdef.NewConfigError("record_type", "must be a string")
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

	// Extract ExpectedIPs (optional, array of strings)
	if expectedIPs, ok := configMap["expected_ips"].([]string); ok {
		c.ExpectedIPs = expectedIPs
	} else if expectedIPsAny, ok := configMap["expected_ips"].([]any); ok {
		// Handle []any and convert to []string
		c.ExpectedIPs = make([]string, len(expectedIPsAny))
		for i, v := range expectedIPsAny {
			if strVal, ok := v.(string); ok {
				c.ExpectedIPs[i] = strVal
			} else {
				return checkerdef.NewConfigError("expected_ips", "must be a string array")
			}
		}
	} else if configMap["expected_ips"] != nil {
		return checkerdef.NewConfigError("expected_ips", "must be a string array")
	}

	// Extract ExpectedValues (optional, array of strings)
	if expectedValues, ok := configMap["expected_values"].([]string); ok {
		c.ExpectedValues = expectedValues
	} else if expectedValuesAny, ok := configMap["expected_values"].([]any); ok {
		// Handle []any and convert to []string
		c.ExpectedValues = make([]string, len(expectedValuesAny))
		for i, v := range expectedValuesAny {
			if strVal, ok := v.(string); ok {
				c.ExpectedValues[i] = strVal
			} else {
				return checkerdef.NewConfigError("expected_values", "must be a string array")
			}
		}
	} else if configMap["expected_values"] != nil {
		return checkerdef.NewConfigError("expected_values", "must be a string array")
	}

	return nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *DNSConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.URL != "" {
		cfg["url"] = c.URL
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Nameserver != "" {
		cfg["nameserver"] = c.Nameserver
	}

	if c.RecordType != "" {
		cfg["record_type"] = c.RecordType
	}

	if len(c.ExpectedIPs) > 0 {
		cfg["expected_ips"] = c.ExpectedIPs
	}

	if len(c.ExpectedValues) > 0 {
		cfg["expected_values"] = c.ExpectedValues
	}

	return cfg
}
