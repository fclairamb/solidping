package checkssh

import (
	"crypto/x509"
	"encoding/pem"
	"regexp"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// SSHConfig holds the configuration for SSH checks.
type SSHConfig struct {
	Host                  string        `json:"host"`
	Port                  int           `json:"port,omitempty"`
	Timeout               time.Duration `json:"timeout,omitempty"`
	ExpectedFingerprint   string        `json:"expected_fingerprint,omitempty"` //nolint:tagliatelle
	Username              string        `json:"username,omitempty"`
	Password              string        `json:"password,omitempty"`
	PrivateKey            string        `json:"private_key,omitempty"` //nolint:tagliatelle
	Command               string        `json:"command,omitempty"`
	ExpectedExitCode      int           `json:"expected_exit_code,omitempty"`      //nolint:tagliatelle
	ExpectedOutput        string        `json:"expected_output,omitempty"`         //nolint:tagliatelle
	ExpectedOutputPattern string        `json:"expected_output_pattern,omitempty"` //nolint:tagliatelle

	// Compiled regex (not serialized)
	outputPatternRegex *regexp.Regexp `json:"-"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Config parsing requires handling many optional fields
func (c *SSHConfig) FromMap(configMap map[string]any) error {
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

	if fp, ok := configMap["expected_fingerprint"].(string); ok {
		c.ExpectedFingerprint = fp
	}

	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	}

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	}

	if privateKey, ok := configMap["private_key"].(string); ok {
		c.PrivateKey = privateKey
	}

	if command, ok := configMap["command"].(string); ok {
		c.Command = command
	}

	if exitCode, ok := configMap["expected_exit_code"].(float64); ok {
		c.ExpectedExitCode = int(exitCode)
	} else if exitCode, ok := configMap["expected_exit_code"].(int); ok {
		c.ExpectedExitCode = exitCode
	}

	if output, ok := configMap["expected_output"].(string); ok {
		c.ExpectedOutput = output
	}

	if pattern, ok := configMap["expected_output_pattern"].(string); ok {
		c.ExpectedOutputPattern = pattern
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SSHConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.ExpectedFingerprint != "" {
		cfg["expected_fingerprint"] = c.ExpectedFingerprint
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	if c.PrivateKey != "" {
		cfg["private_key"] = c.PrivateKey
	}

	if c.Command != "" {
		cfg["command"] = c.Command
	}

	if c.ExpectedExitCode != 0 {
		cfg["expected_exit_code"] = c.ExpectedExitCode
	}

	if c.ExpectedOutput != "" {
		cfg["expected_output"] = c.ExpectedOutput
	}

	if c.ExpectedOutputPattern != "" {
		cfg["expected_output_pattern"] = c.ExpectedOutputPattern
	}

	return cfg
}

// Validate checks the configuration for correctness.
//
//nolint:cyclop // SSH config validation has many interdependent fields
func (c *SSHConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > 60*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	if c.Command != "" && c.Username == "" {
		return checkerdef.NewConfigError("command", "requires username to be set")
	}

	if c.ExpectedOutput != "" && c.Command == "" {
		return checkerdef.NewConfigError("expected_output", "requires command to be set")
	}

	if c.ExpectedOutputPattern != "" && c.Command == "" {
		return checkerdef.NewConfigError("expected_output_pattern", "requires command to be set")
	}

	if c.Password != "" && c.PrivateKey != "" {
		return checkerdef.NewConfigError("password", "cannot use both password and private_key")
	}

	if c.Username != "" && c.Password == "" && c.PrivateKey == "" {
		return checkerdef.NewConfigError("username", "requires password or private_key for authentication")
	}

	if c.ExpectedOutputPattern != "" {
		regex, err := regexp.Compile(c.ExpectedOutputPattern)
		if err != nil {
			return checkerdef.NewConfigErrorf("expected_output_pattern", "invalid regex: %v", err)
		}

		c.outputPatternRegex = regex
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

	// Try parsing as various key types — accept if any succeeds
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return nil
	}

	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}

	if _, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return nil
	}

	// Accept anyway — golang.org/x/crypto/ssh can parse OpenSSH format keys
	return nil
}
