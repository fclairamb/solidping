package checkhealth

import healthconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhealth/config"

// HealthConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps the `checkhealth.HealthConfig` name.
type HealthConfig = healthconfig.HealthConfig

// ComponentOverride is the per-component downgrade of a HealthConfig.
type ComponentOverride = healthconfig.ComponentOverride
