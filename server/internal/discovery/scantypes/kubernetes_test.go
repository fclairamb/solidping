package scantypes_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/egress"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// k8sFixture wires an in-memory db + disabled-credentials service + a kubernetes
// cluster connection (plaintext token), and swaps the clientset factory for one
// returning a fake clientset so BuildJob's probe runs without a live cluster.
func k8sFixture(t *testing.T) (scantypes.Deps, string, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil) // disabled → plaintext fallback
	r.NoError(err)

	org := models.NewOrganization("k8s-st", "K8s Scantypes Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	clusterUID := newClusterConnection(t, dbSvc, org.UID)

	return scantypes.Deps{DBService: dbSvc, Creds: creds}, org.UID, clusterUID
}

// newClusterConnection creates a kubernetes connection with a plaintext token.
func newClusterConnection(t *testing.T, dbSvc db.Service, orgUID string) string {
	t.Helper()

	r := require.New(t)

	settings := &models.KubernetesSettings{APIServer: "https://10.0.0.1:6443"}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["token"] = "sa-token"

	conn := models.NewIntegration(orgUID, models.ConnectionTypeKubernetes, "Cluster")
	conn.Settings = m
	r.NoError(dbSvc.CreateChannel(t.Context(), conn))

	return conn.UID
}

// withFakeClientset swaps the integrations clientset factory for one returning a
// fake clientset, so the probe path runs without a live cluster.
func withFakeClientset(t *testing.T) {
	t.Helper()

	restore := integrationk8s.SetClientsetFactory(func(*rest.Config) (kubernetes.Interface, error) {
		return fake.NewSimpleClientset(), nil
	})
	t.Cleanup(restore)
}

//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestKubernetesBuildJobValid(t *testing.T) {
	withFakeClientset(t)

	r := require.New(t)
	deps, orgUID, clusterUID := k8sFixture(t)

	def := &scantypes.KubernetesDefinition{}
	r.Equal("kubernetes", def.Type())
	r.Equal(models.DiscoverySourceKubernetes, def.Source())

	params := json.RawMessage(`{"clusterUid":"` + clusterUID + `","namespaces":["prod"],"timeout":"15s"}`)
	jobType, cfg, err := def.BuildJob(t.Context(), deps, orgUID, "", params)
	r.NoError(err)
	r.Equal(string(jobdef.JobTypeKubernetesDiscovery), jobType)
	r.Contains(string(cfg), clusterUID)
	r.Contains(string(cfg), "prod")

	var got struct {
		ClusterUID string   `json:"clusterUid"`
		Namespaces []string `json:"namespaces"`
		Timeout    string   `json:"timeout"`
	}
	r.NoError(json.Unmarshal(cfg, &got))
	r.Equal(clusterUID, got.ClusterUID)
	r.Equal([]string{"prod"}, got.Namespaces)
	r.Equal("15s", got.Timeout)
}

//nolint:paralleltest // swaps the package-level clientset factory; must run serially.
func TestKubernetesBuildJobUnknownCluster(t *testing.T) {
	withFakeClientset(t)

	r := require.New(t)
	deps, orgUID, _ := k8sFixture(t)

	def := &scantypes.KubernetesDefinition{}
	params := json.RawMessage(`{"clusterUid":"00000000-0000-0000-0000-000000000000"}`)
	_, _, err := def.BuildJob(t.Context(), deps, orgUID, "", params)
	r.Error(err)
	r.Equal(scantypes.CodeKubernetesClusterNotFound, codeOf(err))
}

func TestKubernetesBuildJobMissingParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	def := &scantypes.KubernetesDefinition{}
	_, _, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", "", json.RawMessage(`{}`))
	r.Error(err)
	r.Equal(scantypes.CodeInvalidParameters, codeOf(err))
}

func TestKubernetesBuildJobMalformedJSON(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	def := &scantypes.KubernetesDefinition{}
	_, _, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", "", json.RawMessage(`{nope`))
	r.Error(err)
	r.Equal(scantypes.CodeInvalidParameters, codeOf(err))
}

func TestKubernetesBuildJobNoDBService(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	def := &scantypes.KubernetesDefinition{}
	params := json.RawMessage(`{"clusterUid":"some-uid"}`)
	_, _, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", "", params)
	r.Error(err)
	r.Equal(scantypes.CodeInvalidParameters, codeOf(err))
}

// probeCluster (via BuildJob) dials the API server through the egress guard of
// the deps: under an enforcing guard a loopback API server is refused with the
// egress denial and receives no request; a permissive guard is the control and
// reaches /version. Real clientset (no factory swap), token mode.
//
//nolint:paralleltest // other tests swap the package-level clientset factory; this one needs the real one.
func TestKubernetesBuildJobEgressGuard(t *testing.T) {
	var requests atomic.Int64

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"31","gitVersion":"v1.31.0"}`))
	}))
	t.Cleanup(apiServer.Close)

	r := require.New(t)
	deps, orgUID, _ := k8sFixture(t)

	settings := &models.KubernetesSettings{APIServer: apiServer.URL}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["token"] = "sa-token"

	conn := models.NewIntegration(orgUID, models.ConnectionTypeKubernetes, "Loopback")
	conn.Settings = m
	r.NoError(deps.DBService.CreateChannel(t.Context(), conn))

	def := &scantypes.KubernetesDefinition{}
	params := json.RawMessage(`{"clusterUid":"` + conn.UID + `"}`)

	deps.EgressGuard = egress.New(false)
	_, _, err = def.BuildJob(t.Context(), deps, orgUID, "", params)
	r.ErrorContains(err, egress.ErrDenied.Error())
	r.NotContains(err.Error(), "connection refused")
	r.Zero(requests.Load(), "the private API server must receive no request")

	deps.EgressGuard = egress.New(true)
	jobType, _, err := def.BuildJob(t.Context(), deps, orgUID, "", params)
	r.NoError(err)
	r.Equal(string(jobdef.JobTypeKubernetesDiscovery), jobType)
	r.Positive(requests.Load())
}
