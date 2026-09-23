package checkpostgres

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkpostgres/config"

// PostgreSQLConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkpostgres.PostgreSQLConfig` call site compiling.
type PostgreSQLConfig = checkconfig.PostgreSQLConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultQuery   = checkconfig.DefaultQuery
	defaultTimeout = checkconfig.DefaultTimeout
)
