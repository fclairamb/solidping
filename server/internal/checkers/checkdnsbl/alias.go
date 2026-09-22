package checkdnsbl

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl/config"

// DNSBLConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkdnsbl.DNSBLConfig` call site compiling.
type DNSBLConfig = checkconfig.DNSBLConfig

// Bounds and defaults, defined once with the config and aliased here so this
// package's own call sites keep their short names.
const (
	maxTimeout = checkconfig.MaxTimeout
)

// keyTarget is the `target` config/output map key, defined with the config.
const keyTarget = checkconfig.KeyTarget

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	zoneSpamhaus   = checkconfig.ZoneSpamhaus
	zoneSpamcop    = checkconfig.ZoneSpamcop
	zoneBarracuda  = checkconfig.ZoneBarracuda
	zoneUCEProtect = checkconfig.ZoneUCEProtect
)

// Defined with the config; aliased so this package's call sites keep the short name.
var (
	defaultBlocklists = checkconfig.DefaultBlocklists
)
