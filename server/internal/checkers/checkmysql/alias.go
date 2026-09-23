package checkmysql

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmysql/config"

// MySQLConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkmysql.MySQLConfig` call site compiling.
type MySQLConfig = checkconfig.MySQLConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultQuery   = checkconfig.DefaultQuery
	defaultTimeout = checkconfig.DefaultTimeout
)
