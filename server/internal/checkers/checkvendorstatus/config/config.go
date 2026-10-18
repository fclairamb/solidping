// Package config holds the vendor-status check's configuration: the struct, its
// map parsing and serialization and the offline rule set (ValidateSpec). Like
// the other check types it is free of the execution client.
package config

import (
	"slices"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// MaxComponents caps the `components` list.
	MaxComponents = 100

	keyURL        = "url"
	keyComponents = "components"
	keyProvider   = "provider"
)

// VendorStatusConfig is the configuration of a vendor-status check.
type VendorStatusConfig struct {
	// URL is the status page root, e.g. https://www.githubstatus.com.
	URL string `json:"url"`
	// Components lists the ids of the components that are watched. Empty means
	// the whole page (its overall indicator).
	Components []string `json:"components,omitempty"`
	// Provider is the catalog slug the page was picked from. Informational only.
	Provider string `json:"provider,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *VendorStatusConfig) FromMap(configMap map[string]any) error {
	for key, target := range map[string]*string{keyURL: &c.URL, keyProvider: &c.Provider} {
		raw, ok := configMap[key]
		if !ok || raw == nil {
			continue
		}

		text, isString := raw.(string)
		if !isString {
			return checkerdef.NewConfigError(key, "must be a string")
		}

		*target = text
	}

	switch value := configMap[keyComponents].(type) {
	case nil:
		c.Components = nil
	case []string:
		c.Components = slices.Clone(value)
	case []any:
		c.Components = make([]string, 0, len(value))

		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return checkerdef.NewConfigError(keyComponents, "must be a list of strings")
			}

			c.Components = append(c.Components, text)
		}
	default:
		return checkerdef.NewConfigError(keyComponents, "must be a list of strings")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *VendorStatusConfig) GetConfig() map[string]any {
	cfg := map[string]any{keyURL: c.URL}

	if len(c.Components) > 0 {
		components := make([]any, len(c.Components))
		for i, id := range c.Components {
			components[i] = id
		}

		cfg[keyComponents] = components
	}

	if c.Provider != "" {
		cfg[keyProvider] = c.Provider
	}

	return cfg
}

// Validate applies the vendor-status rules.
func (c *VendorStatusConfig) Validate() error {
	if c.URL == "" {
		return checkerdef.NewConfigError(keyURL, "is required")
	}

	if !strings.HasPrefix(c.URL, "https://") {
		return checkerdef.NewConfigError(keyURL, "must start with https://")
	}

	if len(c.Components) > MaxComponents {
		return checkerdef.NewConfigErrorf(keyComponents, "at most %d components can be watched", MaxComponents)
	}

	for _, id := range c.Components {
		if id == "" {
			return checkerdef.NewConfigError(keyComponents, "component ids must not be empty")
		}
	}

	return nil
}

// SchemaNotes implements checkerdef.SchemaNoter.
func (c *VendorStatusConfig) SchemaNotes() []string {
	return []string{
		"url is the status page root (https only). A trailing slash and an /api/v2/... suffix are stripped. " +
			"Only pages serving the Atlassian Statuspage v2 summary (/api/v2/summary.json) are supported.",
		"components lists component ids; empty watches the whole page. A listed id that the page no " +
			"longer has makes the check error.",
	}
}
