package checksystemd

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksystemd/config"

// SystemdConfig is the systemd check's configuration. It lives in the light
// `config` sub-package; this alias keeps `checksystemd.SystemdConfig` usable.
type SystemdConfig = checkconfig.SystemdConfig
