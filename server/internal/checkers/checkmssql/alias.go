package checkmssql

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmssql/config"

// MSSQLConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkmssql.MSSQLConfig` call site compiling.
type MSSQLConfig = checkconfig.MSSQLConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultQuery   = checkconfig.DefaultQuery
	defaultTimeout = checkconfig.DefaultTimeout
)
