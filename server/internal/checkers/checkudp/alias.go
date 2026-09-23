package checkudp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkudp/config"

// UDPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkudp.UDPConfig` call site compiling.
type UDPConfig = checkconfig.UDPConfig
