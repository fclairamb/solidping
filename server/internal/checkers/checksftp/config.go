package checksftp

import (
	"crypto/x509"
	"encoding/pem"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort       = 22
	defaultTimeout    = 10 * time.Second
	maxTimeout        = 60 * time.Second
	microsecondsPerMs = 1000.0
)

// SFTPConfig holds the configuration for SFTP checks.
type SFTPConfig struct {
	Host       string        `json:"host"`
	Port       int           `json:"port,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	Username   string        `json:"username"`
	Password   string        `json:"password,omitempty"`
	PrivateKey string        `json:"private_key,omitempty"` //nolint:tagliatelle // API uses snake_case
	Path       string        `json:"path,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *SFTPConfig) FromMap(configMap map[string]any) error {
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

	if privateKey, ok := configMap["private_key"].(string); ok {
		c.PrivateKey = privateKey
	} else if configMap["private_key"] != nil {
		return checkerdef.NewConfigError("private_key", "must be a string")
	}

	if path, ok := configMap["path"].(string); ok {
		c.Path = path
	} else if configMap["path"] != nil {
		return checkerdef.NewConfigError("path", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SFTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host":     c.Host,
		"username": c.Username,
	}

	if c.Port != 0 {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.PrivateKey != "" {
		cfg["private_key"] = c.PrivateKey
	}

	if c.Path != "" {
		cfg["path"] = c.Path
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *SFTPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	if c.Username == "" {
		return checkerdef.NewConfigError("username", "is required")
	}

	if c.Password == "" && c.PrivateKey == "" {
		return checkerdef.NewConfigError("password", "password or private_key is required")
	}

	if c.Password != "" && c.PrivateKey != "" {
		return checkerdef.NewConfigError("password", "cannot use both password and private_key")
	}

	if c.PrivateKey != "" {
		if err := validatePrivateKey(c.PrivateKey); err != nil {
			return err
		}
	}

	return nil
}

func validatePrivateKey(key string) error {
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return checkerdef.NewConfigError("private_key", "invalid PEM format")
	}

	// Try parsing as various key types - accept if any succeeds
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return nil
	}

	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}

	if _, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return nil
	}

	// Accept anyway - golang.org/x/crypto/ssh can parse OpenSSH format keys
	return nil
}
