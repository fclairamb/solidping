package integrations_test

import (
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
