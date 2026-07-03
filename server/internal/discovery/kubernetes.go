package discovery

import (
	"context"
	"log/slog"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// defaultWorkloadListTimeout is the overall timeout applied to the cluster
// enumeration when the job config does not set one.
const defaultWorkloadListTimeout = 30 * time.Second

// Workload kinds surfaced by discovery (v1: Deployment + bare ReplicaSet).
const (
	workloadKindDeployment = "Deployment"
	workloadKindReplicaSet = "ReplicaSet"
)

// kindDeploymentOwner is the ownerReference Kind set on a ReplicaSet managed by
// a Deployment. Such ReplicaSets are folded into their Deployment, not surfaced
// twice.
const kindDeploymentOwner = "Deployment"

// WorkloadEndpoint is one externally-reachable address discovered for a
// workload (a NodePort/LoadBalancer Service port or an Ingress host). Pod IPs
// and ClusterIP services are deliberately excluded — they are not reachable
// from an out-of-cluster worker.
type WorkloadEndpoint struct {
	// Address is the worker-reachable host (LB ingress IP/hostname, a node IP,
	// or an Ingress host).
	Address string
	// Port is the port a worker connects to (service port for LB, allocated
	// nodePort for NodePort, 80/443 for an Ingress host).
	Port int
	// Scheme is "http", "https", or "tcp", decided by the port via defaultPorts.
	Scheme string
	// Source describes where the endpoint came from ("LoadBalancer", "NodePort",
	// "Ingress") for display/debugging.
	Source string
}

// WorkloadCondition is a compact, JSON-friendly view of a workload status
// condition (a group-display hint denormalized onto the suggested-check rows).
type WorkloadCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// DiscoveredWorkload is one Deployment or bare ReplicaSet enumerated from a
// cluster. It is the engine's output, independent of the job plumbing and of the
// suggested-check shape, so it can be unit-tested against a fake clientset.
type DiscoveredWorkload struct {
	UID               string // metadata.uid — the stable group identity
	Kind              string // "Deployment" | "ReplicaSet"
	Namespace         string
	Name              string
	Images            []string
	DesiredReplicas   int32
	ReadyReplicas     int32
	AvailableReplicas int32
	Conditions        []WorkloadCondition
	// matchLabels is the workload's pod-template label set, used to match
	// Services/Ingresses by selector. Not exported — an internal join key.
	matchLabels map[string]string
	Endpoints   []WorkloadEndpoint
}

// ListWorkloads connects to a cluster (via the provided clientset) and returns
// its Deployments and bare ReplicaSets, each annotated with any externally
// reachable endpoints matched from NodePort/LoadBalancer Services and Ingresses.
//
// It is read-only (list/get) and resilient: a per-namespace or per-resource
// list error is logged and skipped (an RBAC gap on one namespace must not abort
// the whole scan), so a partial result still surfaces what is visible. When
// namespaces is empty every visible namespace is scanned (cluster-wide list).
func ListWorkloads(
	ctx context.Context,
	client kubernetes.Interface,
	namespaces []string,
	timeout time.Duration,
	log *slog.Logger,
) ([]DiscoveredWorkload, error) {
	if log == nil {
		log = slog.Default()
	}

	if timeout <= 0 {
		timeout = defaultWorkloadListTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	scopes := namespaces
	if len(scopes) == 0 {
		// A single empty namespace means "all namespaces" to the client-go list
		// calls (metav1.NamespaceAll == "").
		scopes = []string{metav1.NamespaceAll}
	}

	workloads := make([]DiscoveredWorkload, 0, len(scopes))
	endpoints := newEndpointIndex()

	for _, ns := range scopes {
		workloads = append(workloads, listDeployments(ctx, client, ns, log)...)
		workloads = append(workloads, listReplicaSets(ctx, client, ns, log)...)
		endpoints.addServices(listServices(ctx, client, ns, log))
		endpoints.addIngresses(listIngresses(ctx, client, ns, log))
	}

	for i := range workloads {
		workloads[i].Endpoints = endpoints.match(&workloads[i])
	}

	return workloads, nil
}

// listDeployments lists Deployments in a namespace, logging and skipping on a
// list error (RBAC gap). Each Deployment becomes a DiscoveredWorkload.
func listDeployments(
	ctx context.Context, client kubernetes.Interface, namespace string, log *slog.Logger,
) []DiscoveredWorkload {
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logNamespaceSkip(ctx, log, "deployments", namespace, err)

		return nil
	}

	out := make([]DiscoveredWorkload, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, deploymentWorkload(&list.Items[i]))
	}

	return out
}

