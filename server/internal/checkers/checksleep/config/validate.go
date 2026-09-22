package config

import (
	"fmt"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ValidateSpec validates a check spec offline: it parses the config, applies every
// rule, and fills in the spec defaults (name, slug) the create path relies on. It
// lives here rather than on the checker so `sp checks validate` reaches it without
// linking the execution client.
func ValidateSpec(spec *checkerdef.CheckSpec) error {
	cfg := &SleepConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.SleepMs <= 0 {
		return checkerdef.NewConfigError("sleep_ms", "is required and must be > 0")
	}

	if cfg.SleepMs > MaxSleepMs {
		return checkerdef.NewConfigErrorf("sleep_ms", "must be <= %d, got %d", MaxSleepMs, cfg.SleepMs)
	}

	if cfg.JitterMs < 0 {
		return checkerdef.NewConfigError("jitter_ms", "must be >= 0")
	}

	if cfg.JitterMs >= cfg.SleepMs {
		return checkerdef.NewConfigErrorf("jitter_ms", "must be < sleep_ms (%d), got %d", cfg.SleepMs, cfg.JitterMs)
	}

	switch cfg.Status {
	case "", StatusUp, StatusDown, StatusTimeout, StatusError:
		// valid
	default:
		return checkerdef.NewConfigErrorf("status", "must be one of up|down|timeout|error, got %q", cfg.Status)
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("sleep-%dms", cfg.SleepMs)
	}

	if spec.Slug == "" {
		spec.Slug = truncateSlug(fmt.Sprintf("sleep-%dms", cfg.SleepMs))
	}

	return nil
}
