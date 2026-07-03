package kubernetes_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
)

// fixture wires an in-memory db + disabled-credentials service so the resolver
// can be exercised without Postgres or a master key (token lives plaintext on
// Settings, matching the V1 fallback). Mirrors freebox/lanlookup_test.go.
type fixture struct {
	dbSvc db.Service
	creds credentials.Service
	org   *models.Organization
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil) // disabled mode → plaintext fallback
	r.NoError(err)

	org := models.NewOrganization("k8s-test", "K8s Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &fixture{dbSvc: dbSvc, creds: creds, org: org}
}

// newClusterConnection creates a kubernetes connection with the given public
// settings and a plaintext token on Settings (credentials-disabled fallback).
func (f *fixture) newClusterConnection(
	t *testing.T, settings *models.KubernetesSettings, token string,
) *models.Integration {
	t.Helper()

	r := require.New(t)

	m, err := settings.ToJSONMap()
	r.NoError(err)

	if token != "" {
		m["token"] = token
	}

	conn := models.NewIntegration(f.org.UID, models.ConnectionTypeKubernetes, "Cluster")
	conn.Settings = m
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	return conn
}

// withFakeFactory swaps the clientset factory for one returning a fake
// clientset, so the resolver path runs without a live cluster.
func withFakeFactory(t *testing.T) {
	t.Helper()

	restore := integrationk8s.SetClientsetFactory(func(*rest.Config) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	})
	t.Cleanup(restore)
}

//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestResolveClientsetTokenAuth(t *testing.T) {
	withFakeFactory(t)

	r := require.New(t)
	f := newFixture(t)
	conn := f.newClusterConnection(t, &models.KubernetesSettings{
		APIServer: "https://10.0.0.1:6443",
	}, "sa-token")

	cs, err := integrationk8s.ResolveClientset(t.Context(), f.dbSvc, f.creds, f.org.UID, conn.UID)
	r.NoError(err)
	r.NotNil(cs)
}

//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestResolveClientsetByUID(t *testing.T) {
	withFakeFactory(t)

	r := require.New(t)
	f := newFixture(t)
	conn := f.newClusterConnection(t, &models.KubernetesSettings{
		APIServer: "https://10.0.0.1:6443",
	}, "sa-token")

	cs, err := integrationk8s.ResolveClientsetByUID(t.Context(), f.dbSvc, f.creds, conn.UID)
	r.NoError(err)
	r.NotNil(cs)
}

func TestResolveClientsetUnknownUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	_, err := integrationk8s.ResolveClientset(
		t.Context(), f.dbSvc, f.creds, f.org.UID, "00000000-0000-0000-0000-000000000000",
	)
	r.ErrorIs(err, integrationk8s.ErrClusterNotFound)
}

func TestResolveClientsetWrongOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)
	conn := f.newClusterConnection(t, &models.KubernetesSettings{APIServer: "https://x:6443"}, "tok")

	_, err := integrationk8s.ResolveClientset(t.Context(), f.dbSvc, f.creds, "other-org", conn.UID)
	r.ErrorIs(err, integrationk8s.ErrClusterNotFound)
}

func TestResolveClientsetWrongType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	other := models.NewIntegration(f.org.UID, models.ConnectionTypeDiscord, "Discord")
	r.NoError(f.dbSvc.CreateChannel(t.Context(), other))

	_, err := integrationk8s.ResolveClientset(t.Context(), f.dbSvc, f.creds, f.org.UID, other.UID)
	r.ErrorIs(err, integrationk8s.ErrClusterNotFound)

	_, err = integrationk8s.ResolveClientsetByUID(t.Context(), f.dbSvc, f.creds, other.UID)
	r.ErrorIs(err, integrationk8s.ErrClusterNotFound)
}

func TestBuildRestConfigToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{
			APIServer: "https://api.example:6443",
			CACert:    "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		},
		&models.KubernetesPrivateSettings{Token: "abc"},
	)
	r.NoError(err)
	r.Equal("https://api.example:6443", cfg.Host)
	r.Equal("abc", cfg.BearerToken)
	r.False(cfg.Insecure)
	r.NotEmpty(cfg.CAData)
}

func TestBuildRestConfigInsecure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{
			APIServer:             "https://api.example:6443",
			InsecureSkipTLSVerify: true,
			CACert:                "ignored-when-insecure",
		},
		&models.KubernetesPrivateSettings{Token: "abc"},
	)
	r.NoError(err)
	r.True(cfg.Insecure)
	r.Empty(cfg.CAData, "CA must be ignored when insecure")
}

func TestBuildRestConfigMissingAPIServer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{},
		&models.KubernetesPrivateSettings{Token: "abc"},
	)
	r.ErrorIs(err, integrationk8s.ErrMissingAPIServer)
}

func TestBuildRestConfigMissingCredentials(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{APIServer: "https://api:6443"},
		&models.KubernetesPrivateSettings{},
	)
	r.ErrorIs(err, integrationk8s.ErrMissingCredentials)
}

func TestBuildRestConfigKubeconfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	kubeconfig := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://kube.example:6443
    insecure-skip-tls-verify: true
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user:
    token: in-kubeconfig
`

	cfg, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{},
		&models.KubernetesPrivateSettings{Kubeconfig: kubeconfig},
	)
	r.NoError(err)
	r.Equal("https://kube.example:6443", cfg.Host)
	r.Equal("in-kubeconfig", cfg.BearerToken)
}

func TestBuildRestConfigKubeconfigInvalid(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := integrationk8s.BuildRestConfig(
		&models.KubernetesSettings{},
		&models.KubernetesPrivateSettings{Kubeconfig: "::not yaml::"},
	)
	r.Error(err)
}

func TestBuildRestConfigNilArgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Nil settings + nil priv → no in-cluster, no kubeconfig, no apiServer →
	// missing apiServer error (not a panic).
	_, err := integrationk8s.BuildRestConfig(nil, nil)
	r.ErrorIs(err, integrationk8s.ErrMissingAPIServer)
}
