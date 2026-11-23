package checkunixsocket

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkunixsocket/config"

// UnixSocketConfig is the unix_socket check's configuration. It lives in the
// light `config` sub-package; this alias keeps `checkunixsocket.UnixSocketConfig`
// usable.
type UnixSocketConfig = checkconfig.UnixSocketConfig
