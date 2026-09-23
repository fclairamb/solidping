package checkgrpc

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkgrpc/config"

// GRPCConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkgrpc.GRPCConfig` call site compiling.
type GRPCConfig = checkconfig.GRPCConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort = checkconfig.DefaultPort
)
