package checkmongodb

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmongodb/config"

// MongoDBConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkmongodb.MongoDBConfig` call site compiling.
type MongoDBConfig = checkconfig.MongoDBConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
