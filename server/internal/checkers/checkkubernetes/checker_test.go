package checkkubernetes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
)

const (
	testNamespace = "prod"
	testCluster   = "cluster-uid"
)

// errBadCluster is a static sentinel for the resolver-failure test (err113).
var errBadCluster = errors.New("bad cluster")

func ptrInt32(v int32) *int32 { return &v }

// deployment builds an apps/v1 Deployment fixture with the given replica counts.
func deployment(name string, desired, ready, available, updated, unavailable int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrInt32(desired),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:            desired,
			ReadyReplicas:       ready,
			AvailableReplicas:   available,
			UpdatedReplicas:     updated,
			UnavailableReplicas: unavailable,
		},
	}
}

// stuckDeployment is a Deployment whose Progressing condition reports
// ProgressDeadlineExceeded.
func stuckDeployment(name string) *appsv1.Deployment {
	dep := deployment(name, 3, 1, 1, 1, 2)
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:   appsv1.DeploymentProgressing,
			Status: corev1.ConditionFalse,
			Reason: "ProgressDeadlineExceeded",
		},
	}

	return dep
}

func replicaSet(name string, desired, ready, available int32) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptrInt32(desired),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "redis:7"}},
				},
			},
		},
		Status: appsv1.ReplicaSetStatus{
			Replicas:          desired,
			ReadyReplicas:     ready,
			AvailableReplicas: available,
		},
	}
}

// fixedResolver returns a resolver that always yields the given clientset.
func fixedResolver(cs kubernetes.Interface) checkkubernetes.ClientsetResolver {
	return func(_ context.Context, _ string) (kubernetes.Interface, error) {
		return cs, nil
	}
}

// runExecute runs the checker with a resolver bound through the context.
func runExecute(
	t *testing.T, cfg *checkkubernetes.KubernetesConfig, resolver checkkubernetes.ClientsetResolver,
) *checkerdef.Result {
	t.Helper()

	ctx := checkkubernetes.WithResolver(t.Context(), resolver)
	checker := &checkkubernetes.KubernetesChecker{}

	res, err := checker.Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, res)

	return res
}

func deploymentConfig(name string) *checkkubernetes.KubernetesConfig {
	return &checkkubernetes.KubernetesConfig{
		ClusterUID: testCluster,
		Namespace:  testNamespace,
		Kind:       checkkubernetes.KindDeployment,
		Name:       name,
	}
}

func TestExecuteDeploymentStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dep        *appsv1.Deployment
		wantStatus checkerdef.Status
	}{
		{"ready equals desired -> up", deployment("web", 3, 3, 3, 3, 0), checkerdef.StatusUp},
		{"partial -> warning", deployment("web", 3, 1, 1, 1, 2), checkerdef.StatusWarning},
		{"zero ready -> down", deployment("web", 3, 0, 0, 0, 3), checkerdef.StatusDown},
		{"scaled to zero -> warning", deployment("web", 0, 0, 0, 0, 0), checkerdef.StatusWarning},
		{"progress deadline exceeded -> down", stuckDeployment("web"), checkerdef.StatusDown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cs := fake.NewSimpleClientset(tc.dep)

			res := runExecute(t, deploymentConfig("web"), fixedResolver(cs))

			r.Equal(tc.wantStatus, res.Status, "status string: %s", res.Status)
			r.Equal(tc.dep.Status.ReadyReplicas, res.Metrics["readyReplicas"])
			r.Equal(*tc.dep.Spec.Replicas, res.Metrics["desiredReplicas"])
			r.Equal(testNamespace, res.Output["namespace"])
			r.Contains(res.Metrics, "query_time_ms")
		})
	}
}

func TestExecuteReplicaSet(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cs := fake.NewSimpleClientset(replicaSet("cache", 2, 2, 2))

	cfg := deploymentConfig("cache")
	cfg.Kind = checkkubernetes.KindReplicaSet

	res := runExecute(t, cfg, fixedResolver(cs))

	r.Equal(checkerdef.StatusUp, res.Status)
	r.Equal(int32(2), res.Metrics["readyReplicas"])
	r.Equal(checkkubernetes.KindReplicaSet, res.Output["kind"])
}

func TestExecuteWorkloadNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cs := fake.NewSimpleClientset() // empty: workload missing

	res := runExecute(t, deploymentConfig("absent"), fixedResolver(cs))

	r.Equal(checkerdef.StatusDown, res.Status)
	r.Equal("workload not found", res.Output[checkerdef.OutputKeyError])
}

func TestExecuteResolverError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	resolver := func(_ context.Context, _ string) (kubernetes.Interface, error) {
		return nil, errBadCluster
	}

	res := runExecute(t, deploymentConfig("web"), resolver)

	r.Equal(checkerdef.StatusError, res.Status)
	r.Contains(res.Output[checkerdef.OutputKeyError], "bad cluster")
}

func TestExecuteAPIError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cs := fake.NewSimpleClientset()
	// Inject a generic (non-NotFound, non-deadline) API error.
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("apiserver down")
	})

	res := runExecute(t, deploymentConfig("web"), fixedResolver(cs))

	r.Equal(checkerdef.StatusDown, res.Status)
	r.Contains(res.Output[checkerdef.OutputKeyError], "workload fetch failed")
}

func TestExecuteDeadline(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cs := fake.NewSimpleClientset()
	// Simulate a slow API server: the reactor sleeps past the checker's
	// per-execution timeout, so by the time the Get returns the error the
	// derived context is already expired. classifyFetchError then sees
	// ctx.Err() != nil and reports StatusTimeout.
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		time.Sleep(20 * time.Millisecond)

		return true, nil, context.DeadlineExceeded
	})

	cfg := deploymentConfig("web")
	cfg.Timeout = 5 * time.Millisecond

	res := runExecute(t, cfg, fixedResolver(cs))

	r.Equal(checkerdef.StatusTimeout, res.Status)
}

func TestExecuteNoResolver(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &checkkubernetes.KubernetesChecker{}

	// No resolver in context and no package-level resolver set in this test
	// process → ErrResolverNotConfigured.
	_, err := checker.Execute(t.Context(), deploymentConfig("web"))
	r.ErrorIs(err, checkkubernetes.ErrResolverNotConfigured)
}
