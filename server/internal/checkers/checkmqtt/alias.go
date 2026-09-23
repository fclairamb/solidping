package checkmqtt

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmqtt/config"

// MQTTConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkmqtt.MQTTConfig` call site compiling.
type MQTTConfig = checkconfig.MQTTConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
