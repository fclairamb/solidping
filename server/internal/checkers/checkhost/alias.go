package checkhost

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhost/config"

// HostConfig is the host check's configuration. It lives in the light
// `config` sub-package; this alias keeps `checkhost.HostConfig` usable.
type HostConfig = checkconfig.HostConfig
