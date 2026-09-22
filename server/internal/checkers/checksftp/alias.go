package checksftp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksftp/config"

// SFTPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checksftp.SFTPConfig` call site compiling.
type SFTPConfig = checkconfig.SFTPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort       = checkconfig.DefaultPort
	defaultTimeout    = checkconfig.DefaultTimeout
	microsecondsPerMs = checkconfig.MicrosecondsPerMs
)
