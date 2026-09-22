package checkftp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkftp/config"

// FTPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkftp.FTPConfig` call site compiling.
type FTPConfig = checkconfig.FTPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	TLSModeExplicit   = checkconfig.TLSModeExplicit
	TLSModeImplicit   = checkconfig.TLSModeImplicit
	defaultPort       = checkconfig.DefaultPort
	defaultTimeout    = checkconfig.DefaultTimeout
	defaultUsername   = checkconfig.DefaultUsername
	implicitTLSPort   = checkconfig.ImplicitTLSPort
	microsecondsPerMs = checkconfig.MicrosecondsPerMs
)
