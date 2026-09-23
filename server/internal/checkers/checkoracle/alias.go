package checkoracle

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkoracle/config"

// OracleConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkoracle.OracleConfig` call site compiling.
type OracleConfig = checkconfig.OracleConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultQuery   = checkconfig.DefaultQuery
	defaultTimeout = checkconfig.DefaultTimeout
)
