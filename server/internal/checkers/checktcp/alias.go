package checktcp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checktcp/config"

// TCPConfig is the TCP check's configuration. It lives in the light
// `config` sub-package — this alias keeps every existing call site
// (`checktcp.TCPConfig`) compiling unchanged.
type TCPConfig = checkconfig.TCPConfig