// listReplicaSets lists ReplicaSets in a namespace, skipping those owned by a
// Deployment (folded into their Deployment) and logging/skipping on a list
// error.
func listReplicaSets(
	ctx context.Context, client kubernetes.Interface, namespace string, log *slog.Logger,
) []DiscoveredWorkload {
	list, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logNamespaceSkip(ctx, log, "replicasets", namespace, err)

		return nil
	}

	out := make([]DiscoveredWorkload, 0, len(list.Items))

	for i := range list.Items {
		replicaSet := &list.Items[i]
		if ownedByDeployment(replicaSet.OwnerReferences) {
			continue
		}

		out = append(out, replicaSetWorkload(replicaSet))
	}

	return out
}

// listServices lists every Service in a namespace, logging and skipping on a
// list error. All types are kept: NodePort/LoadBalancer services yield direct
// endpoints, while ClusterIP services are still needed to resolve which
// workload an Ingress backend routes to (an Ingress typically fronts a
// ClusterIP service).
func listServices(
	ctx context.Context, client kubernetes.Interface, namespace string, log *slog.Logger,
) []corev1.Service {
	list, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logNamespaceSkip(ctx, log, "services", namespace, err)

		return nil
	}

	return list.Items
}

// listIngresses lists Ingresses in a namespace, logging and skipping on a list
// error.
func listIngresses(
	ctx context.Context, client kubernetes.Interface, namespace string, log *slog.Logger,
) []networkingv1.Ingress {
	list, err := client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logNamespaceSkip(ctx, log, "ingresses", namespace, err)

		return nil
	}

	return list.Items
}

// ownedByDeployment reports whether any ownerReference points at a Deployment.
func ownedByDeployment(owners []metav1.OwnerReference) bool {
	for i := range owners {
		if owners[i].Kind == kindDeploymentOwner {
			return true
		}
	}

	return false
}

// deploymentWorkload maps a Deployment to a DiscoveredWorkload.
func deploymentWorkload(dep *appsv1.Deployment) DiscoveredWorkload {
	conds := make([]WorkloadCondition, 0, len(dep.Status.Conditions))
	for i := range dep.Status.Conditions {
		cond := &dep.Status.Conditions[i]
		conds = append(conds, WorkloadCondition{
			Type:   string(cond.Type),
			Status: string(cond.Status),
			Reason: cond.Reason,
		})
	}

	return DiscoveredWorkload{
		UID:               string(dep.UID),
		Kind:              workloadKindDeployment,
		Namespace:         dep.Namespace,
		Name:              dep.Name,
		Images:            podImages(dep.Spec.Template.Spec.Containers),
		DesiredReplicas:   derefReplicas(dep.Spec.Replicas),
		ReadyReplicas:     dep.Status.ReadyReplicas,
		AvailableReplicas: dep.Status.AvailableReplicas,
		Conditions:        conds,
		matchLabels:       dep.Spec.Template.Labels,
	}
}

// replicaSetWorkload maps a bare ReplicaSet to a DiscoveredWorkload.
func replicaSetWorkload(replicaSet *appsv1.ReplicaSet) DiscoveredWorkload {
	return DiscoveredWorkload{
		UID:               string(replicaSet.UID),
		Kind:              workloadKindReplicaSet,
		Namespace:         replicaSet.Namespace,
		Name:              replicaSet.Name,
		Images:            podImages(replicaSet.Spec.Template.Spec.Containers),
		DesiredReplicas:   derefReplicas(replicaSet.Spec.Replicas),
		ReadyReplicas:     replicaSet.Status.ReadyReplicas,
		AvailableReplicas: replicaSet.Status.AvailableReplicas,
		matchLabels:       replicaSet.Spec.Template.Labels,
	}
}

