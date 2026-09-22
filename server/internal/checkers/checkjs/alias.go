package checkjs

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"

// JSConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkjs.JSConfig` call site compiling.
type JSConfig = checkconfig.JSConfig

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	defaultTimeout = checkconfig.DefaultTimeout
	maxTimeout     = checkconfig.MaxTimeout
)

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	maxEnvEntries = checkconfig.MaxEnvEntries
)
