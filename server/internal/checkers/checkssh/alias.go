package checkssh

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkssh/config"

// SSHConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkssh.SSHConfig` call site compiling.
type SSHConfig = checkconfig.SSHConfig

// Bounds and defaults, defined once with the config and aliased here so this
// package's own call sites keep their short names.
const (
	defaultPort = checkconfig.DefaultPort
)
