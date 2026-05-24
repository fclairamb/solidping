package checkdnsbl

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// defaultBlocklists are the IP-based DNS blocklist zones queried when the
// config provides none. SORBS (dnsbl.sorbs.net) is intentionally excluded —
// it shut down in 2024.
//
//nolint:gochecknoglobals // read-only default list
var defaultBlocklists = []string{
	"zen.spamhaus.org",
	"bl.spamcop.net",
	"b.barracudacentral.org",
	"dnsbl-1.uceprotect.net",
}

// DNSBLConfig holds the configuration for DNSBL (blocklist) checks.
type DNSBLConfig struct {
	Target     string        `json:"target"`               // IPv4 address or hostname
	Blocklists []string      `json:"blocklists,omitempty"` // DNS zones; defaults applied when empty
	Nameserver string        `json:"nameserver,omitempty"` // custom resolver host:port (optional)
	Timeout    time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *DNSBLConfig) FromMap(configMap map[string]any) error {
	// Extract Target (required string)
	if target, ok := configMap["target"].(string); ok {
		c.Target = target
	} else if configMap["target"] != nil {
		return checkerdef.NewConfigError("target", "must be a string")
	}

	// Extract Nameserver (optional string)
	if nameserver, ok := configMap["nameserver"].(string); ok {
		c.Nameserver = nameserver
	} else if configMap["nameserver"] != nil {
		return checkerdef.NewConfigError("nameserver", "must be a string")
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

	// Extract Blocklists (optional, array of strings).
	// JSONB decodes arrays as []any, so we must coerce that case too.
	if blocklists, ok := configMap["blocklists"].([]string); ok {
		c.Blocklists = blocklists
	} else if blocklistsAny, ok := configMap["blocklists"].([]any); ok {
		c.Blocklists = make([]string, len(blocklistsAny))
		for i, v := range blocklistsAny {
			strVal, ok := v.(string)
			if !ok {
				return checkerdef.NewConfigError("blocklists", "must be a string array")
			}

			c.Blocklists[i] = strVal
		}
	} else if configMap["blocklists"] != nil {
		return checkerdef.NewConfigError("blocklists", "must be a string array")
	}

	return nil
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *DNSBLConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"target": c.Target,
	}

	if len(c.Blocklists) > 0 {
		cfg["blocklists"] = c.Blocklists
	}

	if c.Nameserver != "" {
		cfg["nameserver"] = c.Nameserver
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	return cfg
}

// resolveBlocklists returns the configured zones, or the defaults when empty.
func (c *DNSBLConfig) resolveBlocklists() []string {
	if len(c.Blocklists) > 0 {
		return c.Blocklists
	}

	return defaultBlocklists
}
