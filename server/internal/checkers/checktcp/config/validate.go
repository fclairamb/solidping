package config

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies
// every rule, and fills in the spec defaults (name, slug) the create path
// relies on. It lives here, not on the checker, so `sp checks validate` can
// reach it without linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &TCPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Port
	if cfg.Port == 0 {
		return checkerdef.NewConfigError("port", "is required")
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", cfg.Port)
	}

	// Validate Timeout (> 0 and <= 30s) - check the original value if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	// A bad encoding or an uncompilable `expect_pattern` is a VALIDATION_ERROR
	// on save, never a check that errors forever at runtime.
	if err := cfg.exchangeFields().Validate(); err != nil {
		return err
	}

	if spec.Slug == "" {
		spec.Slug = "tcp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}
