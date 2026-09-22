package checkrdp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"

// RDPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkrdp.RDPConfig` call site compiling.
type RDPConfig = checkconfig.RDPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
