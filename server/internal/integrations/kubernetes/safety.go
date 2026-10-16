package kubernetes

import (
	"errors"
	"fmt"
	"sync/atomic"

	"k8s.io/client-go/tools/clientcmd"
)

var (
	// ErrUnsafeKubeconfig is the parent of every kubeconfig rejection below.
	// The handler maps it to a VALIDATION_ERROR; the messages never echo the
	// kubeconfig content (it is a secret).
	ErrUnsafeKubeconfig = errors.New("kubernetes: kubeconfig not allowed")

	// ErrKubeconfigExec is returned when a kubeconfig user carries an `exec`
	// credential plugin: client-go would run its command on this process.
	ErrKubeconfigExec = fmt.Errorf(
		"%w: exec credential plugins are not supported, use a token or client certificate",
		ErrUnsafeKubeconfig)

	// ErrKubeconfigAuthProvider is returned when a kubeconfig user carries an
	// `auth-provider` block (gcp/azure/oidc plugins, some of which run commands).
	ErrKubeconfigAuthProvider = fmt.Errorf(
		"%w: auth-provider plugins are not supported, use a token or client certificate",
		ErrUnsafeKubeconfig)

	// ErrKubeconfigFileReference is returned when a kubeconfig points at a file
	// on the server (tokenFile, client-certificate, client-key,
	// certificate-authority): client-go would read that file from SolidPing's
	// own disk, e.g. the pod's mounted service-account token.
	ErrKubeconfigFileReference = fmt.Errorf(
		"%w: file references are not supported, embed the data "+
			"(token, client-certificate-data, client-key-data, certificate-authority-data)",
		ErrUnsafeKubeconfig)

	// ErrInClusterDisabled is returned when a connection asks for in-cluster
	// mode and the deployment has not opted in with SP_K8S_ALLOW_IN_CLUSTER.
	ErrInClusterDisabled = errors.New(
		"kubernetes: in-cluster mode is disabled on this deployment (SP_K8S_ALLOW_IN_CLUSTER)")
)

// allowInCluster is the process-wide operator opt-in for in-cluster mode,
// installed once at startup from config (SP_K8S_ALLOW_IN_CLUSTER). It is a
// package-level switch rather than a parameter because the use-time chokepoint
// (BuildRestConfig) is reached from the API, the checker resolver and the
// discovery jobs alike, none of which carry the config.
//
//nolint:gochecknoglobals // process-wide deployment policy, mirrors inClusterConfig.
var allowInCluster atomic.Bool

// SetAllowInCluster installs the deployment's in-cluster opt-in and returns a
// function restoring the previous value (handy in tests).
func SetAllowInCluster(allow bool) func() {
	prev := allowInCluster.Swap(allow)

	return func() { allowInCluster.Store(prev) }
}

// InClusterAllowed reports whether this deployment permits in-cluster mode.
func InClusterAllowed() bool {
	return allowInCluster.Load()
}

// ValidateKubeconfigSafe parses a pasted kubeconfig without building a client
// and rejects anything that would make client-go execute a command or read a
// local file: an `exec` plugin, an `auth-provider`, or a file-path credential
// on any user or cluster entry (not only the current context's, so switching
// the current context later cannot activate a dormant one).
//
// Runs at save time (integrations handler) and again in BuildRestConfig, so a
// row written before this check existed is refused when it is used.
func ValidateKubeconfigSafe(raw string) error {
	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil {
		return fmt.Errorf("parse kubeconfig: %w", err)
	}

	for _, user := range cfg.AuthInfos {
		if user == nil {
			continue
		}

		if user.Exec != nil {
			return ErrKubeconfigExec
		}

		if user.AuthProvider != nil {
			return ErrKubeconfigAuthProvider
		}

		if user.TokenFile != "" || user.ClientCertificate != "" || user.ClientKey != "" {
			return ErrKubeconfigFileReference
		}
	}

	for _, cluster := range cfg.Clusters {
		if cluster != nil && cluster.CertificateAuthority != "" {
			return ErrKubeconfigFileReference
		}
	}

	return nil
}
