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

	if cfg.Socket != "" {
		if err := validateSocket(cfg); err != nil {
			return err
		}
	} else if err := validateHostPort(cfg); err != nil {
		return err
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
		target := cfg.Host
		if cfg.Socket != "" {
			target = strings.Trim(strings.NewReplacer("/", "-", ".", "-").Replace(cfg.Socket), "-")
		}

		spec.Slug = "tcp-" + strings.ReplaceAll(target, ".", "-")
	}

	return nil
}

// validateSocket applies the socket-target rules: no host, port or TLS next
// to it, and the shared path syntax (the allowlist is the server's business,
// applied at save time and again at dial time).
func validateSocket(cfg *TCPConfig) error {
	if cfg.Host != "" || cfg.Port != 0 {
		return checkerdef.NewConfigError(configKeySocket, "socket and host/port are mutually exclusive")
	}

	if cfg.TLS {
		return checkerdef.NewConfigError("tls", "TLS is not supported over a Unix socket")
	}

	if err := checkerdef.ValidateLocalSocketPath(cfg.Socket); err != nil {
		return checkerdef.NewConfigError(configKeySocket, err.Error())
	}

	return nil
}

// validateHostPort applies the host:port target rules.
func validateHostPort(cfg *TCPConfig) error {
	if cfg.Host == "" {
		return checkerdef.NewRequiredConfigError("host")
	}

	// Validate Port
	if cfg.Port == 0 {
		return checkerdef.NewRequiredConfigError("port")
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", cfg.Port)
	}

	return nil
}
