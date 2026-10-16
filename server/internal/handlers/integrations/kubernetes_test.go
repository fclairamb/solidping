package integrations_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
)

// k8sTestEnv bundles a ready integrations service + org for the cluster tests.
type k8sTestEnv struct {
	svc *integrations.Service
	org *models.Organization
}

func newK8sTestEnv(t *testing.T) *k8sTestEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("k8s-conn-test", "K8s Conn Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &k8sTestEnv{
		svc: integrations.NewService(dbSvc, creds, nil, nil),
		org: org,
	}
}

// TestCreateKubernetesConnectionEncryptsToken verifies a kubernetes cluster
// connection is accepted, its token is stored encrypted (stripped from the
// response Settings and surfaced only as a private-key name), and the public
// apiServer setting round-trips.
func TestCreateKubernetesConnectionEncryptsToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newK8sTestEnv(t)

	resp, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type: "kubernetes",
		Name: "prod-cluster",
		Settings: map[string]any{
			"apiServer": "https://10.0.0.1:6443",
			"token":     "super-secret-token",
		},
	})
	r.NoError(err)
	r.Equal("kubernetes", resp.Type)
	r.Equal("https://10.0.0.1:6443", resp.Settings["apiServer"])
	r.NotContains(resp.Settings, "token", "token must never be returned in plaintext")
	r.Contains(resp.SettingsPrivateKeys, "token")
}

// TestTestKubernetesConnectionSuccess routes a kubernetes connection through the
// TestIntegration path and confirms a healthy cluster probe reports success
// with the server version, rather than being rejected as non-notifiable.
//
//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestTestKubernetesConnectionSuccess(t *testing.T) {
	restore := integrationk8s.SetClientsetFactory(func(*rest.Config) (kubernetes.Interface, error) {
		cs := fake.NewSimpleClientset()
		disc, _ := cs.Discovery().(*fakediscovery.FakeDiscovery)
		disc.FakedServerVersion = &version.Info{GitVersion: "v1.30.0"}

		return cs, nil
	})
	defer restore()

	r := require.New(t)
	env := newK8sTestEnv(t)

	resp, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "prod",
		Settings: map[string]any{"apiServer": "https://10.0.0.1:6443", "token": "tok"},
	})
	r.NoError(err)

	result, err := env.svc.TestIntegration(t.Context(), env.org.Slug, resp.UID)
	r.NoError(err)
	r.True(result.Success)
	r.Contains(result.Detail, "v1.30.0")
}

// TestTestKubernetesConnectionFailure confirms an auth/RBAC failure on the probe
// is surfaced as Success=false with the error (not an error return).
//
//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestTestKubernetesConnectionFailure(t *testing.T) {
	restore := integrationk8s.SetClientsetFactory(func(*rest.Config) (kubernetes.Interface, error) {
		cs := fake.NewSimpleClientset()
		cs.PrependReactor("get", "version", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewUnauthorized("bad token")
		})

		return cs, nil
	})
	defer restore()

	r := require.New(t)
	env := newK8sTestEnv(t)

	resp, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "prod",
		Settings: map[string]any{"apiServer": "https://10.0.0.1:6443", "token": "tok"},
	})
	r.NoError(err)

	result, err := env.svc.TestIntegration(t.Context(), env.org.Slug, resp.UID)
	r.NoError(err)
	r.False(result.Success)
	r.NotEmpty(result.Error)
}

