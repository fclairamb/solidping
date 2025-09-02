// Package config provides configuration management for the CLI.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	jsonparser "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/fclairamb/solidping/server/internal/defaults"
)

var (
	// ErrURLRequired is returned when URL is required but not provided.
	ErrURLRequired = errors.New("url is required")
	// ErrAuthRequired is returned when authentication is required.
	ErrAuthRequired = errors.New("authentication required: either email+password or PAT must be configured")
	// ErrConflictingAuth is returned when both email+password and PAT are configured.
	ErrConflictingAuth = errors.New("conflicting authentication: cannot use both email+password and PAT")
)

// Config represents the CLI configuration.
type Config struct {
	URL  string `koanf:"url"`
	Org  string `koanf:"org"`
	Auth Auth   `koanf:"auth"`
}

// Auth represents authentication configuration.
type Auth struct {
	Email    string `koanf:"email"`
	Password string `koanf:"password"`
	PAT      string `koanf:"pat"`
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "solidping", "settings.json"), nil
}

// TokenPath returns the token file path.
func TokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "solidping", "token.json"), nil
}

// Load loads configuration from the specified path.
func Load(path string) (*Config, error) {
	// Expand home directory if path starts with ~
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, path[1:])
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create default config if it doesn't exist
		if err := EnsureDefaults(path); err != nil {
			return nil, err
		}
	}

	koanfInst := koanf.New(".")
	if err := koanfInst.Load(file.Provider(path), jsonparser.Parser()); err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	config := &Config{
		Org: defaults.Organization, // Set default before unmarshaling
	}
	if err := koanfInst.Unmarshal("", config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Ensure org is set to default if empty
	if config.Org == "" {
		config.Org = defaults.Organization
	}

	return config, nil
}

// Save saves the configuration to the specified path.
func Save(path string, cfg *Config) error {
	// Expand home directory if path starts with ~
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, path[1:])
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil { //nolint:gofumpt // Standard directory permissions
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal config to JSON
	data, err := json.MarshalIndent(cfg, "", "  ") //nolint:musttag // Config struct already has json tags
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file with restricted permissions
	if err := os.WriteFile(path, data, 0600); err != nil { //nolint:gofumpt // Standard file permissions
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// EnsureDefaults creates a default configuration file if it doesn't exist.
func EnsureDefaults(path string) error {
	// Expand home directory if path starts with ~
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, path[1:])
	}

	// Check if file already exists
	if _, err := os.Stat(path); err == nil {
		return nil // File already exists
	}

	// Create default config
	defaultConfig := &Config{
		URL: defaults.ServerURL,
		Org: defaults.Organization,
		Auth: Auth{
			Email:    defaults.Email,
			Password: defaults.Password,
		},
	}

	return Save(path, defaultConfig)
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.URL == "" {
		return ErrURLRequired
	}

	// Check that either email+password OR pat is set, but not both
	hasEmailPassword := c.Auth.Email != "" && c.Auth.Password != ""
	hasPAT := c.Auth.PAT != ""

	if !hasEmailPassword && !hasPAT {
		return ErrAuthRequired
	}

	if hasEmailPassword && hasPAT {
		return ErrConflictingAuth
	}

	return nil
}
