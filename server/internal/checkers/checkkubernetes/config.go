// Package checkkubernetes provides a Kubernetes workload replica-health check.
// The check references a stored "cluster connection" Integration of type
// `kubernetes` by UID; at execution time the connection's encrypted
// token/kubeconfig is resolved to a clientset via the package-level
// ClientsetResolver indirection (wired from the API server at startup), exactly
// like the freebox_line checker resolves its app_token.
//
// The check reports replica readiness for a single workload (Deployment or
// ReplicaSet) — the structural analog of how the docker checker mirrors a
// container's HEALTHCHECK. No secret ever lives in the check config: only the
// cluster UID plus (namespace, kind, name).
package checkkubernetes

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Workload kinds supported in v1.
const (
	KindDeployment = "Deployment"
	KindReplicaSet = "ReplicaSet"
)

const (
	defaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
)

// Config map keys — kept as constants so FromMap / GetConfig can't drift.
const (
	keyClusterUID = "clusterUid"
	keyNamespace  = "namespace"
	keyKind       = "kind"
	keyName       = "name"
	keyTimeout    = "timeout"
)

// KubernetesConfig holds the configuration for a workload replica-health check.
// It is "connection-backed": ClusterUID refers to an integrations row of type
// `kubernetes` that owns the encrypted credentials.
type KubernetesConfig struct {
	// ClusterUID references an integrations row with type "kubernetes".
	ClusterUID string `json:"clusterUid"`
	// Namespace is the workload's namespace. Required.
	Namespace string `json:"namespace"`
	// Kind is the workload kind: "Deployment" or "ReplicaSet". Required.
	Kind string `json:"kind"`
	// Name is the workload name. Required.
	Name string `json:"name"`
	// Timeout caps the API call(s); default 10s, max 60s.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map (typically the JSONB `config`
// column shape).
func (c *KubernetesConfig) FromMap(configMap map[string]any) error {
	if v, ok := configMap[keyClusterUID].(string); ok {
		c.ClusterUID = v
	} else if configMap[keyClusterUID] != nil {
		return checkerdef.NewConfigError(keyClusterUID, "must be a string")
	}

	if v, ok := configMap[keyNamespace].(string); ok {
		c.Namespace = v
	} else if configMap[keyNamespace] != nil {
		return checkerdef.NewConfigError(keyNamespace, "must be a string")
	}

	if v, ok := configMap[keyKind].(string); ok {
		c.Kind = v
	} else if configMap[keyKind] != nil {
		return checkerdef.NewConfigError(keyKind, "must be a string")
	}

	if v, ok := configMap[keyName].(string); ok {
		c.Name = v
	} else if configMap[keyName] != nil {
		return checkerdef.NewConfigError(keyName, "must be a string")
	}

	if v, ok := configMap[keyTimeout].(string); ok {
		duration, err := time.ParseDuration(v)
		if err != nil {
			return checkerdef.NewConfigError(keyTimeout, "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap[keyTimeout] != nil {
		return checkerdef.NewConfigError(keyTimeout, "must be a string")
	}

	return nil
}

// GetConfig serializes the config back to a map. The zero-valued timeout is
// omitted to keep the persisted JSONB tidy.
func (c *KubernetesConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		keyClusterUID: c.ClusterUID,
		keyNamespace:  c.Namespace,
		keyKind:       c.Kind,
		keyName:       c.Name,
	}

	if c.Timeout != 0 {
		cfg[keyTimeout] = c.Timeout.String()
	}

	return cfg
}

// Validate ensures the configuration is well-formed. Performed without any
// network call — wire validation happens during Execute.
func (c *KubernetesConfig) Validate() error {
	if c.ClusterUID == "" {
		return checkerdef.NewConfigError(keyClusterUID, "is required")
	}

	if c.Namespace == "" {
		return checkerdef.NewConfigError(keyNamespace, "is required")
	}

	if c.Name == "" {
		return checkerdef.NewConfigError(keyName, "is required")
	}

	switch c.Kind {
	case KindDeployment, KindReplicaSet:
	default:
		return checkerdef.NewConfigErrorf(
			keyKind,
			"must be %q or %q, got %q", KindDeployment, KindReplicaSet, c.Kind,
		)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			keyTimeout, "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	return nil
}

// SecretFields implements credentials.SecretFielder. The check config itself
// carries no secrets — the cluster token/kubeconfig lives on the referenced
// Integration. Returning an empty slice (rather than nil) makes the no-secret
// intent explicit.
func (c *KubernetesConfig) SecretFields() []string {
	return []string{}
}

// resolveTimeout returns the per-execution timeout, defaulting to 10s.
func (c *KubernetesConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

// Ensure KubernetesConfig satisfies the Config interface at compile time.
var _ checkerdef.Config = (*KubernetesConfig)(nil)
