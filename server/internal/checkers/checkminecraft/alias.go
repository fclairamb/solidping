package checkminecraft

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkminecraft/config"

// MinecraftConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkminecraft.MinecraftConfig` call site compiling.
type MinecraftConfig = checkconfig.MinecraftConfig

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	EditionJava    = checkconfig.EditionJava
	EditionBedrock = checkconfig.EditionBedrock
)
