package checks

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// maxConfigTimeout is the uniform server-side cap on the per-check `timeout`
// config key (spec 2026-07-11-05). Individual checkers may enforce the same
// or tighter bounds in their own Validate, but this cap applies to every
// check type at create/update time so the limit is consistent across the
// board and agrees with the worker-side execution clamp.
const maxConfigTimeout = 30 * time.Second

// configKeyTimeout is the check-config JSONB key holding the optional
// per-check timeout duration string (spec 2026-07-11-05).
const configKeyTimeout = "timeout"

// validateConfigTimeout enforces the uniform per-check timeout rule on a
// check's config map: when the `timeout` key is present it must be a duration
// string that parses to > 0 and <= 30s. An absent (or nil) key is fine — the
// checker/server defaults apply. Returns a checkerdef.ConfigError so handlers
// surface it as a 400 VALIDATION_ERROR on the `timeout` parameter.
func validateConfigTimeout(config map[string]any) error {
	raw, exists := config[configKeyTimeout]
	if !exists || raw == nil {
		return nil
	}

	str, ok := raw.(string)
	if !ok {
		return checkerdef.NewConfigError(configKeyTimeout, `must be a duration string (e.g. "10s")`)
	}

	duration, err := time.ParseDuration(str)
	if err != nil {
		return checkerdef.NewConfigError(configKeyTimeout, `must be a valid duration string (e.g. "10s")`)
	}

	if duration <= 0 || duration > maxConfigTimeout {
		return checkerdef.NewConfigErrorf(
			configKeyTimeout, "must be > 0 and <= %s, got %s", maxConfigTimeout, duration,
		)
	}

	return nil
}
