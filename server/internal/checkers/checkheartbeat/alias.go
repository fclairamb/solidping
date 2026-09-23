package checkheartbeat

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat/config"

// HeartbeatConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkheartbeat.HeartbeatConfig` call site compiling.
type HeartbeatConfig = checkconfig.HeartbeatConfig

// GenerateToken mints a fresh heartbeat token. It forwards to the config
// sub-package, which owns the generation, so the checks service's
// rotate-token endpoint keeps calling `checkheartbeat.GenerateToken`.
func GenerateToken() (string, error) { return checkconfig.GenerateToken() }

// RequireHMACFromConfig reads the `require_hmac` flag off a raw config map.
func RequireHMACFromConfig(configMap map[string]any) (bool, error) {
	return checkconfig.RequireHMACFromConfig(configMap)
}

// ConfigKeyRequireHMAC is the `require_hmac` config key.
const ConfigKeyRequireHMAC = checkconfig.ConfigKeyRequireHMAC
