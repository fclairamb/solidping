// Package config holds the email check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkemail package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import "github.com/fclairamb/solidping/server/internal/checkers/checkerdef"

// EmailConfig holds the configuration for email passive checks. The token
// is the secret part of the unique address (`<token>@<addressDomain>`) — the
// addressDomain is taken from the email_inbox system parameter at the time
// the address is rendered, not stored on the check.
type EmailConfig struct {
	Token string `json:"token,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *EmailConfig) FromMap(configMap map[string]any) error {
	if token, ok := configMap["token"].(string); ok {
		c.Token = token
	} else if configMap["token"] != nil {
		return checkerdef.NewConfigError("token", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *EmailConfig) GetConfig() map[string]any {
	cfg := make(map[string]any)

	if c.Token != "" {
		cfg["token"] = c.Token
	}

	return cfg
}
