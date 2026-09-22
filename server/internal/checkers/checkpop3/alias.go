package checkpop3

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkpop3/config"

// POP3Config is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkpop3.POP3Config` call site compiling.
type POP3Config = checkconfig.POP3Config

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort     = checkconfig.DefaultPort
	defaultTimeout  = checkconfig.DefaultTimeout
	implicitTLSPort = checkconfig.ImplicitTLSPort
)
