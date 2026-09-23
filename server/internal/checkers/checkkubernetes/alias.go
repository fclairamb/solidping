package checkkubernetes

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes/config"

// KubernetesConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkkubernetes.KubernetesConfig` call site compiling.
type KubernetesConfig = checkconfig.KubernetesConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	KindDeployment = checkconfig.KindDeployment
	KindReplicaSet = checkconfig.KindReplicaSet
)
