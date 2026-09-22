package checkkafka

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkkafka/config"

// KafkaConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkkafka.KafkaConfig` call site compiling.
type KafkaConfig = checkconfig.KafkaConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultTimeout = checkconfig.DefaultTimeout
)
