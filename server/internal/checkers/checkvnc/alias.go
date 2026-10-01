package checkvnc

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkvnc/config"

// VNCConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps the `checkvnc.VNCConfig` name.
type VNCConfig = checkconfig.VNCConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort    = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)
