package checkntp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkntp/config"

// NTPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkntp.NTPConfig` call site compiling.
type NTPConfig = checkconfig.NTPConfig

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	defaultTimeout = checkconfig.DefaultTimeout
	defaultPort    = checkconfig.DefaultPort
	defaultVersion = checkconfig.DefaultVersion
)
