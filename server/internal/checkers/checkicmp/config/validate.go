package config

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
//
//nolint:cyclop // Validation requires checking multiple fields
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &ICMPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Count (1-600) - check the original value if set
	if cfg.Count != 0 && (cfg.Count < MinCount || cfg.Count > MaxCount) {
		return checkerdef.NewConfigErrorf("count", "must be between %d and %d, got %d", MinCount, MaxCount, cfg.Count)
	}

	// Validate Interval (10ms - 60s) - check the original value if set
	if cfg.Interval != 0 && (cfg.Interval < MinInterval || cfg.Interval > MaxInterval) {
		return checkerdef.NewConfigErrorf(
			"interval", "must be between %s and %s, got %s", MinInterval, MaxInterval, cfg.Interval)
	}

	// Validate PacketSize (0 - 65507)
	if cfg.PacketSize < 0 || cfg.PacketSize > 65507 {
		return checkerdef.NewConfigErrorf("packet_size", "must be between 0 and 65507 bytes, got %d", cfg.PacketSize)
	}

	// Validate TTL (1 - 255) - check the original value if set
	if cfg.TTL != 0 && (cfg.TTL < 1 || cfg.TTL > 255) {
		return checkerdef.NewConfigErrorf("ttl", "must be between 1 and 255, got %d", cfg.TTL)
	}

	// Validate Timeout (> 0 and <= 30s) - check the original value if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	if spec.Slug == "" {
		spec.Slug = "icmp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}