// podImages collects container images from a pod template.
func podImages(containers []corev1.Container) []string {
	images := make([]string, 0, len(containers))
	for i := range containers {
		images = append(images, containers[i].Image)
	}

	return images
}

// derefReplicas returns the desired replica count, treating a nil pointer as 0.
func derefReplicas(r *int32) int32 {
	if r == nil {
		return 0
	}

	return *r
}

// logNamespaceSkip logs a per-namespace/per-resource list failure at Warn and
// continues. A 403 (RBAC gap) is the common case; everything else (transport,
// etc.) is treated the same — never abort the run.
func logNamespaceSkip(ctx context.Context, log *slog.Logger, resource, namespace string, err error) {
	scope := namespace
	if scope == metav1.NamespaceAll {
		scope = "<all>"
	}

	log.WarnContext(ctx, "kubernetes discovery: skipping resource list",
		"resource", resource,
		"namespace", scope,
		"forbidden", apierrors.IsForbidden(err),
		"error", err,
	)
}

// endpointIndex accumulates Services and Ingresses and matches them to a
// workload by label selector.
type endpointIndex struct {
	services  []corev1.Service
	ingresses []networkingv1.Ingress
}

func newEndpointIndex() *endpointIndex {
	return &endpointIndex{}
}

func (e *endpointIndex) addServices(svcs []corev1.Service) {
	e.services = append(e.services, svcs...)
}

func (e *endpointIndex) addIngresses(ings []networkingv1.Ingress) {
	e.ingresses = append(e.ingresses, ings...)
}

// match returns the externally reachable endpoints for one workload, joining the
// accumulated Services (by spec.selector ⊆ workload pod-template labels) and the
// Ingresses (by backing a matched Service) in the same namespace. Best-effort: a
// missed match only drops an optional endpoint suggestion.
func (e *endpointIndex) match(workload *DiscoveredWorkload) []WorkloadEndpoint {
	if len(workload.matchLabels) == 0 {
		return nil
	}

	podLabels := labels.Set(workload.matchLabels)

	var endpoints []WorkloadEndpoint

	matchedServices := make(map[string]struct{})

	for i := range e.services {
		svc := &e.services[i]
		if svc.Namespace != workload.Namespace || !serviceSelects(svc, podLabels) {
			continue
		}

		matchedServices[svc.Name] = struct{}{}
		endpoints = append(endpoints, serviceEndpoints(svc)...)
	}

	for i := range e.ingresses {
		ing := &e.ingresses[i]
		if ing.Namespace != workload.Namespace {
			continue
		}

		endpoints = append(endpoints, ingressEndpoints(ing, matchedServices)...)
	}

	return endpoints
}

// serviceSelects reports whether a Service's selector matches a workload's pod
// labels. An empty selector never matches (it would select everything).
func serviceSelects(svc *corev1.Service, podLabels labels.Set) bool {
	if len(svc.Spec.Selector) == 0 {
		return false
	}

	return labels.SelectorFromSet(svc.Spec.Selector).Matches(podLabels)
}

// serviceEndpoints turns a NodePort/LoadBalancer Service into worker-reachable
// endpoints: a LoadBalancer uses its status ingress address + service port; a
// NodePort uses the allocated nodePort (the node IP is unknown out-of-cluster,
// left blank for the operator to fill).
func serviceEndpoints(svc *corev1.Service) []WorkloadEndpoint {
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		return loadBalancerEndpoints(svc)
	case corev1.ServiceTypeNodePort:
		return nodePortEndpoints(svc)
	case corev1.ServiceTypeClusterIP, corev1.ServiceTypeExternalName:
		// Not worker-reachable; only kept in the index to resolve Ingress
		// backends. The kubernetes replica-health check covers these workloads.
		return nil
	default:
		return nil
	}
}

