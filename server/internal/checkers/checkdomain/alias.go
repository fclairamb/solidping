package checkdomain

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdomain/config"

// DomainConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkdomain.DomainConfig` call site compiling.
type DomainConfig = checkconfig.DomainConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	MethodRDAP  = checkconfig.MethodRDAP
	MethodWHOIS = checkconfig.MethodWHOIS
)
