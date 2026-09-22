package config

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &DNSBLConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.Target == "" {
		return checkerdef.NewConfigError(KeyTarget, "is required")
	}

	if cfg.Nameserver != "" && !strings.Contains(cfg.Nameserver, ":") {
		return checkerdef.NewConfigErrorf("nameserver", "must be in format host:port, got %s", cfg.Nameserver)
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > MaxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	if spec.Slug == "" {
		spec.Slug = "dnsbl-" + strings.ReplaceAll(cfg.Target, ".", "-")
	}

	return nil
}
