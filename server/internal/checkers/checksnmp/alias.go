package checksnmp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksnmp/config"

// SNMPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checksnmp.SNMPConfig` call site compiling.
type SNMPConfig = checkconfig.SNMPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultCommunity = checkconfig.DefaultCommunity
	defaultOperator  = checkconfig.DefaultOperator
	defaultPort      = checkconfig.DefaultPort
	defaultTimeout   = checkconfig.DefaultTimeout
	defaultVersion   = checkconfig.DefaultVersion
)
