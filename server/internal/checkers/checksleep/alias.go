package checksleep

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksleep/config"

// SleepConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checksleep.SleepConfig` call site compiling.
type SleepConfig = checkconfig.SleepConfig

// Bounds and defaults, defined once with the config and aliased here so this
// package's own call sites keep their short names.
const (
	maxSleepMs    = checkconfig.MaxSleepMs
	slugMaxLen    = checkconfig.SlugMaxLen
	statusUp      = checkconfig.StatusUp
	statusDown    = checkconfig.StatusDown
	statusTimeout = checkconfig.StatusTimeout
	statusError   = checkconfig.StatusError
)

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	configKeySleepMs = checkconfig.ConfigKeySleepMs
)
