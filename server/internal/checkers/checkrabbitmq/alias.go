package checkrabbitmq

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq/config"

// RabbitMQConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkrabbitmq.RabbitMQConfig` call site compiling.
type RabbitMQConfig = checkconfig.RabbitMQConfig

// Threshold is the parsed memory/disk threshold the config produces; aliased so
// this package keeps the short name.
type threshold = checkconfig.Threshold

// Defined with the config; aliased so this package keeps the short name.
const (
	ModeAMQP = checkconfig.ModeAMQP
	ModeManagement = checkconfig.ModeManagement
	defaultManagementPort = checkconfig.DefaultManagementPort
	defaultMode = checkconfig.DefaultMode
	defaultPort = checkconfig.DefaultPort
	defaultTimeout = checkconfig.DefaultTimeout
)

// Defined with the config; aliased so this package keeps the short name.
const (
	keyDiskFreeCritical = checkconfig.KeyDiskFreeCritical
	keyDiskFreeWarning = checkconfig.KeyDiskFreeWarning
	keyMemoryUsedCritical = checkconfig.KeyMemoryUsedCritical
	keyMemoryUsedWarning = checkconfig.KeyMemoryUsedWarning
)
