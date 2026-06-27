package kubernetes

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ClientsetFactory builds a clientset from a resolved rest.Config. Production
// uses the real kubernetes.NewForConfig; tests swap in a factory that returns a
// fake.Clientset so the resolver path can be exercised without a live cluster.
type ClientsetFactory func(cfg *rest.Config) (kubernetes.Interface, error)

// clientsetFactory is the active factory. It defaults to the real client-go
// constructor and is overridable in tests via SetClientsetFactory.
//
//nolint:gochecknoglobals // package-level test seam, mirrors the freebox/checker indirection pattern.
var clientsetFactory ClientsetFactory = defaultClientsetFactory

// defaultClientsetFactory builds a real clientset from cfg.
func defaultClientsetFactory(cfg *rest.Config) (kubernetes.Interface, error) {
	//nolint:wrapcheck // thin pass-through; callers wrap with context.
	return kubernetes.NewForConfig(cfg)
}

// SetClientsetFactory overrides the clientset factory and returns a restore
// function. Intended for tests:
//
//	defer kubernetes.SetClientsetFactory(fakeFactory)()
func SetClientsetFactory(f ClientsetFactory) func() {
	prev := clientsetFactory
	clientsetFactory = f

	return func() { clientsetFactory = prev }
}
