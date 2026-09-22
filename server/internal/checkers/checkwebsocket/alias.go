package checkwebsocket

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket/config"

// WebSocketConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkwebsocket.WebSocketConfig` call site compiling.
type WebSocketConfig = checkconfig.WebSocketConfig

// Bounds and defaults, defined once with the config and aliased here so this
// package's own call sites keep their short names.
const (
	configKeyURL = checkconfig.ConfigKeyURL
)

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultTimeout = checkconfig.DefaultTimeout
)
