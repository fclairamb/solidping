// Package kubernetes resolves a stored, encrypted "cluster connection"
// Integration to an authenticated Kubernetes clientset. It is the single
// decryption chokepoint for cluster credentials: both the `kubernetes` checker
// (server/internal/checkers/checkkubernetes) and, later, the Kubernetes
// discovery job call ResolveClientset rather than touching settings_private
// themselves.
//
// It mirrors server/internal/integrations/freebox/lanlookup.go — depending only
// on db.Service + credentials.Service keeps this package free of any handlers
// import (avoiding a layering inversion).
package kubernetes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrClusterNotFound is returned when the requested connection does not
	// exist, belongs to another organization, or is not a Kubernetes cluster
	// connection. It is deliberately coarse so callers can map any of those to
	// a single 404/422 without leaking which condition failed.
	ErrClusterNotFound = errors.New("kubernetes: cluster connection not found")

	// ErrEncryptionDisabled is returned when a connection carries an encrypted
	// secret but the credentials service has no master key configured, so the
	// token/kubeconfig cannot be decrypted.
	ErrEncryptionDisabled = errors.New("kubernetes: connection has encrypted credentials but encryption is disabled")

	// ErrMissingCredentials is returned when a non-in-cluster connection has
	// neither a token nor a kubeconfig to authenticate with.
	ErrMissingCredentials = errors.New("kubernetes: connection has no token or kubeconfig")

	// ErrMissingAPIServer is returned when a token-authenticated connection
	// does not specify an API server URL.
	ErrMissingAPIServer = errors.New("kubernetes: connection has no apiServer URL")
)

// ResolveClientset loads the Kubernetes cluster connection by UID, verifies it
// belongs to the org and is a kubernetes-type connection, decrypts its
// token/kubeconfig, builds a *rest.Config, and returns a clientset.
//
// This is the single place cluster secrets are decrypted. Both the checker and
// the discovery engine call it. The returned value is a kubernetes.Interface so
// tests can substitute a fake clientset via the package-level factory.
func ResolveClientset(
	ctx context.Context,
	dbSvc db.Service,
	creds credentials.Service,
	orgUID, clusterUID string,
) (kubernetes.Interface, error) {
	settings, priv, err := resolveClusterConnection(ctx, dbSvc, creds, orgUID, clusterUID)
	if err != nil {
		return nil, err
	}

	restCfg, err := BuildRestConfig(settings, priv)
	if err != nil {
		return nil, err
	}

	return clientsetFactory(restCfg)
}

// ResolveClientsetByUID resolves a cluster connection to a clientset by UID
// alone, deriving the owning org from the connection row. This is the entry
// point the `kubernetes` checker uses: a check can only reference a connection
// in its own org (enforced at check creation), so no separate org argument is
// needed — exactly how the freebox_line checker resolver scopes by connection
// UID. Verifies the connection is a kubernetes-type row.
func ResolveClientsetByUID(
	ctx context.Context,
	dbSvc db.Service,
	creds credentials.Service,
	clusterUID string,
) (kubernetes.Interface, error) {
	conn, err := dbSvc.GetChannel(ctx, clusterUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrClusterNotFound
		}

		return nil, fmt.Errorf("get cluster connection: %w", err)
	}

	if conn.Type != models.ConnectionTypeKubernetes {
		return nil, ErrClusterNotFound
	}

	return ResolveClientset(ctx, dbSvc, creds, conn.OrganizationUID, clusterUID)
}

// resolveClusterConnection loads the connection, verifies org + type, parses
// the public settings, and decrypts the secret half. Mirrors the freebox
// resolveGrantedFreeboxChannel helper but with no handler dependencies.
func resolveClusterConnection(
	ctx context.Context,
	dbSvc db.Service,
	creds credentials.Service,
	orgUID, clusterUID string,
) (*models.KubernetesSettings, *models.KubernetesPrivateSettings, error) {
	conn, err := dbSvc.GetChannel(ctx, clusterUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrClusterNotFound
		}

		return nil, nil, fmt.Errorf("get cluster connection: %w", err)
	}

	if conn.OrganizationUID != orgUID || conn.Type != models.ConnectionTypeKubernetes {
		return nil, nil, ErrClusterNotFound
	}

	settings, err := models.KubernetesSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, nil, fmt.Errorf("parse kubernetes settings: %w", err)
	}

	priv, err := resolvePrivateSettings(ctx, creds, conn)
	if err != nil {
		return nil, nil, err
	}

	return settings, priv, nil
}

// resolvePrivateSettings decrypts the token/kubeconfig stored on a cluster
// connection. Honors both the encrypted-private path and the plaintext fallback
// (credentials disabled), matching the integrations service's
// applySettingsEncryption fallback behavior.
func resolvePrivateSettings(
	ctx context.Context, creds credentials.Service, conn *models.Integration,
) (*models.KubernetesPrivateSettings, error) {
	if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
		if !creds.Enabled() {
			return nil, ErrEncryptionDisabled
		}

		decrypted, err := creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
		if err != nil {
			return nil, fmt.Errorf("decrypt kubernetes credentials: %w", err)
		}

		return models.KubernetesPrivateSettingsFromMap(decrypted), nil
	}

	// Plaintext fallback (credentials disabled): the same keys live on the
	// public Settings map (matching the integrations service's
	// applySettingsEncryption fallback behavior).
	return models.KubernetesPrivateSettingsFromMap(conn.Settings), nil
}

// BuildRestConfig turns a cluster connection's public + secret settings into a
// *rest.Config. Three mutually exclusive auth shapes, in precedence order:
//
//  1. in-cluster (rest.InClusterConfig) when settings.InCluster is set;
//  2. a pasted kubeconfig when priv.Kubeconfig is set;
//  3. apiServer URL + bearer token (+ CA cert / insecureSkipTLSVerify).
func BuildRestConfig(
	settings *models.KubernetesSettings, priv *models.KubernetesPrivateSettings,
) (*rest.Config, error) {
	if settings == nil {
		settings = &models.KubernetesSettings{}
	}

	if priv == nil {
		priv = &models.KubernetesPrivateSettings{}
	}

	if settings.InCluster {
		cfg, err := inClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("load in-cluster config: %w", err)
		}

		return cfg, nil
	}

	if priv.Kubeconfig != "" {
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(priv.Kubeconfig))
		if err != nil {
			return nil, fmt.Errorf("parse kubeconfig: %w", err)
		}

		return cfg, nil
	}

	return buildTokenConfig(settings, priv)
}

// buildTokenConfig builds a rest.Config from an apiServer URL + bearer token.
func buildTokenConfig(
	settings *models.KubernetesSettings, priv *models.KubernetesPrivateSettings,
) (*rest.Config, error) {
	if settings.APIServer == "" {
		return nil, ErrMissingAPIServer
	}

	if priv.Token == "" {
		return nil, ErrMissingCredentials
	}

	cfg := &rest.Config{
		Host:        settings.APIServer,
		BearerToken: priv.Token,
	}

	cfg.Insecure = settings.InsecureSkipTLSVerify

	if !settings.InsecureSkipTLSVerify && settings.CACert != "" {
		cfg.CAData = []byte(settings.CACert)
	}

	return cfg, nil
}

// inClusterConfig is indirected so tests can exercise the InCluster branch of
// BuildRestConfig without running inside a real pod.
//
//nolint:gochecknoglobals // test seam, mirrors clientsetFactory below.
var inClusterConfig = rest.InClusterConfig
