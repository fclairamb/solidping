package checka2s

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checka2s/config"

// A2SConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checka2s.A2SConfig` call site compiling.
type A2SConfig = checkconfig.A2SConfig

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	defaultPort = checkconfig.DefaultPort
)
