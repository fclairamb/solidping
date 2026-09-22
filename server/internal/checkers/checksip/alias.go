package checksip

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksip/config"

// SIPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checksip.SIPConfig` call site compiling.
type SIPConfig = checkconfig.SIPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPortPlain = checkconfig.DefaultPortPlain
	defaultPortTLS   = checkconfig.DefaultPortTLS
	defaultTimeout   = checkconfig.DefaultTimeout
	modeOptions      = checkconfig.ModeOptions
	modeRegister     = checkconfig.ModeRegister
	transportTCP     = checkconfig.TransportTCP
	transportTLS     = checkconfig.TransportTLS
	transportUDP     = checkconfig.TransportUDP
)
