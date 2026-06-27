package kubernetes

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// ServerVersionResult is the outcome of a successful connection probe — the
// cluster's reported server version (e.g. "v1.31.4"). Surfaced to the operator
// at registration time so a good credential is visibly confirmed.
type ServerVersionResult struct {
	GitVersion string
}

// ValidateConnection performs a read-only probe against the cluster to confirm
// the credentials work and basic API access is granted. It calls
// /version (the Discovery client's ServerVersion), which requires no RBAC and
// surfaces credential/transport failures immediately. RBAC gaps on the
// monitored resources surface later at check time; this probe proves the
// connection itself is wired up.
//
// Returns the server version on success, or a wrapped transport/auth error.
func ValidateConnection(ctx context.Context, clientset kubernetes.Interface) (*ServerVersionResult, error) {
	if clientset == nil {
		return nil, fmt.Errorf("%w: nil clientset", ErrMissingCredentials)
	}

	// The Discovery client's ServerVersion call does not take a context in
	// client-go; the transport-level timeout from the rest.Config (and the
	// caller's overall deadline) bounds it. We touch ctx so cancellation before
	// the call is honored and the signature stays context-first.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("kubernetes: validate connection: %w", err)
	}

	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("kubernetes: validate connection: %w", err)
	}

	return &ServerVersionResult{GitVersion: version.GitVersion}, nil
}