// loadBalancerEndpoints builds endpoints from a LoadBalancer's status ingress
// addresses crossed with its service ports.
func loadBalancerEndpoints(svc *corev1.Service) []WorkloadEndpoint {
	addrs := loadBalancerAddresses(svc)
	if len(addrs) == 0 {
		return nil
	}

	endpoints := make([]WorkloadEndpoint, 0, len(addrs)*len(svc.Spec.Ports))

	for _, addr := range addrs {
		for i := range svc.Spec.Ports {
			port := int(svc.Spec.Ports[i].Port)
			endpoints = append(endpoints, WorkloadEndpoint{
				Address: addr,
				Port:    port,
				Scheme:  schemeForEndpointPort(port),
				Source:  string(corev1.ServiceTypeLoadBalancer),
			})
		}
	}

	return endpoints
}

// loadBalancerAddresses extracts the IP/hostname addresses a LoadBalancer
// exposes from its status.
func loadBalancerAddresses(svc *corev1.Service) []string {
	ingress := svc.Status.LoadBalancer.Ingress

	addrs := make([]string, 0, len(ingress))

	for i := range ingress {
		switch {
		case ingress[i].IP != "":
			addrs = append(addrs, ingress[i].IP)
		case ingress[i].Hostname != "":
			addrs = append(addrs, ingress[i].Hostname)
		}
	}

	return addrs
}

// nodePortEndpoints builds endpoints from a NodePort service's allocated
// nodePorts. The node IP is not known from out-of-cluster, so Address is left
// empty for the operator to complete; the scheme is decided by the service port.
func nodePortEndpoints(svc *corev1.Service) []WorkloadEndpoint {
	endpoints := make([]WorkloadEndpoint, 0, len(svc.Spec.Ports))

	for i := range svc.Spec.Ports {
		nodePort := int(svc.Spec.Ports[i].NodePort)
		if nodePort == 0 {
			continue
		}

		endpoints = append(endpoints, WorkloadEndpoint{
			Address: "",
			Port:    nodePort,
			Scheme:  schemeForEndpointPort(int(svc.Spec.Ports[i].Port)),
			Source:  string(corev1.ServiceTypeNodePort),
		})
	}

	return endpoints
}

// ingressEndpoints turns an Ingress into host-based http/https endpoints. Only
// rules whose backing service was matched to this workload are emitted, so an
// Ingress fanning out to many services only contributes the relevant hosts.
func ingressEndpoints(ing *networkingv1.Ingress, matchedServices map[string]struct{}) []WorkloadEndpoint {
	scheme := schemeHTTPLower
	if len(ing.Spec.TLS) > 0 {
		scheme = schemeHTTPSLower
	}

	var endpoints []WorkloadEndpoint

	for i := range ing.Spec.Rules {
		rule := &ing.Spec.Rules[i]
		if rule.Host == "" || !ruleBacksMatchedService(rule, matchedServices) {
			continue
		}

		endpoints = append(endpoints, WorkloadEndpoint{
			Address: rule.Host,
			Port:    ingressPortForScheme(scheme),
			Scheme:  scheme,
			Source:  "Ingress",
		})
	}

	return endpoints
}

// ruleBacksMatchedService reports whether an Ingress rule routes to any Service
// that was matched to the workload.
func ruleBacksMatchedService(rule *networkingv1.IngressRule, matchedServices map[string]struct{}) bool {
	if rule.HTTP == nil {
		return false
	}

	for i := range rule.HTTP.Paths {
		svc := rule.HTTP.Paths[i].Backend.Service
		if svc == nil {
			continue
		}

		if _, ok := matchedServices[svc.Name]; ok {
			return true
		}
	}

	return false
}

// ingressPortForScheme returns the conventional port for an Ingress scheme.
func ingressPortForScheme(scheme string) int {
	if scheme == schemeHTTPSLower {
		return 443
	}

	return 80
}

// schemeForEndpointPort maps a port to an endpoint scheme using the
// authoritative defaultPorts table: an http(s) port → its scheme, everything
// else → tcp. This keeps the port→scheme decision identical to the LAN/container
// suggesters.
func schemeForEndpointPort(port int) string {
	for i := range defaultPorts {
		if defaultPorts[i].Port != port {
			continue
		}

		if defaultPorts[i].CheckType == checkTypeHTTP {
			if strings.HasPrefix(defaultPorts[i].URLTmpl, schemeHTTPSLower) {
				return schemeHTTPSLower
			}

			return schemeHTTPLower
		}

		return checkTypeTCP
	}

	return checkTypeTCP
}
