package kubernetes

import "k8s.io/client-go/rest"

// SetInClusterConfig swaps the in-cluster config loader for tests and returns
// a restore function.
func SetInClusterConfig(f func() (*rest.Config, error)) func() {
	prev := inClusterConfig
	inClusterConfig = f

	return func() { inClusterConfig = prev }
}
