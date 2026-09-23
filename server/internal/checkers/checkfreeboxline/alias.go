package checkfreeboxline

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline/config"

// FreeboxLineConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkfreeboxline.FreeboxLineConfig` call site compiling.
type FreeboxLineConfig = checkconfig.FreeboxLineConfig

// Defined with the config; aliased so this package keeps the short name.
var (
	ErrResolverNotConfigured = checkconfig.ErrResolverNotConfigured
)

// Defined with the config; aliased so this package keeps the short name.
const (
	LinkTypeFTTH = checkconfig.LinkTypeFTTH
	LinkTypeXDSL = checkconfig.LinkTypeXDSL
)
