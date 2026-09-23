package checkdocker

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdocker/config"

// DockerConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkdocker.DockerConfig` call site compiling.
type DockerConfig = checkconfig.DockerConfig
