package checkicmp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkicmp/config"

// ICMPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkicmp.ICMPConfig` call site compiling.
type ICMPConfig = checkconfig.ICMPConfig

// Bounds and defaults, defined once with the config and aliased here so this
// package's own call sites keep their short names.
const (
	defaultTimeout  = checkconfig.DefaultTimeout
	defaultCount    = checkconfig.DefaultCount
	defaultInterval = checkconfig.DefaultInterval
	minCount        = checkconfig.MinCount
	maxCount        = checkconfig.MaxCount
	minInterval     = checkconfig.MinInterval
	maxInterval     = checkconfig.MaxInterval
)