// unsafeK8sKubeconfig renders a kubeconfig whose user carries the given
// (indented) credential block.
func unsafeK8sKubeconfig(userBody string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user:
` + userBody
}

func execK8sKubeconfig(marker string) string {
	return unsafeK8sKubeconfig(`    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: touch
      args: ["` + marker + `"]
      interactiveMode: Never
`)
}

const authProviderK8sUser = `    auth-provider:
      name: oidc
      config:
        idp-issuer-url: https://idp.acme.com
`

// countK8sConnections lists the org's integrations so a test can assert a
// rejected create wrote no row.
func countK8sConnections(t *testing.T, env *k8sTestEnv) int {
	t.Helper()

	list, err := env.svc.ListIntegrations(t.Context(), env.org.Slug, nil)
	require.NoError(t, err)

	return len(list.Data)
}

// TestCreateKubernetesConnectionRejectsUnsafeKubeconfig: exec / auth-provider
// kubeconfigs are refused with ErrInvalidSettings (VALIDATION_ERROR) before
// any row is written; a clean kubeconfig is the positive control.
func TestCreateKubernetesConnectionRejectsUnsafeKubeconfig(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		kubeconfig string
		wantErr    error
	}{
		"exec":          {execK8sKubeconfig("/tmp/never"), integrationk8s.ErrKubeconfigExec},
		"auth-provider": {unsafeK8sKubeconfig(authProviderK8sUser), integrationk8s.ErrKubeconfigAuthProvider},
		"clean":         {unsafeK8sKubeconfig("    token: tok\n"), nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newK8sTestEnv(t)

			resp, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
				Type:     "kubernetes",
				Name:     "cluster",
				Settings: map[string]any{"kubeconfig": tc.kubeconfig},
			})

			if tc.wantErr == nil {
				r.NoError(err)
				r.Contains(resp.SettingsPrivateKeys, "kubeconfig")
				r.Equal(1, countK8sConnections(t, env))

				return
			}

			r.ErrorIs(err, integrations.ErrInvalidSettings)
			r.ErrorIs(err, tc.wantErr)
			r.Nil(resp)
			r.Zero(countK8sConnections(t, env), "a rejected create must not write a row")
		})
	}
}

// TestUpdateKubernetesConnectionRejectsUnsafeKubeconfig: a clean connection
// cannot be PATCHed to an exec or auth-provider kubeconfig; a clean PATCH is
// the positive control.
func TestUpdateKubernetesConnectionRejectsUnsafeKubeconfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newK8sTestEnv(t)

	created, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "cluster",
		Settings: map[string]any{"apiServer": "https://10.0.0.1:6443", "token": "tok"},
	})
	r.NoError(err)

	for name, raw := range map[string]string{
		"exec":          execK8sKubeconfig("/tmp/never"),
		"auth-provider": unsafeK8sKubeconfig(authProviderK8sUser),
	} {
		_, err = env.svc.UpdateIntegration(t.Context(), env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: map[string]any{"kubeconfig": raw},
		})
		r.ErrorIs(err, integrations.ErrInvalidSettings, name)
		r.ErrorIs(err, integrationk8s.ErrUnsafeKubeconfig, name)
	}

	name := "renamed"
	cleanPatch := integrations.UpdateIntegrationRequest{
		Name:     &name,
		Settings: map[string]any{"kubeconfig": unsafeK8sKubeconfig("    token: tok2\n")},
	}
	updated, err := env.svc.UpdateIntegration(t.Context(), env.org.Slug, created.UID, cleanPatch)
	r.NoError(err)
	r.Equal("renamed", updated.Name)
	r.Contains(updated.SettingsPrivateKeys, "kubeconfig")
}

// TestKubernetesConnectionInClusterGate: inCluster=true is refused at create
// and at PATCH unless the deployment opted in; with the opt-in it is saved.
//
//nolint:paralleltest // flips the process-wide in-cluster opt-in.
func TestKubernetesConnectionInClusterGate(t *testing.T) {
	r := require.New(t)
	env := newK8sTestEnv(t)

	restore := integrationk8s.SetAllowInCluster(false)
	t.Cleanup(restore)

	_, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "in-cluster",
		Settings: map[string]any{"inCluster": true},
	})
	r.ErrorIs(err, integrations.ErrInvalidSettings)
	r.ErrorIs(err, integrationk8s.ErrInClusterDisabled)
	r.Zero(countK8sConnections(t, env))

	created, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "remote",
		Settings: map[string]any{"apiServer": "https://10.0.0.1:6443", "token": "tok"},
	})
	r.NoError(err)

	_, err = env.svc.UpdateIntegration(t.Context(), env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{"inCluster": true},
	})
	r.ErrorIs(err, integrationk8s.ErrInClusterDisabled)

	// Positive control: the operator opted in.
	integrationk8s.SetAllowInCluster(true)

	resp, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "kubernetes",
		Name:     "in-cluster",
		Settings: map[string]any{"inCluster": true},
	})
	r.NoError(err)
	r.Equal(true, resp.Settings["inCluster"])
}

// TestTestKubernetesConnectionRejectsStoredExecKubeconfig: POST /test on a row
// stored before the save-time check (written straight to the DB) reports the
// coded rejection and never runs the plugin. The real clientset factory is
// used, so without the guard the probe would exec (see
// TestExecKubeconfigFixtureFires in the kubernetes package).
func TestTestKubernetesConnectionRejectsStoredExecKubeconfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil) // plaintext fallback
	r.NoError(err)

	org := models.NewOrganization("k8s-exec-test", "K8s Exec Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	marker := filepath.Join(t.TempDir(), "pwned")
	conn := models.NewIntegration(org.UID, models.ConnectionTypeKubernetes, "legacy")
	conn.Settings = models.JSONMap{"kubeconfig": execK8sKubeconfig(marker)}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	svc := integrations.NewService(dbSvc, creds, nil, nil)

	result, err := svc.TestIntegration(ctx, org.Slug, conn.UID)
	r.NoError(err)
	r.False(result.Success)
	r.Equal(integrationk8s.ErrKubeconfigExec.Error(), result.Error)

	_, statErr := os.Stat(marker)
	r.ErrorIs(statErr, os.ErrNotExist, "the exec plugin must never run")
}
