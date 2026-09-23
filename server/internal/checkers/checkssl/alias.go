package checkssl

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkssl/config"

// SSLConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkssl.SSLConfig` call site compiling.
type SSLConfig = checkconfig.SSLConfig

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
