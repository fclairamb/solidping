package checkimap

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkimap/config"

// IMAPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkimap.IMAPConfig` call site compiling.
type IMAPConfig = checkconfig.IMAPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort     = checkconfig.DefaultPort
	defaultTimeout  = checkconfig.DefaultTimeout
	implicitTLSPort = checkconfig.ImplicitTLSPort
)
