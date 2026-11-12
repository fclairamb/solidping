// Package config holds the email check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkemail package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"slices"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config keys of the optional sender-authentication options.
const (
	KeyRequireSPF      = "require_spf"
	KeyRequireDKIM     = "require_dkim"
	KeyRequireDMARC    = "require_dmarc"
	KeyCheckBlocklists = "check_blocklists"
	KeyBlocklists      = "blocklists"
)

// SPF requirement levels (a minimum, see SPFAccepts).
const (
	SPFNone     = "none"
	SPFSoftfail = "softfail"
	SPFPass     = "pass"
)

// DMARCPass is the only DMARC requirement level.
const DMARCPass = "pass"

// EmailConfig holds the configuration for email passive checks. The token
// is the secret part of the unique address (`<token>@<addressDomain>`) — the
// addressDomain is taken from the email_inbox system parameter at the time
// the address is rendered, not stored on the check.
//
// The remaining fields are the optional sender-authentication policy: all of
// them absent means a delivered message is "up" and nothing more is evaluated.
type EmailConfig struct {
	Token string `json:"token,omitempty"`
	// RequireSPF is "" (off), "none", "softfail" or "pass".
	RequireSPF string `json:"require_spf,omitempty"` //nolint:tagliatelle // API uses snake_case
	// RequireDKIM requires one valid signature aligned with the From domain.
	RequireDKIM bool `json:"require_dkim,omitempty"` //nolint:tagliatelle // API uses snake_case
	// RequireDMARC is "" (off) or "pass".
	RequireDMARC string `json:"require_dmarc,omitempty"` //nolint:tagliatelle // API uses snake_case
	// CheckBlocklists looks the sending IP up on DNS blocklists.
	CheckBlocklists bool `json:"check_blocklists,omitempty"` //nolint:tagliatelle // API uses snake_case
	// Blocklists are the DNSBL zones; empty means the dnsbl checker defaults.
	// Setting any implies CheckBlocklists.
	Blocklists []string `json:"blocklists,omitempty"`
}

// AuthEnabled reports whether any sender-authentication option is set.
func (c *EmailConfig) AuthEnabled() bool {
	return c.RequireSPF != "" || c.RequireDKIM || c.RequireDMARC != "" || c.BlocklistsEnabled()
}

// BlocklistsEnabled reports whether the sending IP must be looked up on blocklists.
func (c *EmailConfig) BlocklistsEnabled() bool {
	return c.CheckBlocklists || len(c.Blocklists) > 0
}

// SPFAccepts reports whether the SPF verdict satisfies the RequireSPF minimum.
// An empty requirement accepts everything.
func (c *EmailConfig) SPFAccepts(verdict string) bool {
	switch c.RequireSPF {
	case SPFPass:
		return verdict == "pass"
	case SPFSoftfail:
		return slices.Contains([]string{"pass", "neutral", "softfail"}, verdict)
	case SPFNone:
		return verdict != "fail" && verdict != "permerror"
	default:
		return true
	}
}

// FromMap populates the configuration from a map.
func (c *EmailConfig) FromMap(configMap map[string]any) error {
	if token, ok := configMap["token"].(string); ok {
		c.Token = token
	} else if configMap["token"] != nil {
		return checkerdef.NewConfigError("token", "must be a string")
	}

	if err := c.authFromMap(configMap); err != nil {
		return err
	}

	return nil
}

func (c *EmailConfig) authFromMap(configMap map[string]any) error {
	for key, dst := range map[string]*string{
		KeyRequireSPF:   &c.RequireSPF,
		KeyRequireDMARC: &c.RequireDMARC,
	} {
		if v, ok := configMap[key].(string); ok {
			*dst = v
		} else if configMap[key] != nil {
			return checkerdef.NewConfigError(key, "must be a string")
		}
	}

	for key, dst := range map[string]*bool{
		KeyRequireDKIM:     &c.RequireDKIM,
		KeyCheckBlocklists: &c.CheckBlocklists,
	} {
		if v, ok := configMap[key].(bool); ok {
			*dst = v
		} else if configMap[key] != nil {
			return checkerdef.NewConfigError(key, "must be a boolean")
		}
	}

	switch raw := configMap[KeyBlocklists].(type) {
	case nil:
	case []string:
		c.Blocklists = slices.Clone(raw)
	case []any:
		zones := make([]string, 0, len(raw))

		for _, item := range raw {
			zone, ok := item.(string)
			if !ok {
				return checkerdef.NewConfigError(KeyBlocklists, "must be a list of strings")
			}

			zones = append(zones, zone)
		}

		c.Blocklists = zones
	default:
		return checkerdef.NewConfigError(KeyBlocklists, "must be a list of strings")
	}

	return nil
}

// GetConfig returns the configuration as a map. Unset options add no key.
func (c *EmailConfig) GetConfig() map[string]any {
	cfg := make(map[string]any)

	if c.Token != "" {
		cfg["token"] = c.Token
	}

	if c.RequireSPF != "" {
		cfg[KeyRequireSPF] = c.RequireSPF
	}

	if c.RequireDKIM {
		cfg[KeyRequireDKIM] = true
	}

	if c.RequireDMARC != "" {
		cfg[KeyRequireDMARC] = c.RequireDMARC
	}

	if c.CheckBlocklists {
		cfg[KeyCheckBlocklists] = true
	}

	if len(c.Blocklists) > 0 {
		cfg[KeyBlocklists] = c.Blocklists
	}

	return cfg
}
