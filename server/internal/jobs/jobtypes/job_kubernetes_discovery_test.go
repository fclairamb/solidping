package jobtypes_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// errResolveK8s is a static sentinel for the resolver-failure test.
var errResolveK8s = errors.New("cannot resolve cluster")

func ptr32(v int32) *int32 { return &v }

// deployment builds a Deployment fixture for the job tests.
func deployment(uid, name string, replicas, ready int32, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{UID: ktypes.UID(uid), Name: name, Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr32(replicas),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready, AvailableReplicas: ready},
	}
}

// k8sDiscoveryFixture is an in-memory db + org + a kubernetes_discovery job row.
type k8sDiscoveryFixture struct {
	dbSvc  db.Service
	org    *models.Organization
	jobUID string
}

func newK8sDiscoveryFixture(t *testing.T) *k8sDiscoveryFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("k8s-disc", "K8s Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	job := models.NewJob(&org.UID, string(jobdef.JobTypeKubernetesDiscovery))
	r.NoError(dbSvc.CreateJob(ctx, job))

	return &k8sDiscoveryFixture{dbSvc: dbSvc, org: org, jobUID: job.UID}
}

func (f *k8sDiscoveryFixture) jctx() *jobdef.JobContext {
	creds, _ := credentials.NewService(nil, nil)

	return &jobdef.JobContext{
		OrganizationUID: &f.org.UID,
		Job:             &models.Job{UID: f.jobUID},
		DB:              f.dbSvc.DB(),
		DBService:       f.dbSvc,
		Services:        &services.Registry{Credentials: creds},
		Logger:          slog.Default(),
	}
}

func (f *k8sDiscoveryFixture) listChecks(t *testing.T) []*models.DiscoveredCheck {
	t.Helper()

	var checks []*models.DiscoveredCheck
	err := f.dbSvc.DB().NewSelect().
		Model(&checks).
		Where("organization_uid = ?", f.org.UID).
		Where("deleted_at IS NULL").
		Order("group_key ASC", "slug ASC").
		Scan(t.Context())
	require.NoError(t, err)

	return checks
}

// fakeResolver returns a resolver yielding the given clientset (or error).
func fakeResolver(cs k8sclient.Interface, err error) func(
	context.Context, db.Service, credentials.Service, string,
) (k8sclient.Interface, error) {
	return func(context.Context, db.Service, credentials.Service, string) (k8sclient.Interface, error) {
		return cs, err
	}
}

func runK8sJob(t *testing.T, f *k8sDiscoveryFixture, cfg jobtypes.KubernetesDiscoveryConfig) error {
	t.Helper()

	def := &jobtypes.KubernetesDiscoveryJobDefinition{}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	runner, err := def.CreateJobRun(raw)
	require.NoError(t, err)

	return runner.Run(context.Background(), f.jctx())
}

//nolint:paralleltest // swaps the package-level clientset resolver; must run serially.
func TestKubernetesDiscoveryPersistsGroupedChecks(t *testing.T) {
	r := require.New(t)

	cs := fake.NewSimpleClientset(
		deployment("api-uid", "api", 3, 2, map[string]string{"app": "api"}),
		deployment("web-uid", "web", 1, 1, map[string]string{"app": "web"}),
	)
	defer jobtypes.SetKubernetesClientsetResolver(fakeResolver(cs, nil))()

	f := newK8sDiscoveryFixture(t)
	r.NoError(runK8sJob(t, f, jobtypes.KubernetesDiscoveryConfig{ClusterUID: "cluster-1"}))

	checks := f.listChecks(t)
	r.Len(checks, 2, "each ClusterIP-only workload yields one kubernetes check")

	for _, c := range checks {
		r.Equal(models.DiscoverySourceKubernetes, c.Source)
		r.Equal(f.jobUID, c.JobUID)
		r.Equal("kubernetes", c.Type)
	}

	byGroup := map[string]*models.DiscoveredCheck{}
	for _, c := range checks {
		byGroup[c.GroupKey] = c
	}

	api := byGroup["api-uid"]
	r.NotNil(api)
	r.Equal("prod/api", api.GroupLabel)

	var cfg struct {
		ClusterUID string `json:"clusterUid"`
		Namespace  string `json:"namespace"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
	}
	r.NoError(json.Unmarshal(api.Config, &cfg))
	r.Equal("cluster-1", cfg.ClusterUID)
	r.Equal("prod", cfg.Namespace)
	r.Equal("Deployment", cfg.Kind)
	r.Equal("api", cfg.Name)
}

//nolint:paralleltest // swaps the package-level clientset resolver; must run serially.
func TestKubernetesDiscoveryUpsertIsIdempotent(t *testing.T) {
	r := require.New(t)

	cs := fake.NewSimpleClientset(
		deployment("api-uid", "api", 1, 1, map[string]string{"app": "api"}),
	)
	defer jobtypes.SetKubernetesClientsetResolver(fakeResolver(cs, nil))()

	f := newK8sDiscoveryFixture(t)
	cfg := jobtypes.KubernetesDiscoveryConfig{ClusterUID: "cluster-1"}
	r.NoError(runK8sJob(t, f, cfg))
	r.NoError(runK8sJob(t, f, cfg))

	checks := f.listChecks(t)
	r.Len(checks, 1, "re-running must update in place, not duplicate")
}

//nolint:paralleltest // swaps the package-level clientset resolver; must run serially.
func TestKubernetesDiscoveryResolverErrorAborts(t *testing.T) {
	r := require.New(t)

	defer jobtypes.SetKubernetesClientsetResolver(fakeResolver(nil, errResolveK8s))()

	f := newK8sDiscoveryFixture(t)
	err := runK8sJob(t, f, jobtypes.KubernetesDiscoveryConfig{ClusterUID: "cluster-1"})
	r.Error(err, "a connection-resolution failure aborts the run (a misconfiguration, not partial data)")
	r.Empty(f.listChecks(t))
}

func TestKubernetesDiscoveryRequiresCluster(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := &jobtypes.KubernetesDiscoveryJobDefinition{}
	_, err := def.CreateJobRun(json.RawMessage(`{}`))
	r.Error(err)

	_, err = def.CreateJobRun(json.RawMessage(`{"namespaces":["prod"]}`))
	r.Error(err)
}
