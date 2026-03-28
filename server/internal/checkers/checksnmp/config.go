package checksnmp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort    = 161
	defaultVersion = "2c"
	// defaultCommunity is the default SNMP community string.
	defaultCommunity = "public"
	defaultTimeout   = 10 * time.Second
	maxTimeout       = 60 * time.Second
	defaultOperator  = "equals"
)

// SNMPConfig holds the configuration for SNMP health checks.
type SNMPConfig struct {
	Host          string        `json:"host"`
	Port          int           `json:"port,omitempty"`
	Version       string        `json:"version,omitempty"`
	Community     string        `json:"community,omitempty"`
	OID           string        `json:"oid"`
	ExpectedValue string        `json:"expectedValue,omitempty"`
	Operator      string        `json:"operator,omitempty"`
	Username      string        `json:"username,omitempty"`
	AuthProtocol  string        `json:"authProtocol,omitempty"`
	AuthPassword  string        `json:"authPassword,omitempty"`
	PrivProtocol  string        `json:"privProtocol,omitempty"`
	PrivPassword  string        `json:"privPassword,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,gocognit // Configuration parsing requires checking multiple field types
func (c *SNMPConfig) FromMap(configMap map[string]any) error {
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

	if version, ok := configMap["version"].(string); ok {
		c.Version = version
	} else if configMap["version"] != nil {
		return checkerdef.NewConfigError("version", "must be a string")
	}

	if community, ok := configMap["community"].(string); ok {
		c.Community = community
	} else if configMap["community"] != nil {
		return checkerdef.NewConfigError("community", "must be a string")
	}

	if oid, ok := configMap["oid"].(string); ok {
		c.OID = oid
	} else if configMap["oid"] != nil {
		return checkerdef.NewConfigError("oid", "must be a string")
	}

	if err := c.parseExpectedFields(configMap); err != nil {
		return err
	}

	if err := c.parseAuthFields(configMap); err != nil {
		return err
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

	return nil
}

func (c *SNMPConfig) parseExpectedFields(configMap map[string]any) error {
	if expectedValue, ok := configMap["expectedValue"].(string); ok {
		c.ExpectedValue = expectedValue
	} else if configMap["expectedValue"] != nil {
		return checkerdef.NewConfigError("expectedValue", "must be a string")
	}

	if operator, ok := configMap["operator"].(string); ok {
		c.Operator = operator
	} else if configMap["operator"] != nil {
		return checkerdef.NewConfigError("operator", "must be a string")
	}

	return nil
}

func (c *SNMPConfig) parseAuthFields(configMap map[string]any) error {
	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	if authProtocol, ok := configMap["authProtocol"].(string); ok {
		c.AuthProtocol = authProtocol
	} else if configMap["authProtocol"] != nil {
		return checkerdef.NewConfigError("authProtocol", "must be a string")
	}

	if authPassword, ok := configMap["authPassword"].(string); ok {
		c.AuthPassword = authPassword
	} else if configMap["authPassword"] != nil {
		return checkerdef.NewConfigError("authPassword", "must be a string")
	}

	if privProtocol, ok := configMap["privProtocol"].(string); ok {
		c.PrivProtocol = privProtocol
	} else if configMap["privProtocol"] != nil {
		return checkerdef.NewConfigError("privProtocol", "must be a string")
	}

	if privPassword, ok := configMap["privPassword"].(string); ok {
		c.PrivPassword = privPassword
	} else if configMap["privPassword"] != nil {
		return checkerdef.NewConfigError("privPassword", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SNMPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
		"oid":  c.OID,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Version != "" && c.Version != defaultVersion {
		cfg["version"] = c.Version
	}

	if c.Community != "" && c.Community != defaultCommunity {
		cfg["community"] = c.Community
	}

	if c.ExpectedValue != "" {
		cfg["expectedValue"] = c.ExpectedValue
	}

	if c.Operator != "" && c.Operator != defaultOperator {
		cfg["operator"] = c.Operator
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.AuthProtocol != "" {
		cfg["authProtocol"] = c.AuthProtocol
	}

	if c.AuthPassword != "" {
		cfg["authPassword"] = c.AuthPassword
	}

	if c.PrivProtocol != "" {
		cfg["privProtocol"] = c.PrivProtocol
	}

	if c.PrivPassword != "" {
		cfg["privPassword"] = c.PrivPassword
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// Validate checks if the configuration is valid.
//
//nolint:cyclop // Validation requires checking multiple interdependent fields
func (c *SNMPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.OID == "" {
		return checkerdef.NewConfigError("oid", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if err := c.validateVersion(); err != nil {
		return err
	}

	if err := c.validateOperator(); err != nil {
		return err
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String(),
		)
	}

	return c.validateV3Auth()
}

func (c *SNMPConfig) validateVersion() error {
	if c.Version != "" {
		switch c.Version {
		case "1", "2c", "3":
			// valid
		default:
			return checkerdef.NewConfigErrorf(
				"version", "must be one of: 1, 2c, 3; got %q", c.Version,
			)
		}
	}

	return nil
}

func (c *SNMPConfig) validateOperator() error {
	if c.Operator != "" {
		switch c.Operator {
		case "equals", "contains", "greater_than", "less_than", "not_equals":
			// valid
		default:
			return checkerdef.NewConfigErrorf(
				"operator",
				"must be one of: equals, contains, greater_than, less_than, not_equals; got %q",
				c.Operator,
			)
		}
	}

	return nil
}

func (c *SNMPConfig) validateV3Auth() error {
	version := c.Version
	if version == "" {
		version = defaultVersion
	}

	if version == "3" && c.Username == "" {
		return checkerdef.NewConfigError("username", "is required for SNMP v3")
	}

	if c.PrivProtocol != "" && c.AuthProtocol == "" {
		return checkerdef.NewConfigError(
			"authProtocol", "is required when privProtocol is set",
		)
	}

	if c.AuthProtocol != "" {
		switch c.AuthProtocol {
		case "MD5", "SHA", "SHA-256", "SHA-512":
			// valid
		default:
			return checkerdef.NewConfigErrorf(
				"authProtocol",
				"must be one of: MD5, SHA, SHA-256, SHA-512; got %q",
				c.AuthProtocol,
			)
		}
	}

	if c.PrivProtocol != "" {
		switch c.PrivProtocol {
		case "DES", "AES", "AES-192", "AES-256":
			// valid
		default:
			return checkerdef.NewConfigErrorf(
				"privProtocol",
				"must be one of: DES, AES, AES-192, AES-256; got %q",
				c.PrivProtocol,
			)
		}
	}

	return nil
}
