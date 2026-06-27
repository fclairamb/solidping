package discovery

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const k8sTestNamespace = "prod"

// errForbidden is a static sentinel for the per-namespace skip test.
var errForbidden = errors.New("forbidden")

func ptr32(v int32) *int32 { return &v }

// depFixture builds a Deployment with pod-template labels for selector matching.
func depFixture(uid, name string, replicas, ready int32, lbls map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{UID: ktypes.UID(uid), Name: name, Namespace: k8sTestNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr32(replicas),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: lbls},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready, AvailableReplicas: ready},
	}
}

func boundReplicaSet(uid, name string, ownedByDep bool, lbls map[string]string) *appsv1.ReplicaSet {
	var owners []metav1.OwnerReference
	if ownedByDep {
		owners = []metav1.OwnerReference{{Kind: "Deployment", Name: "owner"}}
	}

	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			UID: ktypes.UID(uid), Name: name, Namespace: k8sTestNamespace, OwnerReferences: owners,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: ptr32(1),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: lbls},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "redis:7"}},
				},
			},
		},
		Status: appsv1.ReplicaSetStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
}

func loadBalancerService(name string, selector map[string]string, port int32, addr string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k8sTestNamespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: selector,
			Ports:    []corev1.ServicePort{{Port: port}},
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: addr}},
			},
		},
	}
}

func nodePortService(name string, selector map[string]string, port, nodePort int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k8sTestNamespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: selector,
			Ports:    []corev1.ServicePort{{Port: port, NodePort: nodePort}},
		},
	}
}

func clusterIPService(name string, selector map[string]string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k8sTestNamespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports:    []corev1.ServicePort{{Port: port}},
		},
	}
}

func ingress(name, host, backingService string, tls bool) *networkingv1.Ingress {
	var tlsRules []networkingv1.IngressTLS
	if tls {
		tlsRules = []networkingv1.IngressTLS{{Hosts: []string{host}}}
	}

	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: k8sTestNamespace},
		Spec: networkingv1.IngressSpec{
			TLS: tlsRules,
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{Name: backingService},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func findWorkload(workloads []DiscoveredWorkload, uid string) *DiscoveredWorkload {
	for i := range workloads {
		if workloads[i].UID == uid {
			return &workloads[i]
		}
	}

	return nil
}

func TestListWorkloadsSurfacesDeploymentsAndBareReplicaSets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "api", 3, 2, map[string]string{"app": "api"}),
		boundReplicaSet("owned-uid", "api-abc123", true, map[string]string{"app": "api"}),
		boundReplicaSet("bare-uid", "legacy-rs", false, map[string]string{"app": "legacy"}),
	)

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err)
	r.Len(workloads, 2, "the Deployment-owned ReplicaSet must be folded into its Deployment")

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Equal(workloadKindDeployment, dep.Kind)
	r.Equal("api", dep.Name)
	r.Equal(k8sTestNamespace, dep.Namespace)
	r.Equal(int32(3), dep.DesiredReplicas)
	r.Equal(int32(2), dep.ReadyReplicas)
	r.Equal([]string{"nginx:1.27"}, dep.Images)

	bare := findWorkload(workloads, "bare-uid")
	r.NotNil(bare)
	r.Equal(workloadKindReplicaSet, bare.Kind)

	r.Nil(findWorkload(workloads, "owned-uid"), "owned RS must not be surfaced")
}

func TestListWorkloadsMatchesLoadBalancerEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "web", 1, 1, map[string]string{"app": "web"}),
		loadBalancerService("web-lb", map[string]string{"app": "web"}, 443, "203.0.113.10"),
	)

	workloads, err := ListWorkloads(t.Context(), cs, []string{k8sTestNamespace}, time.Second, nil)
	r.NoError(err)

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Len(dep.Endpoints, 1)
	r.Equal("203.0.113.10", dep.Endpoints[0].Address)
	r.Equal(443, dep.Endpoints[0].Port)
	r.Equal("https", dep.Endpoints[0].Scheme)
	r.Equal("LoadBalancer", dep.Endpoints[0].Source)
}

func TestListWorkloadsMatchesNodePortEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "api", 1, 1, map[string]string{"app": "api"}),
		nodePortService("api-np", map[string]string{"app": "api"}, 80, 31000),
	)

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err)

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Len(dep.Endpoints, 1)
	r.Equal(31000, dep.Endpoints[0].Port, "the allocated nodePort is used")
	r.Equal("http", dep.Endpoints[0].Scheme, "scheme follows the service port (80)")
	r.Equal("NodePort", dep.Endpoints[0].Source)
}

func TestListWorkloadsMatchesIngressEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "web", 1, 1, map[string]string{"app": "web"}),
		clusterIPService("web-svc", map[string]string{"app": "web"}, 80),
		ingress("web-ing", "web.example.com", "web-svc", true),
	)

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err)

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Len(dep.Endpoints, 1, "the Ingress host backed by the matched ClusterIP service yields an endpoint")
	r.Equal("web.example.com", dep.Endpoints[0].Address)
	r.Equal("https", dep.Endpoints[0].Scheme, "TLS rule -> https")
	r.Equal("Ingress", dep.Endpoints[0].Source)
}

func TestListWorkloadsClusterIPOnlyYieldsNoEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "internal", 1, 1, map[string]string{"app": "internal"}),
		clusterIPService("internal-svc", map[string]string{"app": "internal"}, 8080),
	)

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err)

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Empty(dep.Endpoints, "a ClusterIP-only workload exposes nothing worker-reachable")
}

func TestListWorkloadsServiceSelectorMustMatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "web", 1, 1, map[string]string{"app": "web"}),
		// Selector points at a different workload.
		loadBalancerService("other-lb", map[string]string{"app": "other"}, 80, "203.0.113.20"),
	)

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err)

	dep := findWorkload(workloads, "dep-uid")
	r.NotNil(dep)
	r.Empty(dep.Endpoints, "a non-matching service selector yields no endpoint")
}

func TestListWorkloadsSkipsNamespaceListError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset(
		depFixture("dep-uid", "api", 1, 1, map[string]string{"app": "api"}),
	)
	// A forbidden error on the ReplicaSet list must be logged + skipped, not abort.
	cs.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errForbidden
	})

	workloads, err := ListWorkloads(t.Context(), cs, nil, time.Second, nil)
	r.NoError(err, "a per-resource list error must not abort the run")
	r.Len(workloads, 1, "the Deployment is still surfaced despite the RS list failing")
	r.Equal("api", workloads[0].Name)
}
