package checkclickhouse

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse/config"

// ClickHouseConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkclickhouse.ClickHouseConfig` call site compiling.
type ClickHouseConfig = checkconfig.ClickHouseConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultDatabase = checkconfig.DefaultDatabase
	DefaultPort     = checkconfig.DefaultPort
	defaultQuery    = checkconfig.DefaultQuery
	defaultUser     = checkconfig.DefaultUser
	fieldDatabase   = checkconfig.FieldDatabase
)
