package checkredis

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkredis/config"

// RedisConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkredis.RedisConfig` call site compiling.
type RedisConfig = checkconfig.RedisConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
